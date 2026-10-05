// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

// STUB: placeholder registered by the reporting foundation commit; replaced
// by the full implementation.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
)

type dashboardDataSource struct{}

// NewDashboardDataSource returns the hubspot_dashboard data source.
func NewDashboardDataSource() datasource.DataSource { return &dashboardDataSource{} }

func (d *dashboardDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (d *dashboardDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Not yet implemented.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID."},
		},
	}
}

func (d *dashboardDataSource) Read(_ context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	resp.Diagnostics.AddError("Not implemented", "hubspot_dashboard is not implemented yet")
}
