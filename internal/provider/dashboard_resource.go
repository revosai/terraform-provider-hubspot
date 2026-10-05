// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

var (
	_ resource.Resource                   = &dashboardResource{}
	_ resource.ResourceWithConfigure      = &dashboardResource{}
	_ resource.ResourceWithImportState    = &dashboardResource{}
	_ resource.ResourceWithValidateConfig = &dashboardResource{}
)

// numericIDRegexp matches HubSpot's numeric object IDs (dashboards, reports,
// users, business units).
var numericIDRegexp = regexp.MustCompile(`^[0-9]+$`)

// dashboardResource implements hubspot_dashboard on the Analytics Reporting
// API (public beta). Destroy archives (restorable). Widget membership is
// managed only when report_ids is set; widget layout and tags are read-only.
type dashboardResource struct {
	client *client.Client
}

// NewDashboardResource returns the hubspot_dashboard resource.
func NewDashboardResource() resource.Resource { return &dashboardResource{} }

type dashboardResourceModel struct {
	ID                   types.String `tfsdk:"id"`
	Name                 types.String `tfsdk:"name"`
	Description          types.String `tfsdk:"description"`
	BusinessUnitID       types.String `tfsdk:"business_unit_id"`
	OwnerUserID          types.String `tfsdk:"owner_user_id"`
	Permissions          types.Object `tfsdk:"permissions"`
	ReportIDs            types.Set    `tfsdk:"report_ids"`
	CloneFromDashboardID types.String `tfsdk:"clone_from_dashboard_id"`
	CloneReports         types.Bool   `tfsdk:"clone_reports"`
	Widgets              types.List   `tfsdk:"widgets"`
	Tags                 types.List   `tfsdk:"tags"`
	CreatedAt            types.String `tfsdk:"created_at"`
	CreatedByUserID      types.String `tfsdk:"created_by_user_id"`
	UpdatedAt            types.String `tfsdk:"updated_at"`
	UpdatedByUserID      types.String `tfsdk:"updated_by_user_id"`
}

func (r *dashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func numericString(what string) validator.String {
	return stringvalidator.RegexMatches(numericIDRegexp, "must be a numeric "+what)
}

func (r *dashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	widgets := resourceWidgetsAttribute()
	widgets.MarkdownDescription += " Re-read on every refresh; changes only when `report_ids` changes or " +
		"widgets are edited in the HubSpot UI."
	widgets.PlanModifiers = []planmodifier.List{dashboardWidgetsPlanModifier{}}

	tags := resourceTagsAttribute("dashboard")
	tags.PlanModifiers = []planmodifier.List{listplanmodifier.UseStateForUnknown()}

	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a HubSpot reporting dashboard via the Analytics Reporting API.\n\n" +
			reportingBetaMarkdown + "\n\n" +
			"**Widgets.** When `report_ids` is set, the provider owns the dashboard's widget membership " +
			"exactly: missing reports are added (`PUT …/widgets/{reportId}`) and unlisted ones removed " +
			"(`DELETE …/widgets/{reportId}`), diffed by report ID. When `report_ids` is **null (omitted)** " +
			"the widgets are unmanaged and never touched, so dashboards arranged in the HubSpot UI stay as " +
			"they are. Widget layout (position and size) and tags cannot be set through the API; they are " +
			"exposed read-only as `widgets` and `tags`. Reports cannot be created from scratch via the API " +
			"either — build them in the UI (or clone them with `hubspot_report`) and reference their IDs.\n\n" +
			"**Cloning.** Set `clone_from_dashboard_id` to create the dashboard as a copy of an existing one " +
			"(optionally with copies of its reports via `clone_reports`). The clone arguments are " +
			"create-time only: changing them later forces replacement, except when their prior value is " +
			"null (e.g. after import), which never plans a replacement.\n\n" +
			"**Destroy archives** the dashboard (`PATCH {archived: true}`, confirmed by a read of the archived " +
			"copy) instead of deleting it; it can be restored in HubSpot. Archived or deleted dashboards are " +
			"removed from state on refresh and re-created on the next apply.\n\n" +
			"Import with the numeric dashboard ID. After import `report_ids` is null (widgets unmanaged); " +
			"adding `report_ids` to the configuration adopts the dashboard's widget membership.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The dashboard's numeric ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Dashboard name. Mutable (in-place update).",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Dashboard description. Mutable; removing it clears the description in " +
					"HubSpot (and clears a description copied from a clone source).",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"business_unit_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "ID of the business unit (brand) the dashboard belongs to. Defaults to the " +
					"account's default business unit (`0`), or the clone source's. Mutable; removing it from the " +
					"configuration keeps the current value.",
				Validators:    []validator.String{numericString("business unit ID")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"owner_user_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "ID of the user who owns the dashboard. Defaults to the token's user. " +
					"HubSpot's create call has no owner field, so a configured owner is applied by an update " +
					"right after creation. Mutable; removing it from the configuration keeps the current owner.",
				Validators:    []validator.String{numericString("user ID")},
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"permissions": resourcePermissionsAttribute("dashboard"),
			"report_ids": schema.SetAttribute{
				Optional:    true,
				ElementType: types.StringType,
				MarkdownDescription: "IDs of the reports shown on the dashboard. When set, membership is " +
					"managed exactly (missing reports are added, unlisted ones removed; `[]` removes all). " +
					"When null (omitted), widgets are **unmanaged** and never touched. Archived or unknown " +
					"report IDs fail the apply with an error (HubSpot's create-time `reportIdsToAdd` is " +
					"best-effort, so membership is always verified).",
				Validators: []validator.Set{setvalidator.ValueStringsAre(numericString("report ID"))},
			},
			"clone_from_dashboard_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Create the dashboard as a clone of this dashboard ID. **Create-time " +
					"only**: changing it forces replacement (unless the prior value is null, e.g. after " +
					"import). The clone starts with the source's widgets, description and business unit; " +
					"configured attributes are applied on top.",
				Validators:    []validator.String{numericString("dashboard ID")},
				PlanModifiers: []planmodifier.String{requiresReplaceIfPriorNotNull()},
			},
			"clone_reports": schema.BoolAttribute{
				Optional: true,
				MarkdownDescription: "Only valid with `clone_from_dashboard_id`. `true` also clones every report " +
					"on the source dashboard, so the new dashboard's widgets point at **new** report IDs; " +
					"`false` or omitted makes the clone reference the source's reports. **Create-time only**: " +
					"changing it forces replacement (unless the prior value is null, e.g. after import).",
				PlanModifiers: []planmodifier.Bool{requiresReplaceIfPriorNotNullBool()},
			},
			"widgets": widgets,
			"tags":    tags,
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp (ISO 8601).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_by_user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the user who created the dashboard.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last-update timestamp (ISO 8601).",
			},
			"updated_by_user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "ID of the user who last updated the dashboard.",
			},
		},
	}
}

// dashboardWidgetsPlanModifier keeps the prior `widgets` value when an update
// cannot change widgets (report_ids unchanged, or switched to unmanaged), and
// leaves it unknown when the membership will be reconciled.
type dashboardWidgetsPlanModifier struct{}

func (dashboardWidgetsPlanModifier) Description(_ context.Context) string {
	return "Keeps the prior widgets unless report_ids changes."
}

func (m dashboardWidgetsPlanModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (dashboardWidgetsPlanModifier) PlanModifyList(ctx context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.State.Raw.IsNull() || req.Plan.Raw.IsNull() || req.StateValue.IsNull() || !req.PlanValue.IsUnknown() {
		return
	}
	var planIDs, stateIDs types.Set
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("report_ids"), &planIDs)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("report_ids"), &stateIDs)...)
	if resp.Diagnostics.HasError() || planIDs.IsUnknown() {
		return
	}
	if planIDs.IsNull() || planIDs.Equal(stateIDs) {
		resp.PlanValue = req.StateValue
	}
}

func (r *dashboardResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, ok := clientFromProviderData(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected resource Configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}
	r.client = c
}

func (r *dashboardResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg dashboardResourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validatePermissions(ctx, cfg.Permissions, path.Root("permissions"), false)...)
	resp.Diagnostics.Append(validateNoEmptyGrantSets(ctx, cfg.Permissions)...)

	if !cfg.CloneReports.IsNull() && cfg.CloneFromDashboardID.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("clone_reports"), "clone_reports without a clone source",
			"`clone_reports` requires `clone_from_dashboard_id`: it only applies when the dashboard is created "+
				"as a clone.")
	}
}

// validateNoEmptyGrantSets rejects `view = []` / `edit = []`: HubSpot stores
// no grants as absent, which reads back as null, so an empty set could never
// converge. Omit the attribute instead.
func validateNoEmptyGrantSets(ctx context.Context, obj types.Object) diag.Diagnostics {
	var diags diag.Diagnostics
	m, d := permissionsFromObject(ctx, obj)
	diags.Append(d...)
	if diags.HasError() || obj.IsNull() || obj.IsUnknown() {
		return diags
	}
	for name, s := range map[string]types.Set{"view": m.View, "edit": m.Edit} {
		if !s.IsNull() && !s.IsUnknown() && len(s.Elements()) == 0 {
			diags.AddAttributeError(path.Root("permissions").AtName(name), "Empty permission grant set",
				fmt.Sprintf("permissions.%s must not be an empty set; omit it (null) when there are no %s grants.", name, name))
		}
	}
	return diags
}

// fromAPI overwrites the model with the dashboard as read from HubSpot.
// Sections the response omits (permissions) keep their current value;
// report_ids is refreshed from the widgets only when it is managed (non-null).
// Clone arguments are never touched (they are not readable).
func (m *dashboardResourceModel) fromAPI(d *apiDashboard) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(d.ID)
	m.Name = types.StringValue(d.Name)
	m.Description = optionalString(d.Description)
	m.BusinessUnitID = types.StringValue(d.BusinessUnitID)
	m.OwnerUserID = optionalString(d.OwnerUserID)
	if d.Permissions != nil {
		p, dd := dashboardPermissionsToObject(d.Permissions)
		diags.Append(dd...)
		m.Permissions = p
	}
	if !m.ReportIDs.IsNull() {
		ids := widgetReportIDs(d.Widgets)
		elems := make([]attr.Value, 0, len(ids))
		for _, id := range ids {
			elems = append(elems, types.StringValue(id))
		}
		s, dd := types.SetValue(types.StringType, elems)
		diags.Append(dd...)
		m.ReportIDs = s
	}
	w, dd := widgetsToList(d.Widgets)
	diags.Append(dd...)
	m.Widgets = w
	t, dd := tagsToList(d.Tags)
	diags.Append(dd...)
	m.Tags = t
	m.CreatedAt = types.StringValue(d.CreatedAt)
	m.CreatedByUserID = optionalString(d.CreatedByUserID)
	m.UpdatedAt = types.StringValue(d.UpdatedAt)
	m.UpdatedByUserID = optionalString(d.UpdatedByUserID)
	return diags
}

// dashboardMetadataPatch builds the PATCH body that moves current to plan:
// only changed fields, never `archived` (which cannot be combined). Unknown
// or null computed values in plan (business unit, owner) are left alone; a
// null description is cleared with an explicit null.
func dashboardMetadataPatch(ctx context.Context, plan, current dashboardResourceModel) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	body := map[string]any{}
	if !plan.Name.Equal(current.Name) {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(current.Description) {
		if plan.Description.IsNull() {
			body["description"] = nil
		} else {
			body["description"] = plan.Description.ValueString()
		}
	}
	if !plan.BusinessUnitID.IsNull() && !plan.BusinessUnitID.IsUnknown() && !plan.BusinessUnitID.Equal(current.BusinessUnitID) {
		body["businessUnitId"] = plan.BusinessUnitID.ValueString()
	}
	if !plan.OwnerUserID.IsNull() && !plan.OwnerUserID.IsUnknown() && !plan.OwnerUserID.Equal(current.OwnerUserID) {
		body["ownerUserId"] = plan.OwnerUserID.ValueString()
	}
	if !plan.Permissions.Equal(current.Permissions) {
		p, d := dashboardPermissionsToWire(ctx, plan.Permissions)
		diags.Append(d...)
		body["permissions"] = p
	}
	return body, diags
}

// setStrings decodes a known string set (sorted).
func setStrings(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	var out []string
	diags := s.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out, diags
}

// reportingFailure is a summary/detail pair for a diagnostic.
type reportingFailure struct {
	summary, detail string
}

// reconcileWidgets makes the dashboard's widget membership exactly desired:
// PUT adds missing reports, DELETE removes unlisted ones, then a fresh read
// verifies the result (HubSpot's reportIdsToAdd is best-effort).
func (r *dashboardResource) reconcileWidgets(ctx context.Context, id string, desired []string) *reportingFailure {
	p := dashboardPath(id)
	d, _, err := getDashboard(ctx, r.client, id, false)
	if err != nil {
		return &reportingFailure{"Unable to read HubSpot dashboard widgets",
			fmt.Sprintf("GET /%s failed: %s", p, reportingErrorDetail(err))}
	}
	want := map[string]bool{}
	for _, rid := range desired {
		want[rid] = true
	}
	have := map[string]bool{}
	for _, rid := range widgetReportIDs(d.Widgets) {
		have[rid] = true
	}
	for _, rid := range desired {
		if have[rid] {
			continue
		}
		wp := p + "/widgets/" + rid
		if err := r.client.Put(ctx, wp, nil, nil); err != nil {
			return &reportingFailure{
				fmt.Sprintf("Unable to add report %s to HubSpot dashboard %s", rid, id),
				fmt.Sprintf("PUT /%s failed: %s\n\nCheck that report %s exists and is not archived.",
					wp, reportingErrorDetail(err), rid)}
		}
	}
	for rid := range have {
		if want[rid] {
			continue
		}
		wp := p + "/widgets/" + rid
		if err := r.client.Delete(ctx, wp, nil); err != nil && !client.IsNotFound(err) {
			return &reportingFailure{
				fmt.Sprintf("Unable to remove report %s from HubSpot dashboard %s", rid, id),
				fmt.Sprintf("DELETE /%s failed: %s", wp, reportingErrorDetail(err))}
		}
	}

	d, _, err = getDashboard(ctx, r.client, id, false)
	if err != nil {
		return &reportingFailure{"Unable to verify HubSpot dashboard widgets",
			fmt.Sprintf("GET /%s failed: %s", p, reportingErrorDetail(err))}
	}
	got := widgetReportIDs(d.Widgets)
	var missing, extra []string
	gotSet := map[string]bool{}
	for _, rid := range got {
		gotSet[rid] = true
		if !want[rid] {
			extra = append(extra, rid)
		}
	}
	for _, rid := range desired {
		if !gotSet[rid] {
			missing = append(missing, rid)
		}
	}
	if len(missing) > 0 || len(extra) > 0 {
		return &reportingFailure{
			fmt.Sprintf("Unable to add report(s) %s to HubSpot dashboard %s", strings.Join(missing, ", "), id),
			fmt.Sprintf("After reconciling widgets, dashboard %s shows reports [%s] but the configuration "+
				"wants [%s] (missing: [%s], unexpected: [%s]). Check that every report in report_ids exists "+
				"and is not archived.", id, strings.Join(got, ", "), strings.Join(desired, ", "),
				strings.Join(missing, ", "), strings.Join(extra, ", "))}
	}
	return nil
}

// refreshInto reads the dashboard and stores it in m. Returns a failure on
// any read error (404 included).
func (r *dashboardResource) refreshInto(ctx context.Context, id string, m *dashboardResourceModel) (*reportingFailure, diag.Diagnostics) {
	d, _, err := getDashboard(ctx, r.client, id, false)
	if err != nil {
		return &reportingFailure{"Unable to read HubSpot dashboard",
			fmt.Sprintf("GET /%s failed: %s", dashboardPath(id), reportingErrorDetail(err))}, nil
	}
	return nil, m.fromAPI(d)
}

// applyAfterWrite runs the follow-ups shared by Create and Update once the
// dashboard exists — metadata PATCH (from current to plan), widget
// reconciliation (managed report_ids only) and the final read — persisting
// state after every step so a failure leaves a tracked, convergent resource
// (decision #9).
func (r *dashboardResource) applyAfterWrite(ctx context.Context, plan, current dashboardResourceModel, setState func(dashboardResourceModel) diag.Diagnostics) diag.Diagnostics {
	var diags diag.Diagnostics
	id := current.ID.ValueString()

	fail := func(f *reportingFailure) diag.Diagnostics {
		// Best effort: record what actually exists before reporting the error.
		refreshed := current
		if rf, d := r.refreshInto(ctx, id, &refreshed); rf == nil && !d.HasError() {
			diags.Append(setState(refreshed)...)
		}
		diags.AddError(f.summary, f.detail)
		return diags
	}

	body, d := dashboardMetadataPatch(ctx, plan, current)
	diags.Append(d...)
	if diags.HasError() {
		return diags
	}
	if len(body) > 0 {
		p := dashboardPath(id)
		if err := r.client.Patch(ctx, p, body, nil); err != nil {
			return fail(&reportingFailure{"Unable to update HubSpot dashboard",
				fmt.Sprintf("PATCH /%s failed: %s", p, reportingErrorDetail(err))})
		}
	}

	if !plan.ReportIDs.IsNull() && !plan.ReportIDs.IsUnknown() {
		desired, d := setStrings(ctx, plan.ReportIDs)
		diags.Append(d...)
		if diags.HasError() {
			return diags
		}
		if f := r.reconcileWidgets(ctx, id, desired); f != nil {
			return fail(f)
		}
	}

	final := plan
	final.ID = current.ID
	f, d := r.refreshInto(ctx, id, &final)
	diags.Append(d...)
	if f != nil {
		diags.AddError(f.summary, f.detail)
		return diags
	}
	diags.Append(setState(final)...)
	return diags
}

func (r *dashboardResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	perms, d := dashboardPermissionsToWire(ctx, plan.Permissions)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	var created apiDashboard
	if src := plan.CloneFromDashboardID; !src.IsNull() {
		p := dashboardPath(src.ValueString()) + "/clone"
		body := map[string]any{
			"name":         plan.Name.ValueString(),
			"permissions":  perms,
			"cloneReports": plan.CloneReports.ValueBool(), // null => false
		}
		if err := r.client.Post(ctx, p, body, &created); err != nil {
			resp.Diagnostics.AddError("Unable to clone HubSpot dashboard",
				fmt.Sprintf("POST /%s failed: %s", p, reportingErrorDetail(err)))
			return
		}
	} else {
		body := map[string]any{
			"name":        plan.Name.ValueString(),
			"permissions": perms,
		}
		if !plan.Description.IsNull() {
			body["description"] = plan.Description.ValueString()
		}
		if !plan.BusinessUnitID.IsNull() && !plan.BusinessUnitID.IsUnknown() {
			body["businessUnitId"] = plan.BusinessUnitID.ValueString()
		}
		if !plan.ReportIDs.IsNull() && len(plan.ReportIDs.Elements()) > 0 {
			ids, d := setStrings(ctx, plan.ReportIDs)
			resp.Diagnostics.Append(d...)
			if resp.Diagnostics.HasError() {
				return
			}
			// Best effort on HubSpot's side; verified by the reconcile below.
			body["reportIdsToAdd"] = ids
		}
		if err := r.client.Post(ctx, dashboardsBasePath, body, &created); err != nil {
			resp.Diagnostics.AddError("Unable to create HubSpot dashboard",
				fmt.Sprintf("POST /%s failed: %s", dashboardsBasePath, reportingErrorDetail(err)))
			return
		}
	}
	if created.ID == "" {
		resp.Diagnostics.AddError("Unable to create HubSpot dashboard",
			"HubSpot's create/clone response did not include a dashboard ID.")
		return
	}

	// Persist immediately: the dashboard exists now, so any later failure
	// must leave it tracked (tainted) rather than leaked.
	current := plan
	resp.Diagnostics.Append(current.fromAPI(&created)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &current)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.Append(r.applyAfterWrite(ctx, plan, current, func(m dashboardResourceModel) diag.Diagnostics {
		return resp.State.Set(ctx, &m)
	})...)
}

func (r *dashboardResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	d, _, err := getDashboard(ctx, r.client, id, false)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read HubSpot dashboard",
			fmt.Sprintf("GET /%s failed: %s", dashboardPath(id), reportingErrorDetail(err)))
		return
	}
	if d.Archived {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(state.fromAPI(d)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dashboardResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dashboardResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	// Clone arguments are create-time only; an update just records them
	// (e.g. added to the configuration after import).
	state.CloneFromDashboardID = plan.CloneFromDashboardID
	state.CloneReports = plan.CloneReports
	resp.Diagnostics.Append(r.applyAfterWrite(ctx, plan, state, func(m dashboardResourceModel) diag.Diagnostics {
		return resp.State.Set(ctx, &m)
	})...)
}

func (r *dashboardResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dashboardResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := archiveReportingObject(ctx, r.client, dashboardPath(state.ID.ValueString())); err != nil {
		resp.Diagnostics.AddError("Unable to archive HubSpot dashboard", reportingErrorDetail(err))
	}
}

func (r *dashboardResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !numericIDRegexp.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected the numeric HubSpot dashboard ID (e.g. 24680135), got %q.", req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
