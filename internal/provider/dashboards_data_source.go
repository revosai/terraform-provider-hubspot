// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

var (
	_ datasource.DataSource              = &dashboardsDataSource{}
	_ datasource.DataSourceWithConfigure = &dashboardsDataSource{}
)

// dashboardsDataSource lists dashboards matching the given filters (Analytics
// Reporting API beta) — the bulk "sync to repo" surface.
type dashboardsDataSource struct {
	client *client.Client
}

// NewDashboardsDataSource returns the hubspot_dashboards data source.
func NewDashboardsDataSource() datasource.DataSource {
	return &dashboardsDataSource{}
}

type dashboardsDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	Query           types.String `tfsdk:"query"`
	OwnerUserIDs    types.Set    `tfsdk:"owner_user_ids"`
	TagIDs          types.Set    `tfsdk:"tag_ids"`
	BusinessUnitIDs types.Set    `tfsdk:"business_unit_ids"`
	Archived        types.Bool   `tfsdk:"archived"`
	IncludeWidgets  types.Bool   `tfsdk:"include_widgets"`
	IDs             types.List   `tfsdk:"ids"`
	Dashboards      types.List   `tfsdk:"dashboards"`
}

// dashboardPropertiesWithoutWidgets is requested when include_widgets is false.
const dashboardPropertiesWithoutWidgets = "permissions,tags"

const dashboardsWidgetsNote = " Null unless the data source sets `include_widgets = true`."

func (d *dashboardsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboards"
}

func (d *dashboardsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	filters := reportingCommonFilterAttributes(dashboardKind)
	filters["include_widgets"] = dschema.BoolAttribute{
		Optional: true,
		MarkdownDescription: "When `true`, also requests each dashboard's widgets, populating `widgets` and " +
			"`report_ids` and including `widgets` in each `raw_json` (which then equals the `hubspot_dashboard` " +
			"data source's `raw_json`). Defaults to `false`: widgets are not requested, `widgets` / `report_ids` " +
			"are null and `raw_json` has no `widgets` key.",
	}
	resp.Schema = reportingListSchema(dashboardKind,
		"Lists HubSpot reporting dashboards matching optional filters (all filters combine with AND; within a "+
			"set filter, any value matches). Typical use: snapshot every dashboard to git with `local_file` and "+
			"`for_each` over the results, or feed `ids` into `import` blocks to adopt them as `hubspot_dashboard` "+
			"resources.",
		filters, dashboardsWidgetsNote)
}

func (d *dashboardsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, ok := clientFromProviderData(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected data source Configure type",
			fmt.Sprintf("Expected *client.Client, got: %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.client = c
}

func (d *dashboardsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config dashboardsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := newReportingListFilters()
	resp.Diagnostics.Append(reportingCommonFilters(ctx, f, config.Query, config.OwnerUserIDs, config.TagIDs,
		config.BusinessUnitIDs, config.Archived)...)
	f.boolean("include_widgets", "", config.IncludeWidgets, true)
	if resp.Diagnostics.HasError() {
		return
	}
	withWidgets := config.IncludeWidgets.ValueBool()
	query := f.api
	if withWidgets {
		query.Set("properties", dashboardProperties)
	} else {
		query.Set("properties", dashboardPropertiesWithoutWidgets)
	}

	results, err := searchReportingSorted(ctx, d.client, dashboardsBasePath, query)
	if err != nil {
		resp.Diagnostics.AddError("Unable to search HubSpot dashboards",
			fmt.Sprintf("GET /%s failed: %s", dashboardsBasePath, reportingErrorDetail(err)))
		return
	}

	ids, items, diags := reportingListResult(ctx, dashboardKind, results, withWidgets)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	config.ID = types.StringValue(f.id(dashboardKind.plural))
	config.IDs = ids
	config.Dashboards = items
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
