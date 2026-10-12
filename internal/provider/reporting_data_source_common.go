// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

// Helpers shared by the hubspot_dashboard / hubspot_dashboards /
// hubspot_report / hubspot_reports data sources: one attribute map and one
// flatten per object kind, used both for the singular data source's root
// attributes and for each item of the list data sources, so the two shapes
// can never drift apart.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

const (
	// reportingSnapshotMarkdown documents raw_json on every reporting data source.
	reportingSnapshotMarkdown = "`raw_json` holds the API object canonicalized — sorted keys, 2-space indent, " +
		"trailing newline — with the volatile view-tracking fields (`lastViewedAt`, `lastViewedByUserId`) " +
		"stripped. It is meant for **committed snapshots** (write it with `local_file` and check it into git): " +
		"a snapshot only diffs on real changes, and fields HubSpot adds to the API later are captured without " +
		"a provider release. The typed `last_viewed_at` / `last_viewed_by_user_id` attributes still expose the " +
		"view-tracking values."

	// reportingGapsMarkdown documents what the Reporting API cannot provide.
	reportingGapsMarkdown = "**Report configuration (query, data sources, filters, visualization) is not exposed " +
		"by the HubSpot API at all** — not even readable — so it cannot be captured here; only metadata, " +
		"permissions, tags and dashboard layout are. Exporting report data (the API's emailed CSV/PDF export) " +
		"is intentionally not offered: it is a side-effecting action on CRM record data, outside this " +
		"provider's configuration-plane scope."
)

// reportingObjectKind distinguishes dashboards from reports in the shared
// schema/flatten helpers.
type reportingObjectKind struct {
	singular   string // "dashboard" | "report"
	plural     string // "dashboards" | "reports"
	collection string // dashboardsBasePath | reportsBasePath
	widgets    bool   // dashboards carry widgets / report_ids
}

var (
	dashboardKind = reportingObjectKind{singular: "dashboard", plural: "dashboards", collection: dashboardsBasePath, widgets: true}
	reportKind    = reportingObjectKind{singular: "report", plural: "reports", collection: reportsBasePath}
)

// reportingItemAttributes returns the computed attributes describing one
// dashboard or report. widgetsNote is appended to the widgets / report_ids
// descriptions (dashboards only).
func reportingItemAttributes(k reportingObjectKind, widgetsNote string) map[string]dschema.Attribute {
	s := k.singular
	str := func(desc string) dschema.StringAttribute {
		return dschema.StringAttribute{Computed: true, MarkdownDescription: desc}
	}
	attrs := map[string]dschema.Attribute{
		"id":               str(fmt.Sprintf("The %s's ID.", s)),
		"name":             str(fmt.Sprintf("The %s's name.", s)),
		"description":      str(fmt.Sprintf("The %s's description (null when unset).", s)),
		"business_unit_id": str(fmt.Sprintf("Business unit (brand) the %s belongs to; `0` is the default business unit.", s)),
		"owner_user_id":    str(fmt.Sprintf("HubSpot user ID of the %s's owner.", s)),
		"archived": dschema.BoolAttribute{
			Computed:            true,
			MarkdownDescription: fmt.Sprintf("Whether the %s is archived.", s),
		},
		"archived_at":        str(fmt.Sprintf("When the %s was archived (ISO 8601); null for an active %s.", s, s)),
		"created_at":         str(fmt.Sprintf("When the %s was created (ISO 8601).", s)),
		"created_by_user_id": str(fmt.Sprintf("HubSpot user ID of the %s's creator.", s)),
		"updated_at":         str(fmt.Sprintf("When the %s was last updated (ISO 8601).", s)),
		"updated_by_user_id": str(fmt.Sprintf("HubSpot user ID of the user who last updated the %s.", s)),
		"last_viewed_at": str(fmt.Sprintf("When the %s was last viewed (ISO 8601). Volatile — it changes whenever "+
			"anyone opens the %s — so it is excluded from `raw_json`.", s, s)),
		"last_viewed_by_user_id": str("HubSpot user ID of the last viewer. Volatile; excluded from `raw_json`."),
		"permissions":            dataSourcePermissionsAttribute(s),
		"tags":                   dataSourceTagsAttribute(s),
		"raw_json": str(fmt.Sprintf("The full API %s object as canonical JSON for committed snapshots: sorted keys, "+
			"2-space indent, trailing newline, view-tracking fields (`lastViewedAt`, `lastViewedByUserId`) stripped.", s)),
	}
	if k.widgets {
		w := dataSourceWidgetsAttribute()
		w.MarkdownDescription += widgetsNote
		attrs["widgets"] = w
		attrs["report_ids"] = dschema.ListAttribute{
			Computed:            true,
			ElementType:         types.StringType,
			MarkdownDescription: "Distinct IDs of the reports shown on the dashboard's widgets, sorted." + widgetsNote,
		}
	}
	return attrs
}

// reportingItemAttrTypes is the object type matching reportingItemAttributes.
func reportingItemAttrTypes(k reportingObjectKind) map[string]attr.Type {
	t := map[string]attr.Type{
		"id":                     types.StringType,
		"name":                   types.StringType,
		"description":            types.StringType,
		"business_unit_id":       types.StringType,
		"owner_user_id":          types.StringType,
		"archived":               types.BoolType,
		"archived_at":            types.StringType,
		"created_at":             types.StringType,
		"created_by_user_id":     types.StringType,
		"updated_at":             types.StringType,
		"updated_by_user_id":     types.StringType,
		"last_viewed_at":         types.StringType,
		"last_viewed_by_user_id": types.StringType,
		"permissions":            types.ObjectType{AttrTypes: reportingPermissionsAttrTypes},
		"tags":                   types.ListType{ElemType: reportingTagObjectType},
		"raw_json":               types.StringType,
	}
	if k.widgets {
		t["widgets"] = types.ListType{ElemType: reportingWidgetObjectType}
		t["report_ids"] = types.ListType{ElemType: types.StringType}
	}
	return t
}

// reportingMetadataWire is the metadata shared by PublicDashboard and
// PublicReport (same JSON field names).
type reportingMetadataWire struct {
	ID                 string  `json:"id"`
	Name               string  `json:"name"`
	Description        *string `json:"description,omitempty"`
	BusinessUnitID     *string `json:"businessUnitId,omitempty"`
	OwnerUserID        *string `json:"ownerUserId,omitempty"`
	Archived           bool    `json:"archived"`
	ArchivedAt         *string `json:"archivedAt,omitempty"`
	CreatedAt          *string `json:"createdAt,omitempty"`
	CreatedByUserID    *string `json:"createdByUserId,omitempty"`
	UpdatedAt          *string `json:"updatedAt,omitempty"`
	UpdatedByUserID    *string `json:"updatedByUserId,omitempty"`
	LastViewedAt       *string `json:"lastViewedAt,omitempty"`
	LastViewedByUserID *string `json:"lastViewedByUserId,omitempty"`
}

// reportingItemObject flattens one raw API dashboard/report into the object
// value described by reportingItemAttributes. withWidgets=false (dashboards
// listed without include_widgets) leaves widgets/report_ids null because the
// response does not carry them.
func reportingItemObject(ctx context.Context, k reportingObjectKind, raw json.RawMessage, withWidgets bool) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	null := types.ObjectNull(reportingItemAttrTypes(k))

	var meta reportingMetadataWire
	if err := json.Unmarshal(raw, &meta); err != nil {
		diags.AddError("Unable to decode HubSpot "+k.singular, err.Error())
		return null, diags
	}
	snapshot, err := canonicalSnapshotJSON(raw)
	if err != nil {
		diags.AddError("Unable to encode HubSpot "+k.singular+" snapshot", err.Error())
		return null, diags
	}

	vals := map[string]attr.Value{
		"id":                     types.StringValue(meta.ID),
		"name":                   types.StringValue(meta.Name),
		"description":            optionalString(meta.Description),
		"business_unit_id":       optionalString(meta.BusinessUnitID),
		"owner_user_id":          optionalString(meta.OwnerUserID),
		"archived":               types.BoolValue(meta.Archived),
		"archived_at":            optionalString(meta.ArchivedAt),
		"created_at":             optionalString(meta.CreatedAt),
		"created_by_user_id":     optionalString(meta.CreatedByUserID),
		"updated_at":             optionalString(meta.UpdatedAt),
		"updated_by_user_id":     optionalString(meta.UpdatedByUserID),
		"last_viewed_at":         optionalString(meta.LastViewedAt),
		"last_viewed_by_user_id": optionalString(meta.LastViewedByUserID),
		"raw_json":               types.StringValue(snapshot),
	}

	var tags []apiTag
	var d diag.Diagnostics
	if k.widgets {
		var dash apiDashboard
		if err := json.Unmarshal(raw, &dash); err != nil {
			diags.AddError("Unable to decode HubSpot dashboard", err.Error())
			return null, diags
		}
		tags = dash.Tags
		vals["permissions"], d = dashboardPermissionsToObject(dash.Permissions)
		diags.Append(d...)
		if withWidgets {
			vals["widgets"], d = widgetsToList(dash.Widgets)
			diags.Append(d...)
			vals["report_ids"], d = types.ListValueFrom(ctx, types.StringType, nonNilStrings(widgetReportIDs(dash.Widgets)))
			diags.Append(d...)
		} else {
			vals["widgets"] = types.ListNull(reportingWidgetObjectType)
			vals["report_ids"] = types.ListNull(types.StringType)
		}
	} else {
		var rep apiReport
		if err := json.Unmarshal(raw, &rep); err != nil {
			diags.AddError("Unable to decode HubSpot report", err.Error())
			return null, diags
		}
		tags = rep.Tags
		vals["permissions"], d = reportPermissionsToObject(rep.Permissions)
		diags.Append(d...)
	}
	vals["tags"], d = tagsToList(tags)
	diags.Append(d...)
	if diags.HasError() {
		return null, diags
	}
	obj, d := types.ObjectValue(reportingItemAttrTypes(k), vals)
	diags.Append(d...)
	return obj, diags
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// reportingIDLess orders IDs numerically (falling back to string order for
// non-numeric IDs) so results are deterministic regardless of API sort.
func reportingIDLess(a, b string) bool {
	ai, errA := strconv.ParseInt(a, 10, 64)
	bi, errB := strconv.ParseInt(b, 10, 64)
	switch {
	case errA == nil && errB == nil:
		return ai < bi
	case errA == nil:
		return true
	case errB == nil:
		return false
	default:
		return a < b
	}
}

// reportingRawResult is one search result with its ID decoded.
type reportingRawResult struct {
	ID   string
	Name string
	Raw  json.RawMessage
}

// searchReportingSorted runs searchReporting and returns the results ordered
// by numeric ID.
func searchReportingSorted(ctx context.Context, c *client.Client, collection string, filters url.Values) ([]reportingRawResult, error) {
	raws, err := searchReporting(ctx, c, collection, filters)
	if err != nil {
		return nil, err
	}
	out := make([]reportingRawResult, 0, len(raws))
	for _, raw := range raws {
		var head struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &head); err != nil {
			return nil, fmt.Errorf("decoding search result: %w", err)
		}
		out = append(out, reportingRawResult{ID: head.ID, Name: head.Name, Raw: raw})
	}
	sort.SliceStable(out, func(i, j int) bool { return reportingIDLess(out[i].ID, out[j].ID) })
	return out, nil
}

// reportingStateWord renders "active" / "archived" for diagnostics.
func reportingStateWord(archived bool) string {
	if archived {
		return "archived"
	}
	return "active"
}

// stringSetValues returns a set's string elements sorted (nil when null).
func stringSetValues(ctx context.Context, s types.Set) ([]string, diag.Diagnostics) {
	if s.IsNull() || s.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := s.ElementsAs(ctx, &out, false)
	sort.Strings(out)
	return out, diags
}

// reportingListFilters accumulates the API query filters and a parallel
// rendering in Terraform argument names (for the data source's stable id).
type reportingListFilters struct {
	api  url.Values
	args url.Values
}

func newReportingListFilters() *reportingListFilters {
	return &reportingListFilters{api: url.Values{}, args: url.Values{}}
}

func (f *reportingListFilters) str(arg, param string, v types.String) {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return
	}
	f.api.Set(param, v.ValueString())
	f.args.Set(arg, v.ValueString())
}

// boolean sets param when v is known and non-null; onlyTrue skips false
// (for flags whose false value is the API default).
func (f *reportingListFilters) boolean(arg, param string, v types.Bool, onlyTrue bool) {
	if v.IsNull() || v.IsUnknown() || (onlyTrue && !v.ValueBool()) {
		return
	}
	s := strconv.FormatBool(v.ValueBool())
	if param != "" {
		f.api.Set(param, s)
	}
	f.args.Set(arg, s)
}

// set adds a multi-valued filter as repeated (exploded) query parameters.
func (f *reportingListFilters) set(ctx context.Context, arg, param string, v types.Set) diag.Diagnostics {
	vals, diags := stringSetValues(ctx, v)
	if len(vals) == 0 {
		return diags
	}
	f.api[param] = vals
	f.args.Set(arg, strings.Join(vals, ","))
	return diags
}

// id renders the data source's stable identifier, e.g. "reports" or
// "dashboards?owner_user_ids=2%2C3&query=sales".
func (f *reportingListFilters) id(plural string) string {
	if len(f.args) == 0 {
		return plural
	}
	return plural + "?" + f.args.Encode()
}

// reportingListResult flattens sorted search results into the list data
// source's `ids` and item list values.
func reportingListResult(ctx context.Context, k reportingObjectKind, results []reportingRawResult, withWidgets bool) (types.List, types.List, diag.Diagnostics) {
	var diags diag.Diagnostics
	itemType := types.ObjectType{AttrTypes: reportingItemAttrTypes(k)}
	ids := make([]string, 0, len(results))
	items := make([]attr.Value, 0, len(results))
	for _, r := range results {
		obj, d := reportingItemObject(ctx, k, r.Raw, withWidgets)
		diags.Append(d...)
		if diags.HasError() {
			return types.ListNull(types.StringType), types.ListNull(itemType), diags
		}
		ids = append(ids, r.ID)
		items = append(items, obj)
	}
	idList, d := types.ListValueFrom(ctx, types.StringType, ids)
	diags.Append(d...)
	itemList, d := types.ListValue(itemType, items)
	diags.Append(d...)
	return idList, itemList, diags
}

// ---------------------------------------------------------------------------
// Singular lookup (hubspot_dashboard / hubspot_report).
// ---------------------------------------------------------------------------

// reportingLookup implements the singular data sources: look one dashboard
// or report up by id xor exact name, then always GET it by id so every
// optional section is present and raw_json does not depend on the lookup
// method.
type reportingLookup struct {
	kind   reportingObjectKind
	client *client.Client
}

func (l *reportingLookup) schema(intro string) dschema.Schema {
	k := l.kind
	attrs := reportingItemAttributes(k, "")
	attrs["id"] = dschema.StringAttribute{
		Optional:            true,
		Computed:            true,
		MarkdownDescription: fmt.Sprintf("The %s's ID. Exactly one of `id` or `name` must be set.", k.singular),
		Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
	}
	attrs["name"] = dschema.StringAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: fmt.Sprintf("The %s's exact name (case-sensitive). Exactly one of `id` or `name` must "+
			"be set; a name lookup errors when no %s, or more than one, has that name.", k.singular, k.singular),
		Validators: []validator.String{stringvalidator.LengthAtLeast(1)},
	}
	attrs["archived"] = dschema.BoolAttribute{
		Optional: true,
		Computed: true,
		MarkdownDescription: fmt.Sprintf("Set to `true` to look up an **archived** %s instead of an active one "+
			"(HubSpot keeps the two apart: an archived %s is not found without it, and an active one is not "+
			"found with it). Defaults to `false`.", k.singular, k.singular),
	}
	return dschema.Schema{
		MarkdownDescription: intro + "\n\n" + reportingSnapshotMarkdown + "\n\n" + reportingGapsMarkdown +
			"\n\n" + reportingBetaMarkdown,
		Attributes: attrs,
	}
}

func (l *reportingLookup) configValidators() []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (l *reportingLookup) configure(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, ok := clientFromProviderData(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected data source Configure type",
			fmt.Sprintf("Expected *client.Client, got: %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	l.client = c
}

// findIDsByName searches (q= is a contains-match on name OR description) and
// keeps only exact, case-sensitive name matches, sorted by numeric ID.
func (l *reportingLookup) findIDsByName(ctx context.Context, name string, archived bool) ([]string, error) {
	q := url.Values{"q": {name}}
	if archived {
		q.Set("archived", "true")
	}
	results, err := searchReportingSorted(ctx, l.client, l.kind.collection, q)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range results {
		if r.Name == name {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

func (l *reportingLookup) read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	k := l.kind
	var id, name types.String
	var archivedArg types.Bool
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("name"), &name)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("archived"), &archivedArg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	archived := archivedArg.ValueBool()
	state := reportingStateWord(archived)
	hint := ""
	if !archived {
		hint = fmt.Sprintf(" To look up an archived %s, set archived = true.", k.singular)
	}

	objectID := id.ValueString()
	if id.IsNull() {
		ids, err := l.findIDsByName(ctx, name.ValueString(), archived)
		if err != nil {
			resp.Diagnostics.AddError("Unable to search HubSpot "+k.plural,
				fmt.Sprintf("GET /%s failed: %s", k.collection, reportingErrorDetail(err)))
			return
		}
		switch len(ids) {
		case 0:
			resp.Diagnostics.AddError("HubSpot "+k.singular+" not found",
				fmt.Sprintf("No %s %s named %q exists (the name match is exact and case-sensitive).%s",
					state, k.singular, name.ValueString(), hint))
			return
		case 1:
			objectID = ids[0]
		default:
			resp.Diagnostics.AddError("Ambiguous HubSpot "+k.singular+" name",
				fmt.Sprintf("%d %s %s are named %q (IDs %s). Look the %s up by id instead.",
					len(ids), state, k.plural, name.ValueString(), strings.Join(ids, ", "), k.singular))
			return
		}
	}

	var raw json.RawMessage
	var err error
	if k.widgets {
		_, raw, err = getDashboard(ctx, l.client, objectID, archived)
	} else {
		_, raw, err = getReport(ctx, l.client, objectID, archived)
	}
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("HubSpot "+k.singular+" not found",
				fmt.Sprintf("No %s %s with ID %q exists.%s", state, k.singular, objectID, hint))
			return
		}
		resp.Diagnostics.AddError("Unable to read HubSpot "+k.singular,
			fmt.Sprintf("GET /%s/%s failed: %s", k.collection, objectID, reportingErrorDetail(err)))
		return
	}

	obj, diags := reportingItemObject(ctx, k, raw, true)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, obj)...)
}

// ---------------------------------------------------------------------------
// List (hubspot_dashboards / hubspot_reports).
// ---------------------------------------------------------------------------

// reportingListSchema builds a list data source schema: filters + `id`,
// `ids` and the items attribute (named after the plural) whose objects have
// exactly the singular data source's attributes.
func reportingListSchema(k reportingObjectKind, intro string, filters map[string]dschema.Attribute, widgetsNote string) dschema.Schema {
	attrs := map[string]dschema.Attribute{
		"id": dschema.StringAttribute{
			Computed: true,
			MarkdownDescription: fmt.Sprintf("Stable identifier for this data source: `%s`, followed by the "+
				"non-default filter arguments as a sorted, URL-encoded query string (e.g. `%s?query=sales`).",
				k.plural, k.plural),
		},
		"ids": dschema.ListAttribute{
			Computed:            true,
			ElementType:         types.StringType,
			MarkdownDescription: fmt.Sprintf("IDs of the matching %s, in result order (ascending numeric ID).", k.plural),
		},
		k.plural: dschema.ListNestedAttribute{
			Computed: true,
			MarkdownDescription: fmt.Sprintf("The matching %s, ordered by ascending numeric ID. Each item has the "+
				"same attributes as the `hubspot_%s` data source, including its own `raw_json` snapshot.",
				k.plural, k.singular),
			NestedObject: dschema.NestedAttributeObject{Attributes: reportingItemAttributes(k, widgetsNote)},
		},
	}
	for name, a := range filters {
		attrs[name] = a
	}
	return dschema.Schema{
		MarkdownDescription: intro + " Every page of results is fetched (100 per request, following the " +
			"pagination cursor).\n\n" + reportingSnapshotMarkdown + "\n\n" + reportingGapsMarkdown + "\n\n" +
			reportingBetaMarkdown,
		Attributes: attrs,
	}
}

// reportingCommonFilterAttributes are the filters shared by both list data
// sources.
func reportingCommonFilterAttributes(k reportingObjectKind) map[string]dschema.Attribute {
	set := func(desc string) dschema.SetAttribute {
		return dschema.SetAttribute{Optional: true, ElementType: types.StringType, MarkdownDescription: desc}
	}
	return map[string]dschema.Attribute{
		"query": dschema.StringAttribute{
			Optional: true,
			MarkdownDescription: fmt.Sprintf("Only %s whose name **or description** contains this text "+
				"(case-insensitive, HubSpot's `q` search).", k.plural),
		},
		"owner_user_ids":    set(fmt.Sprintf("Only %s owned by one of these HubSpot user IDs.", k.plural)),
		"tag_ids":           set(fmt.Sprintf("Only %s carrying at least one of these tag IDs.", k.plural)),
		"business_unit_ids": set(fmt.Sprintf("Only %s in one of these business units (`0` is the default business unit).", k.plural)),
		"archived": dschema.BoolAttribute{
			Optional: true,
			MarkdownDescription: fmt.Sprintf("When `true`, returns archived %s **instead of** active ones "+
				"(mirrors the API's `archived` parameter). Defaults to `false`.", k.plural),
		},
	}
}

// reportingCommonFilters translates the shared filter arguments.
func reportingCommonFilters(ctx context.Context, f *reportingListFilters, query types.String, owners, tags, units types.Set, archived types.Bool) diag.Diagnostics {
	var diags diag.Diagnostics
	f.str("query", "q", query)
	diags.Append(f.set(ctx, "owner_user_ids", "ownerUserIds", owners)...)
	diags.Append(f.set(ctx, "tag_ids", "tagIds", tags)...)
	diags.Append(f.set(ctx, "business_unit_ids", "businessUnitIds", units)...)
	f.boolean("archived", "archived", archived, true)
	return diags
}
