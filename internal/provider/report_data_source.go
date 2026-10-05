// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
)

var (
	_ datasource.DataSource                     = &reportDataSource{}
	_ datasource.DataSourceWithConfigure        = &reportDataSource{}
	_ datasource.DataSourceWithConfigValidators = &reportDataSource{}
)

// reportDataSource looks up one report by id or exact name (Analytics
// Reporting API beta), including permissions, tags and a raw_json snapshot.
type reportDataSource struct {
	lookup reportingLookup
}

// NewReportDataSource returns the hubspot_report data source.
func NewReportDataSource() datasource.DataSource {
	return &reportDataSource{lookup: reportingLookup{kind: reportKind}}
}

func (d *reportDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_report"
}

func (d *reportDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = d.lookup.schema("Looks up one HubSpot report by `id` or exact `name`, exposing its metadata, " +
		"permissions, tags and a `raw_json` snapshot. Name lookups search with HubSpot's contains-match `q` filter " +
		"and then keep only exact, case-sensitive name matches; the report is then always fetched by ID, so the " +
		"result (including `raw_json`) is identical whichever way it was looked up. To find the reports on a " +
		"dashboard, use `hubspot_reports` with `dashboard_id` or the `report_ids` of `hubspot_dashboard`.")
}

func (d *reportDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return d.lookup.configValidators()
}

func (d *reportDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.lookup.configure(req, resp)
}

func (d *reportDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	d.lookup.read(ctx, req, resp)
}
