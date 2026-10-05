// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	dschema "github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

var (
	_ datasource.DataSource              = &reportsDataSource{}
	_ datasource.DataSourceWithConfigure = &reportsDataSource{}
)

// reportsDataSource lists reports matching the given filters (Analytics
// Reporting API beta) — the bulk "sync to repo" surface.
type reportsDataSource struct {
	client *client.Client
}

// NewReportsDataSource returns the hubspot_reports data source.
func NewReportsDataSource() datasource.DataSource {
	return &reportsDataSource{}
}

type reportsDataSourceModel struct {
	ID              types.String `tfsdk:"id"`
	Query           types.String `tfsdk:"query"`
	DashboardID     types.String `tfsdk:"dashboard_id"`
	OnDashboard     types.Bool   `tfsdk:"on_dashboard"`
	OwnerUserIDs    types.Set    `tfsdk:"owner_user_ids"`
	TagIDs          types.Set    `tfsdk:"tag_ids"`
	BusinessUnitIDs types.Set    `tfsdk:"business_unit_ids"`
	Archived        types.Bool   `tfsdk:"archived"`
	IDs             types.List   `tfsdk:"ids"`
	Reports         types.List   `tfsdk:"reports"`
}

func (d *reportsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_reports"
}

func (d *reportsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	filters := reportingCommonFilterAttributes(reportKind)
	filters["dashboard_id"] = dschema.StringAttribute{
		Optional:            true,
		MarkdownDescription: "Only reports shown on this dashboard (by dashboard ID).",
		Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
	}
	filters["on_dashboard"] = dschema.BoolAttribute{
		Optional: true,
		MarkdownDescription: "When `true`, only reports that are on at least one dashboard; when `false`, only " +
			"reports on no dashboard (handy for finding orphaned reports). Unset returns both.",
	}
	resp.Schema = reportingListSchema(reportKind,
		"Lists HubSpot reports matching optional filters (all filters combine with AND; within a set filter, "+
			"any value matches). Typical use: snapshot every report's metadata to git with `local_file` and "+
			"`for_each` over the results, find orphaned reports with `on_dashboard = false`, or feed `ids` into "+
			"`import` blocks to adopt them as `hubspot_report` resources.",
		filters, "")
}

func (d *reportsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, ok := clientFromProviderData(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected data source Configure type",
			fmt.Sprintf("Expected *client.Client, got: %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.client = c
}

func (d *reportsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config reportsDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	f := newReportingListFilters()
	resp.Diagnostics.Append(reportingCommonFilters(ctx, f, config.Query, config.OwnerUserIDs, config.TagIDs,
		config.BusinessUnitIDs, config.Archived)...)
	f.str("dashboard_id", "dashboardId", config.DashboardID)
	f.boolean("on_dashboard", "onDashboard", config.OnDashboard, false)
	if resp.Diagnostics.HasError() {
		return
	}
	query := f.api
	query.Set("properties", reportProperties)

	results, err := searchReportingSorted(ctx, d.client, reportsBasePath, query)
	if err != nil {
		resp.Diagnostics.AddError("Unable to search HubSpot reports",
			fmt.Sprintf("GET /%s failed: %s", reportsBasePath, reportingErrorDetail(err)))
		return
	}

	ids, items, diags := reportingListResult(ctx, reportKind, results, false)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	config.ID = types.StringValue(f.id(reportKind.plural))
	config.IDs = ids
	config.Reports = items
	resp.Diagnostics.Append(resp.State.Set(ctx, &config)...)
}
