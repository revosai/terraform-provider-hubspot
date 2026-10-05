// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

// Shared plumbing for the Analytics Reporting API (public beta) surface:
// hubspot_dashboard, hubspot_report and their data sources. See
// dev-docs/design/resource-model.md ("Reporting") for the design.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	rschema "github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

const (
	// reportingBasePath is the Analytics Reporting API (public beta) root.
	reportingBasePath  = "analytics/reporting/2027-03-beta"
	dashboardsBasePath = reportingBasePath + "/dashboards"
	reportsBasePath    = reportingBasePath + "/reports"

	// dashboardProperties / reportProperties are the optional response
	// sections requested on every read: HubSpot omits them unless asked.
	dashboardProperties = "permissions,tags,widgets"
	reportProperties    = "permissions,tags"

	// reportingSearchPageSize is the API's maximum page size.
	reportingSearchPageSize = 100

	// reportingBetaMarkdown is the shared beta notice for schema descriptions.
	reportingBetaMarkdown = "**The Analytics Reporting API is a public beta** (`/analytics/reporting/2027-03-beta`), " +
		"so this is beta-backed per the provider's " +
		"[API stability policy](https://github.com/revosai/terraform-provider-hubspot/blob/main/ROADMAP.md#api-stability-policy). " +
		"The portal must opt into the beta (via the associated product update in HubSpot) and the token needs " +
		"the `reporting.full.read` scope (plus `reporting.full.write` to create, clone or archive)."
)

// Permission-type enums shared by dashboards and reports.
var (
	reportingPermissionTypes = []string{"PRIVATE", "EVERYONE_VIEW", "EVERYONE_EDIT", "SPECIFIC"}
	reportingGrantTypes      = []string{"USER", "TEAM"}
)

// ---------------------------------------------------------------------------
// Wire types (mirror PublicDashboard / PublicReport in the OpenAPI spec).
// ---------------------------------------------------------------------------

type apiPermissionGrant struct {
	GrantType string `json:"grantType"`
	GranteeID string `json:"granteeId"`
}

type apiSpecificPermission struct {
	PermissionType string               `json:"permissionType"` // VIEW | EDIT
	Grants         []apiPermissionGrant `json:"grants,omitempty"`
}

// apiDashboardPermissions: specificPermissions is an ARRAY on dashboards.
type apiDashboardPermissions struct {
	PermissionType      string                  `json:"permissionType"`
	SpecificPermissions []apiSpecificPermission `json:"specificPermissions,omitempty"`
}

// apiReportPermissions: specificPermissions is a SINGLE object on reports.
type apiReportPermissions struct {
	PermissionType      string                 `json:"permissionType"`
	SpecificPermissions *apiSpecificPermission `json:"specificPermissions,omitempty"`
}

type apiTag struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type apiWidgetLayout struct {
	X      int64 `json:"x"`
	Y      int64 `json:"y"`
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}

type apiWidget struct {
	ReportID     string          `json:"reportId"`
	WidgetLayout apiWidgetLayout `json:"widgetLayout"`
}

type apiDashboard struct {
	ID                 string                   `json:"id"`
	Name               string                   `json:"name"`
	Description        *string                  `json:"description,omitempty"`
	BusinessUnitID     string                   `json:"businessUnitId"`
	OwnerUserID        *string                  `json:"ownerUserId,omitempty"`
	Archived           bool                     `json:"archived"`
	ArchivedAt         *string                  `json:"archivedAt,omitempty"`
	CreatedAt          string                   `json:"createdAt"`
	CreatedByUserID    *string                  `json:"createdByUserId,omitempty"`
	UpdatedAt          string                   `json:"updatedAt"`
	UpdatedByUserID    *string                  `json:"updatedByUserId,omitempty"`
	LastViewedAt       *string                  `json:"lastViewedAt,omitempty"`
	LastViewedByUserID *string                  `json:"lastViewedByUserId,omitempty"`
	Permissions        *apiDashboardPermissions `json:"permissions,omitempty"`
	Tags               []apiTag                 `json:"tags,omitempty"`
	Widgets            []apiWidget              `json:"widgets,omitempty"`
}

type apiReport struct {
	ID                 string                `json:"id"`
	Name               string                `json:"name"`
	Description        *string               `json:"description,omitempty"`
	BusinessUnitID     string                `json:"businessUnitId"`
	OwnerUserID        *string               `json:"ownerUserId,omitempty"`
	Archived           bool                  `json:"archived"`
	ArchivedAt         *string               `json:"archivedAt,omitempty"`
	CreatedAt          string                `json:"createdAt"`
	CreatedByUserID    *string               `json:"createdByUserId,omitempty"`
	UpdatedAt          string                `json:"updatedAt"`
	UpdatedByUserID    *string               `json:"updatedByUserId,omitempty"`
	LastViewedAt       *string               `json:"lastViewedAt,omitempty"`
	LastViewedByUserID *string               `json:"lastViewedByUserId,omitempty"`
	Permissions        *apiReportPermissions `json:"permissions,omitempty"`
	Tags               []apiTag              `json:"tags,omitempty"`
}

// reportingCollectionPage is one page of a dashboards/reports search.
type reportingCollectionPage struct {
	Total   int64             `json:"total"`
	Results []json.RawMessage `json:"results"`
	Paging  *struct {
		Next *struct {
			After string `json:"after"`
		} `json:"next,omitempty"`
	} `json:"paging,omitempty"`
}

// ---------------------------------------------------------------------------
// Terraform models.
// ---------------------------------------------------------------------------

// reportingPermissionsModel is the `permissions` single nested attribute.
type reportingPermissionsModel struct {
	Type types.String `tfsdk:"type"`
	View types.Set    `tfsdk:"view"`
	Edit types.Set    `tfsdk:"edit"`
}

type reportingGrantModel struct {
	Type types.String `tfsdk:"type"`
	ID   types.String `tfsdk:"id"`
}

var reportingGrantAttrTypes = map[string]attr.Type{
	"type": types.StringType,
	"id":   types.StringType,
}

var reportingGrantObjectType = types.ObjectType{AttrTypes: reportingGrantAttrTypes}

var reportingPermissionsAttrTypes = map[string]attr.Type{
	"type": types.StringType,
	"view": types.SetType{ElemType: reportingGrantObjectType},
	"edit": types.SetType{ElemType: reportingGrantObjectType},
}

var reportingTagAttrTypes = map[string]attr.Type{
	"id":   types.StringType,
	"name": types.StringType,
}

var reportingTagObjectType = types.ObjectType{AttrTypes: reportingTagAttrTypes}

var reportingWidgetAttrTypes = map[string]attr.Type{
	"report_id": types.StringType,
	"x":         types.Int64Type,
	"y":         types.Int64Type,
	"width":     types.Int64Type,
	"height":    types.Int64Type,
}

var reportingWidgetObjectType = types.ObjectType{AttrTypes: reportingWidgetAttrTypes}

// ---------------------------------------------------------------------------
// Schema builders.
// ---------------------------------------------------------------------------

const permissionsMarkdown = "Who can access the %s. `type` is one of `PRIVATE`, `EVERYONE_VIEW`, " +
	"`EVERYONE_EDIT` or `SPECIFIC`; with `SPECIFIC`, list the users/teams in `view` and/or `edit`%s. " +
	"Grants are sets (order-insensitive) and are only allowed with `SPECIFIC`."

func resourceGrantSetAttribute(level, object string) rschema.SetNestedAttribute {
	return rschema.SetNestedAttribute{
		Optional: true,
		MarkdownDescription: fmt.Sprintf("Users/teams granted %s access to the %s. Only valid when "+
			"`type = \"SPECIFIC\"`.", level, object),
		NestedObject: rschema.NestedAttributeObject{
			Attributes: map[string]rschema.Attribute{
				"type": rschema.StringAttribute{
					Required:            true,
					MarkdownDescription: "Grantee kind: `USER` or `TEAM`.",
					Validators:          []validator.String{stringvalidator.OneOf(reportingGrantTypes...)},
				},
				"id": rschema.StringAttribute{
					Required:            true,
					MarkdownDescription: "HubSpot user ID or team ID of the grantee.",
				},
			},
		},
	}
}

// resourcePermissionsAttribute is the required `permissions` block for the
// dashboard and report resources. object is "dashboard" or "report".
func resourcePermissionsAttribute(object string) rschema.SingleNestedAttribute {
	extra := ""
	if object == "report" {
		extra = " (reports accept only one of `view` or `edit`)"
	}
	return rschema.SingleNestedAttribute{
		Required:            true,
		MarkdownDescription: fmt.Sprintf(permissionsMarkdown, object, extra),
		Attributes: map[string]rschema.Attribute{
			"type": rschema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Permission type: `PRIVATE`, `EVERYONE_VIEW`, `EVERYONE_EDIT` or `SPECIFIC`.",
				Validators:          []validator.String{stringvalidator.OneOf(reportingPermissionTypes...)},
			},
			"view": resourceGrantSetAttribute("view", object),
			"edit": resourceGrantSetAttribute("edit", object),
		},
	}
}

func dataSourceGrantSetAttribute(level, object string) dschema.SetNestedAttribute {
	return dschema.SetNestedAttribute{
		Computed:            true,
		MarkdownDescription: fmt.Sprintf("Users/teams granted %s access to the %s (`SPECIFIC` only).", level, object),
		NestedObject: dschema.NestedAttributeObject{
			Attributes: map[string]dschema.Attribute{
				"type": dschema.StringAttribute{Computed: true, MarkdownDescription: "Grantee kind: `USER` or `TEAM`."},
				"id":   dschema.StringAttribute{Computed: true, MarkdownDescription: "HubSpot user ID or team ID."},
			},
		},
	}
}

// dataSourcePermissionsAttribute is the computed `permissions` block.
func dataSourcePermissionsAttribute(object string) dschema.SingleNestedAttribute {
	return dschema.SingleNestedAttribute{
		Computed:            true,
		MarkdownDescription: fmt.Sprintf(permissionsMarkdown, object, ""),
		Attributes: map[string]dschema.Attribute{
			"type": dschema.StringAttribute{Computed: true, MarkdownDescription: "Permission type."},
			"view": dataSourceGrantSetAttribute("view", object),
			"edit": dataSourceGrantSetAttribute("edit", object),
		},
	}
}

var tagsMarkdown = "Tags on the %s (read-only: the API cannot set tags)."

func resourceTagsAttribute(object string) rschema.ListNestedAttribute {
	return rschema.ListNestedAttribute{
		Computed:            true,
		MarkdownDescription: fmt.Sprintf(tagsMarkdown, object),
		NestedObject: rschema.NestedAttributeObject{
			Attributes: map[string]rschema.Attribute{
				"id":   rschema.StringAttribute{Computed: true, MarkdownDescription: "Tag ID."},
				"name": rschema.StringAttribute{Computed: true, MarkdownDescription: "Tag name."},
			},
		},
	}
}

func dataSourceTagsAttribute(object string) dschema.ListNestedAttribute {
	return dschema.ListNestedAttribute{
		Computed:            true,
		MarkdownDescription: fmt.Sprintf(tagsMarkdown, object),
		NestedObject: dschema.NestedAttributeObject{
			Attributes: map[string]dschema.Attribute{
				"id":   dschema.StringAttribute{Computed: true, MarkdownDescription: "Tag ID."},
				"name": dschema.StringAttribute{Computed: true, MarkdownDescription: "Tag name."},
			},
		},
	}
}

const widgetsMarkdown = "Widgets on the dashboard with their grid layout (read-only: the API cannot position widgets)."

func widgetAttributeDescriptions() map[string]string {
	return map[string]string{
		"report_id": "ID of the report the widget shows.",
		"x":         "0-based horizontal grid position.",
		"y":         "0-based vertical grid position.",
		"width":     "Widget width in grid units.",
		"height":    "Widget height in grid units.",
	}
}

func resourceWidgetsAttribute() rschema.ListNestedAttribute {
	d := widgetAttributeDescriptions()
	return rschema.ListNestedAttribute{
		Computed:            true,
		MarkdownDescription: widgetsMarkdown,
		NestedObject: rschema.NestedAttributeObject{
			Attributes: map[string]rschema.Attribute{
				"report_id": rschema.StringAttribute{Computed: true, MarkdownDescription: d["report_id"]},
				"x":         rschema.Int64Attribute{Computed: true, MarkdownDescription: d["x"]},
				"y":         rschema.Int64Attribute{Computed: true, MarkdownDescription: d["y"]},
				"width":     rschema.Int64Attribute{Computed: true, MarkdownDescription: d["width"]},
				"height":    rschema.Int64Attribute{Computed: true, MarkdownDescription: d["height"]},
			},
		},
	}
}

func dataSourceWidgetsAttribute() dschema.ListNestedAttribute {
	d := widgetAttributeDescriptions()
	return dschema.ListNestedAttribute{
		Computed:            true,
		MarkdownDescription: widgetsMarkdown,
		NestedObject: dschema.NestedAttributeObject{
			Attributes: map[string]dschema.Attribute{
				"report_id": dschema.StringAttribute{Computed: true, MarkdownDescription: d["report_id"]},
				"x":         dschema.Int64Attribute{Computed: true, MarkdownDescription: d["x"]},
				"y":         dschema.Int64Attribute{Computed: true, MarkdownDescription: d["y"]},
				"width":     dschema.Int64Attribute{Computed: true, MarkdownDescription: d["width"]},
				"height":    dschema.Int64Attribute{Computed: true, MarkdownDescription: d["height"]},
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Permissions expand / validate / flatten.
// ---------------------------------------------------------------------------

// permissionsFromObject decodes the `permissions` object attribute.
func permissionsFromObject(ctx context.Context, obj types.Object) (reportingPermissionsModel, diag.Diagnostics) {
	var m reportingPermissionsModel
	if obj.IsNull() || obj.IsUnknown() {
		return m, nil
	}
	diags := obj.As(ctx, &m, basetypes.ObjectAsOptions{})
	return m, diags
}

func grantsFromSet(ctx context.Context, s types.Set) ([]apiPermissionGrant, diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	var grants []reportingGrantModel
	diags := s.ElementsAs(ctx, &grants, false)
	out := make([]apiPermissionGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, apiPermissionGrant{GrantType: g.Type.ValueString(), GranteeID: g.ID.ValueString()})
	}
	sortGrants(out)
	return out, diags
}

func sortGrants(g []apiPermissionGrant) {
	sort.Slice(g, func(i, j int) bool {
		if g[i].GrantType != g[j].GrantType {
			return g[i].GrantType < g[j].GrantType
		}
		return g[i].GranteeID < g[j].GranteeID
	})
}

// validatePermissions enforces the plan-time rules: SPECIFIC needs at least
// one grant, other types forbid grants, and reports (forReport) accept only
// one of view/edit. Unknown values are skipped (validated at apply).
func validatePermissions(ctx context.Context, obj types.Object, attrPath path.Path, forReport bool) diag.Diagnostics {
	var diags diag.Diagnostics
	m, d := permissionsFromObject(ctx, obj)
	diags.Append(d...)
	if diags.HasError() || obj.IsNull() || obj.IsUnknown() || m.Type.IsUnknown() || m.View.IsUnknown() || m.Edit.IsUnknown() {
		return diags
	}
	nView, nEdit := len(m.View.Elements()), len(m.Edit.Elements())
	if m.Type.ValueString() == "SPECIFIC" {
		if nView == 0 && nEdit == 0 {
			diags.AddAttributeError(attrPath, "Missing permission grants",
				"permissions.type = \"SPECIFIC\" requires at least one grant in `view` or `edit`.")
		}
		if forReport && nView > 0 && nEdit > 0 {
			diags.AddAttributeError(attrPath, "Too many permission levels for a report",
				"HubSpot reports carry a single specific-permission level: set either `view` or `edit`, not both.")
		}
		return diags
	}
	if nView > 0 || nEdit > 0 {
		diags.AddAttributeError(attrPath, "Grants require SPECIFIC permissions",
			fmt.Sprintf("permissions.view / permissions.edit are only valid with type = \"SPECIFIC\" (got %q).",
				m.Type.ValueString()))
	}
	return diags
}

// dashboardPermissionsToWire expands the permissions object for dashboards.
func dashboardPermissionsToWire(ctx context.Context, obj types.Object) (*apiDashboardPermissions, diag.Diagnostics) {
	m, diags := permissionsFromObject(ctx, obj)
	if diags.HasError() {
		return nil, diags
	}
	out := &apiDashboardPermissions{PermissionType: m.Type.ValueString()}
	if out.PermissionType != "SPECIFIC" {
		return out, diags
	}
	view, d := grantsFromSet(ctx, m.View)
	diags.Append(d...)
	edit, d := grantsFromSet(ctx, m.Edit)
	diags.Append(d...)
	if len(view) > 0 {
		out.SpecificPermissions = append(out.SpecificPermissions, apiSpecificPermission{PermissionType: "VIEW", Grants: view})
	}
	if len(edit) > 0 {
		out.SpecificPermissions = append(out.SpecificPermissions, apiSpecificPermission{PermissionType: "EDIT", Grants: edit})
	}
	return out, diags
}

// reportPermissionsToWire expands the permissions object for reports.
func reportPermissionsToWire(ctx context.Context, obj types.Object) (*apiReportPermissions, diag.Diagnostics) {
	m, diags := permissionsFromObject(ctx, obj)
	if diags.HasError() {
		return nil, diags
	}
	out := &apiReportPermissions{PermissionType: m.Type.ValueString()}
	if out.PermissionType != "SPECIFIC" {
		return out, diags
	}
	view, d := grantsFromSet(ctx, m.View)
	diags.Append(d...)
	edit, d := grantsFromSet(ctx, m.Edit)
	diags.Append(d...)
	switch {
	case len(edit) > 0:
		out.SpecificPermissions = &apiSpecificPermission{PermissionType: "EDIT", Grants: edit}
	case len(view) > 0:
		out.SpecificPermissions = &apiSpecificPermission{PermissionType: "VIEW", Grants: view}
	}
	return out, diags
}

// grantsToSet flattens grants into a set; empty => null (matching an omitted
// config attribute).
func grantsToSet(grants []apiPermissionGrant) (types.Set, diag.Diagnostics) {
	if len(grants) == 0 {
		return types.SetNull(reportingGrantObjectType), nil
	}
	var diags diag.Diagnostics
	elems := make([]attr.Value, 0, len(grants))
	for _, g := range grants {
		o, d := types.ObjectValue(reportingGrantAttrTypes, map[string]attr.Value{
			"type": types.StringValue(g.GrantType),
			"id":   types.StringValue(g.GranteeID),
		})
		diags.Append(d...)
		elems = append(elems, o)
	}
	s, d := types.SetValue(reportingGrantObjectType, elems)
	diags.Append(d...)
	return s, diags
}

func permissionsObject(permType string, view, edit []apiPermissionGrant) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	viewSet, d := grantsToSet(view)
	diags.Append(d...)
	editSet, d := grantsToSet(edit)
	diags.Append(d...)
	o, d := types.ObjectValue(reportingPermissionsAttrTypes, map[string]attr.Value{
		"type": types.StringValue(permType),
		"view": viewSet,
		"edit": editSet,
	})
	diags.Append(d...)
	return o, diags
}

// dashboardPermissionsToObject flattens dashboard permissions (nil => null).
func dashboardPermissionsToObject(p *apiDashboardPermissions) (types.Object, diag.Diagnostics) {
	if p == nil {
		return types.ObjectNull(reportingPermissionsAttrTypes), nil
	}
	var view, edit []apiPermissionGrant
	for _, sp := range p.SpecificPermissions {
		switch sp.PermissionType {
		case "VIEW":
			view = append(view, sp.Grants...)
		case "EDIT":
			edit = append(edit, sp.Grants...)
		}
	}
	return permissionsObject(p.PermissionType, view, edit)
}

// reportPermissionsToObject flattens report permissions (nil => null).
func reportPermissionsToObject(p *apiReportPermissions) (types.Object, diag.Diagnostics) {
	if p == nil {
		return types.ObjectNull(reportingPermissionsAttrTypes), nil
	}
	var view, edit []apiPermissionGrant
	if sp := p.SpecificPermissions; sp != nil {
		switch sp.PermissionType {
		case "VIEW":
			view = sp.Grants
		case "EDIT":
			edit = sp.Grants
		}
	}
	return permissionsObject(p.PermissionType, view, edit)
}

// ---------------------------------------------------------------------------
// Tags / widgets flatten.
// ---------------------------------------------------------------------------

// tagsToList flattens tags; always a known (possibly empty) list.
func tagsToList(tags []apiTag) (types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	elems := make([]attr.Value, 0, len(tags))
	for _, t := range tags {
		o, d := types.ObjectValue(reportingTagAttrTypes, map[string]attr.Value{
			"id":   types.StringValue(t.ID),
			"name": types.StringValue(t.Name),
		})
		diags.Append(d...)
		elems = append(elems, o)
	}
	l, d := types.ListValue(reportingTagObjectType, elems)
	diags.Append(d...)
	return l, diags
}

// widgetsToList flattens widgets in a stable order (y, x, report_id) so a
// layout-preserving refresh never shows a reorder diff.
func widgetsToList(widgets []apiWidget) (types.List, diag.Diagnostics) {
	ws := append([]apiWidget(nil), widgets...)
	sort.SliceStable(ws, func(i, j int) bool {
		a, b := ws[i].WidgetLayout, ws[j].WidgetLayout
		if a.Y != b.Y {
			return a.Y < b.Y
		}
		if a.X != b.X {
			return a.X < b.X
		}
		return ws[i].ReportID < ws[j].ReportID
	})
	var diags diag.Diagnostics
	elems := make([]attr.Value, 0, len(ws))
	for _, w := range ws {
		o, d := types.ObjectValue(reportingWidgetAttrTypes, map[string]attr.Value{
			"report_id": types.StringValue(w.ReportID),
			"x":         types.Int64Value(w.WidgetLayout.X),
			"y":         types.Int64Value(w.WidgetLayout.Y),
			"width":     types.Int64Value(w.WidgetLayout.Width),
			"height":    types.Int64Value(w.WidgetLayout.Height),
		})
		diags.Append(d...)
		elems = append(elems, o)
	}
	l, d := types.ListValue(reportingWidgetObjectType, elems)
	diags.Append(d...)
	return l, diags
}

// widgetReportIDs returns the distinct report IDs on a dashboard, sorted.
func widgetReportIDs(widgets []apiWidget) []string {
	seen := map[string]bool{}
	var ids []string
	for _, w := range widgets {
		if !seen[w.ReportID] {
			seen[w.ReportID] = true
			ids = append(ids, w.ReportID)
		}
	}
	sort.Strings(ids)
	return ids
}

// optionalString flattens an optional API string (nil or "" => null).
func optionalString(s *string) types.String {
	if s == nil || *s == "" {
		return types.StringNull()
	}
	return types.StringValue(*s)
}

// ---------------------------------------------------------------------------
// Snapshot JSON (raw_json on data sources).
// ---------------------------------------------------------------------------

// reportingVolatileKeys are view-tracking fields that change whenever anyone
// opens the object; stripped from raw_json so committed snapshots only diff
// on real configuration changes.
var reportingVolatileKeys = []string{"lastViewedAt", "lastViewedByUserId"}

// canonicalSnapshotJSON renders an API object for raw_json: volatile keys
// stripped (top level and inside nested widget reports), keys sorted, 2-space
// indent, trailing newline — stable, diff-friendly file content.
func canonicalSnapshotJSON(raw []byte) (string, error) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return "", fmt.Errorf("decoding API object: %w", err)
	}
	stripVolatile(v)
	// encoding/json sorts map keys, which makes the output canonical.
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "", err
	}
	return string(out) + "\n", nil
}

func stripVolatile(v any) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range reportingVolatileKeys {
			delete(t, k)
		}
		for _, child := range t {
			stripVolatile(child)
		}
	case []any:
		for _, child := range t {
			stripVolatile(child)
		}
	}
}

// ---------------------------------------------------------------------------
// Errors.
// ---------------------------------------------------------------------------

// reportingErrorDetail renders err for a diagnostic, appending actionable
// guidance for the beta's most common failure: a 403 because the portal has
// not opted into the Reporting API beta or the token lacks the scope.
func reportingErrorDetail(err error) string {
	msg := err.Error()
	var apiErr *client.APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusForbidden {
		msg += "\n\nA 403 from the Reporting API usually means one of: (1) the portal has not opted into the " +
			"Analytics Reporting API public beta (enable the associated product update in HubSpot); (2) the " +
			"token lacks the `reporting.full.read` scope (reads) or `reporting.full.write` scope (create, " +
			"clone, archive) — `reporting.full.edit` only allows metadata updates; (3) the token's user cannot " +
			"access this object under its current permissions."
	}
	return msg
}

// ---------------------------------------------------------------------------
// API helpers.
// ---------------------------------------------------------------------------

func dashboardPath(id string) string { return dashboardsBasePath + "/" + url.PathEscape(id) }
func reportPath(id string) string    { return reportsBasePath + "/" + url.PathEscape(id) }

// getDashboard fetches one dashboard with permissions, tags and widgets.
// archived=true fetches an archived dashboard instead of an active one. The
// raw response is returned for raw_json snapshots.
func getDashboard(ctx context.Context, c *client.Client, id string, archived bool) (*apiDashboard, json.RawMessage, error) {
	q := url.Values{"properties": {dashboardProperties}}
	if archived {
		q.Set("archived", "true")
	}
	var raw json.RawMessage
	if err := c.Get(ctx, dashboardPath(id), q, &raw); err != nil {
		return nil, nil, err
	}
	var d apiDashboard
	if err := json.Unmarshal(raw, &d); err != nil {
		return nil, nil, fmt.Errorf("decoding dashboard %s: %w", id, err)
	}
	return &d, raw, nil
}

// getReport fetches one report with permissions and tags.
func getReport(ctx context.Context, c *client.Client, id string, archived bool) (*apiReport, json.RawMessage, error) {
	q := url.Values{"properties": {reportProperties}}
	if archived {
		q.Set("archived", "true")
	}
	var raw json.RawMessage
	if err := c.Get(ctx, reportPath(id), q, &raw); err != nil {
		return nil, nil, err
	}
	var r apiReport
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, nil, fmt.Errorf("decoding report %s: %w", id, err)
	}
	return &r, raw, nil
}

// searchReporting pages through GET {collectionPath} (dashboards or reports)
// with the given filters, returning every raw result object.
func searchReporting(ctx context.Context, c *client.Client, collectionPath string, filters url.Values) ([]json.RawMessage, error) {
	var all []json.RawMessage
	after := ""
	for {
		q := url.Values{}
		for k, v := range filters {
			q[k] = append([]string(nil), v...)
		}
		q.Set("limit", fmt.Sprint(reportingSearchPageSize))
		if after != "" {
			q.Set("after", after)
		}
		var page reportingCollectionPage
		if err := c.Get(ctx, collectionPath, q, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if page.Paging == nil || page.Paging.Next == nil || page.Paging.Next.After == "" {
			return all, nil
		}
		after = page.Paging.Next.After
	}
}

// archiveReportingObject archives a dashboard or report (objectPath is
// dashboardPath(id) or reportPath(id)) and confirms the archive with a read of
// the archived copy. A 404 on the archive call means it is already gone.
// PATCH {archived: true} cannot be combined with other fields.
func archiveReportingObject(ctx context.Context, c *client.Client, objectPath string) error {
	if err := c.Patch(ctx, objectPath, map[string]any{"archived": true}, nil); err != nil {
		if client.IsNotFound(err) {
			return nil
		}
		return fmt.Errorf("PATCH /%s {archived: true} failed: %w", objectPath, err)
	}
	var confirm struct {
		Archived bool `json:"archived"`
	}
	if err := c.Get(ctx, objectPath, url.Values{"archived": {"true"}}, &confirm); err != nil {
		if client.IsNotFound(err) {
			return nil // purged outright — gone either way
		}
		return fmt.Errorf("confirming archive (GET /%s?archived=true) failed: %w", objectPath, err)
	}
	if !confirm.Archived {
		return fmt.Errorf("HubSpot accepted the archive request for /%s but the object still reads as active", objectPath)
	}
	return nil
}
