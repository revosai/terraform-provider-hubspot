// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

var (
	_ datasource.DataSource                     = &dashboardDataSource{}
	_ datasource.DataSourceWithConfigure        = &dashboardDataSource{}
	_ datasource.DataSourceWithConfigValidators = &dashboardDataSource{}
)

// dashboardDataSource looks up one dashboard by id or exact name (Analytics
// Reporting API beta), including permissions, tags, widgets and a raw_json
// snapshot.
type dashboardDataSource struct {
	lookup reportingLookup
}

// NewDashboardDataSource returns the hubspot_dashboard data source.
func NewDashboardDataSource() datasource.DataSource {
	return &dashboardDataSource{lookup: reportingLookup{kind: dashboardKind}}
}

func (d *dashboardDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (d *dashboardDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = d.lookup.schema("Looks up one HubSpot reporting dashboard by `id` or exact `name`, exposing its " +
		"metadata, permissions, tags, widget layout (`widgets`), the reports it shows (`report_ids`) and a " +
		"`raw_json` snapshot. Name lookups search with HubSpot's contains-match `q` filter and then keep only " +
		"exact, case-sensitive name matches; the dashboard is then always fetched by ID, so the result (including " +
		"`raw_json`) is identical whichever way it was looked up.")
}

func (d *dashboardDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return d.lookup.configValidators()
}

func (d *dashboardDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.lookup.configure(req, resp)
}

func (d *dashboardDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	d.lookup.read(ctx, req, resp)
}
