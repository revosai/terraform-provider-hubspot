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

type reportsDataSource struct{}

// NewReportsDataSource returns the hubspot_reports data source.
func NewReportsDataSource() datasource.DataSource { return &reportsDataSource{} }

func (d *reportsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_reports"
}

func (d *reportsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Not yet implemented.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID."},
		},
	}
}

func (d *reportsDataSource) Read(_ context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	resp.Diagnostics.AddError("Not implemented", "hubspot_reports is not implemented yet")
}
