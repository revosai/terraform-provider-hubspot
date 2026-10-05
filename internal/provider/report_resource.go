// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"
	"reflect"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

var (
	_ resource.Resource                   = &reportResource{}
	_ resource.ResourceWithConfigure      = &reportResource{}
	_ resource.ResourceWithImportState    = &reportResource{}
	_ resource.ResourceWithModifyPlan     = &reportResource{}
	_ resource.ResourceWithValidateConfig = &reportResource{}
)

// reportResourceIDPattern matches HubSpot reporting object IDs (decimal digits).
var reportResourceIDPattern = regexp.MustCompile(`^[0-9]+$`)

// reportResource implements hubspot_report: the metadata (name, description,
// business unit, owner, permissions) of a report in the Analytics Reporting
// API public beta. The API has no public report-configuration format, so a
// report is created by cloning an existing (UI-built) one, or adopted via
// import; its query and visualization stay UI-managed.
type reportResource struct {
	client *client.Client
}

// NewReportResource returns the hubspot_report resource.
func NewReportResource() resource.Resource { return &reportResource{} }

type reportResourceModel struct {
	ID              types.String `tfsdk:"id"`
	SourceReportID  types.String `tfsdk:"source_report_id"`
	Name            types.String `tfsdk:"name"`
	Description     types.String `tfsdk:"description"`
	BusinessUnitID  types.String `tfsdk:"business_unit_id"`
	OwnerUserID     types.String `tfsdk:"owner_user_id"`
	Permissions     types.Object `tfsdk:"permissions"`
	Tags            types.List   `tfsdk:"tags"`
	CreatedAt       types.String `tfsdk:"created_at"`
	CreatedByUserID types.String `tfsdk:"created_by_user_id"`
	UpdatedAt       types.String `tfsdk:"updated_at"`
	UpdatedByUserID types.String `tfsdk:"updated_by_user_id"`
}

func (r *reportResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_report"
}

func (r *reportResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a HubSpot report's **metadata** — name, description, business unit, owner and " +
			"permissions — via the Analytics Reporting API (`/analytics/reporting/2027-03-beta/reports`).\n\n" +
			reportingBetaMarkdown + "\n\n" +
			"**What the API cannot do.** HubSpot has no public report-configuration format: a report cannot be " +
			"created from scratch, and its configuration (data sources, query, filters, visualization) can be " +
			"neither read nor written. Build the report in the HubSpot UI, then either **clone** it with " +
			"`source_report_id` (the clone gets this resource's name and permissions) or adopt it with " +
			"`import`. The configuration stays UI-managed: it is never captured in state and survives every " +
			"apply untouched. Creating this resource without `source_report_id` is a plan-time error.\n\n" +
			"**`source_report_id` is create-time only.** Changing it on a report created by this resource " +
			"forces replacement (a new clone; the old report is archived). On an imported report its prior " +
			"value is null, so adding it later never plans a replacement.\n\n" +
			"**Destroy archives the report** (restorable in HubSpot) and confirms the archive with a read — " +
			"including reports adopted via `import`. To stop managing a report without archiving it, remove " +
			"the resource from configuration together with a `removed { from = hubspot_report.x  lifecycle " +
			"{ destroy = false } }` block (Terraform ≥ 1.7, OpenTofu ≥ 1.7).\n\n" +
			"Tags are read-only (computed). Import with the numeric report ID.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The report ID.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"source_report_id": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "ID of an existing report to clone on create — typically a template built in " +
					"the HubSpot UI (the clone copies its configuration; the source is not modified). Required to " +
					"create the resource (the API cannot create a report from scratch); not needed for imported " +
					"reports. Create-time only: changing it forces replacement, unless the prior value is null " +
					"(e.g. after import). Never read back from the API.",
				Validators: []validator.String{
					stringvalidator.RegexMatches(reportResourceIDPattern, "must be a numeric HubSpot report ID"),
				},
				PlanModifiers: []planmodifier.String{requiresReplaceIfPriorNotNull()},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Report name. Updated in place.",
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"description": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "Report description. Updated in place; removing it clears the description " +
					"(a clone otherwise inherits the source's description, which is cleared when this is unset).",
				Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"business_unit_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Business unit (brand) the report belongs to; `\"0\"` is the default business " +
					"unit. When unset, the value HubSpot assigns (inherited from the clone source) is kept. Updated " +
					"in place.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"owner_user_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "HubSpot user ID of the report owner. When unset, the value HubSpot assigns " +
					"(the token's user, for a clone) is kept. Updated in place.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"permissions": resourcePermissionsAttribute("report"),
			"tags":        resourceTagsAttribute("report"),
			"created_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Creation timestamp (ISO 8601).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"created_by_user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "HubSpot user ID of the report's creator.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"updated_at": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Last-update timestamp (ISO 8601).",
			},
			"updated_by_user_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "HubSpot user ID of the last user to update the report.",
			},
		},
	}
}

func (r *reportResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *reportResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var perms types.Object
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("permissions"), &perms)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(validatePermissions(ctx, perms, path.Root("permissions"), true)...)
}

// ModifyPlan rejects, at plan time, creating a report without a clone
// source: the API has no from-scratch create. It never fires on update,
// destroy or import (all have a prior state).
func (r *reportResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if !req.State.Raw.IsNull() || req.Plan.Raw.IsNull() {
		return
	}
	var source types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("source_report_id"), &source)...)
	if resp.Diagnostics.HasError() || !source.IsNull() {
		return // known or unknown (resolved at apply) source: fine
	}
	resp.Diagnostics.AddAttributeError(path.Root("source_report_id"),
		"hubspot_report requires source_report_id to create a report",
		"HubSpot's Reporting API has no public report-configuration format, so a report cannot be created "+
			"from scratch. Build the report in the HubSpot UI, then either clone it by setting "+
			"source_report_id to its ID, or adopt it as-is with `import` (an import block or "+
			"`terraform import` / `tofu import` with the report ID). If this plan replaces an existing "+
			"report, keep source_report_id in the configuration.")
}

// reportResourcePermissionsEqual compares two permissions objects by their wire
// form, so set ordering and null-vs-empty grant sets never produce a diff.
func reportResourcePermissionsEqual(ctx context.Context, a, b types.Object) bool {
	if a.IsNull() || a.IsUnknown() || b.IsNull() || b.IsUnknown() {
		return a.Equal(b)
	}
	wa, d1 := reportPermissionsToWire(ctx, a)
	wb, d2 := reportPermissionsToWire(ctx, b)
	if d1.HasError() || d2.HasError() {
		return false
	}
	return reflect.DeepEqual(wa, wb)
}

// flattenReportResource writes an API report into m. The prior permissions value is
// kept when semantically equal to the API's (preserving the configured
// shape, e.g. an empty grant set); a nil API permissions section (create /
// PATCH responses omit it) keeps the prior value as well.
func flattenReportResource(ctx context.Context, rep *apiReport, m *reportResourceModel) diag.Diagnostics {
	var diags diag.Diagnostics
	m.ID = types.StringValue(rep.ID)
	m.Name = types.StringValue(rep.Name)
	m.Description = optionalString(rep.Description)
	m.BusinessUnitID = types.StringValue(rep.BusinessUnitID)
	m.OwnerUserID = optionalString(rep.OwnerUserID)
	m.CreatedAt = types.StringValue(rep.CreatedAt)
	m.CreatedByUserID = optionalString(rep.CreatedByUserID)
	m.UpdatedAt = types.StringValue(rep.UpdatedAt)
	m.UpdatedByUserID = optionalString(rep.UpdatedByUserID)
	if rep.Permissions != nil {
		perms, d := reportPermissionsToObject(rep.Permissions)
		diags.Append(d...)
		if !reportResourcePermissionsEqual(ctx, m.Permissions, perms) {
			m.Permissions = perms
		}
	}
	tags, d := tagsToList(rep.Tags)
	diags.Append(d...)
	m.Tags = tags
	return diags
}

func (r *reportResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan reportResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if plan.SourceReportID.IsNull() || plan.SourceReportID.IsUnknown() {
		// Normally caught at plan time by ModifyPlan.
		resp.Diagnostics.AddAttributeError(path.Root("source_report_id"), "Missing source_report_id",
			"Creating a hubspot_report requires source_report_id: the API can only clone an existing report.")
		return
	}
	source := plan.SourceReportID.ValueString()

	perms, d := reportPermissionsToWire(ctx, plan.Permissions)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}

	clonePath := reportPath(source) + "/clone"
	var cloned apiReport
	if err := r.client.Post(ctx, clonePath, map[string]any{
		"name":        plan.Name.ValueString(),
		"permissions": perms,
	}, &cloned); err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddAttributeError(path.Root("source_report_id"), "Source report not found",
				fmt.Sprintf("Report %s (source_report_id) does not exist or is archived, so it cannot be "+
					"cloned. Point source_report_id at an active report (find IDs with the hubspot_reports "+
					"data source or in the report's URL in HubSpot). Underlying error: %s",
					source, reportingErrorDetail(err)))
			return
		}
		resp.Diagnostics.AddError("Unable to clone HubSpot report",
			fmt.Sprintf("POST /%s failed: %s", clonePath, reportingErrorDetail(err)))
		return
	}

	// Persist the clone immediately (decision #9): the follow-up PATCH may
	// fail, and the report must not be orphaned.
	state := plan
	resp.Diagnostics.Append(flattenReportResource(ctx, &cloned, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	// Reconcile the metadata the clone cannot set (description, business
	// unit and owner are inherited / defaulted) — only what differs.
	body := map[string]any{}
	if !plan.Description.Equal(state.Description) {
		if plan.Description.IsNull() {
			body["description"] = nil
		} else {
			body["description"] = plan.Description.ValueString()
		}
	}
	if reportKnownString(plan.BusinessUnitID) && !plan.BusinessUnitID.Equal(state.BusinessUnitID) {
		body["businessUnitId"] = plan.BusinessUnitID.ValueString()
	}
	if reportKnownString(plan.OwnerUserID) && !plan.OwnerUserID.Equal(state.OwnerUserID) {
		body["ownerUserId"] = plan.OwnerUserID.ValueString()
	}
	if len(body) > 0 {
		p := reportPath(state.ID.ValueString())
		if err := r.client.Patch(ctx, p, body, nil); err != nil {
			resp.Diagnostics.AddError("Unable to set HubSpot report metadata after clone",
				fmt.Sprintf("Report %s was cloned from %s and saved to state, but PATCH /%s failed: %s. "+
					"Re-run the apply to retry.", state.ID.ValueString(), source, p, reportingErrorDetail(err)))
			return
		}
	}

	r.readInto(ctx, state.ID.ValueString(), &state, &resp.Diagnostics, "after create")
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

// readInto GETs the report (with permissions and tags) and flattens it into
// m; used after writes, where the object must exist.
func (r *reportResource) readInto(ctx context.Context, id string, m *reportResourceModel, diags *diag.Diagnostics, when string) {
	rep, _, err := getReport(ctx, r.client, id, false)
	if err != nil {
		diags.AddError("Unable to read HubSpot report "+when,
			fmt.Sprintf("GET /%s failed: %s", reportPath(id), reportingErrorDetail(err)))
		return
	}
	diags.Append(flattenReportResource(ctx, rep, m)...)
}

func reportKnownString(s types.String) bool { return !s.IsNull() && !s.IsUnknown() }

func (r *reportResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state reportResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()
	rep, _, err := getReport(ctx, r.client, id, false)
	if err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Unable to read HubSpot report",
			fmt.Sprintf("GET /%s failed: %s", reportPath(id), reportingErrorDetail(err)))
		return
	}
	if rep.Archived {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(flattenReportResource(ctx, rep, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *reportResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state reportResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.ID.ValueString()

	// One PATCH with only the changed metadata (never combined with
	// `archived`, which the API rejects).
	body := map[string]any{}
	if !plan.Name.Equal(state.Name) {
		body["name"] = plan.Name.ValueString()
	}
	if !plan.Description.Equal(state.Description) {
		if plan.Description.IsNull() {
			body["description"] = nil
		} else {
			body["description"] = plan.Description.ValueString()
		}
	}
	if reportKnownString(plan.BusinessUnitID) && !plan.BusinessUnitID.Equal(state.BusinessUnitID) {
		body["businessUnitId"] = plan.BusinessUnitID.ValueString()
	}
	if reportKnownString(plan.OwnerUserID) && !plan.OwnerUserID.Equal(state.OwnerUserID) {
		body["ownerUserId"] = plan.OwnerUserID.ValueString()
	}
	if !reportResourcePermissionsEqual(ctx, plan.Permissions, state.Permissions) {
		perms, d := reportPermissionsToWire(ctx, plan.Permissions)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
		body["permissions"] = perms
	}
	if len(body) > 0 {
		p := reportPath(id)
		if err := r.client.Patch(ctx, p, body, nil); err != nil {
			resp.Diagnostics.AddError("Unable to update HubSpot report",
				fmt.Sprintf("PATCH /%s failed: %s", p, reportingErrorDetail(err)))
			return
		}
	}

	// source_report_id is never sent (create-time only); carry the planned
	// value, e.g. one added to an imported report.
	next := plan
	r.readInto(ctx, id, &next, &resp.Diagnostics, "after update")
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &next)...)
}

func (r *reportResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state reportResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := archiveReportingObject(ctx, r.client, reportPath(state.ID.ValueString())); err != nil {
		resp.Diagnostics.AddError("Unable to archive HubSpot report", reportingErrorDetail(err))
	}
}

func (r *reportResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !reportResourceIDPattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("hubspot_report is imported by its numeric report ID (e.g. `12345678`, the number in "+
				"the report's URL in HubSpot); got %q.", req.ID))
		return
	}
	// source_report_id stays null: imported reports were not cloned by
	// Terraform, so adding it later never forces a replacement.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
