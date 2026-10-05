// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

// STUB: placeholder registered by the reporting foundation commit; replaced
// by the full implementation.

import (
	"context"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
)

type reportResource struct{}

// NewReportResource returns the hubspot_report resource.
func NewReportResource() resource.Resource { return &reportResource{} }

func (r *reportResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_report"
}

func (r *reportResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Not yet implemented.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID."},
		},
	}
}

func (r *reportResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Not implemented", "hubspot_report is not implemented yet")
}

func (r *reportResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {}

func (r *reportResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Not implemented", "hubspot_report is not implemented yet")
}

func (r *reportResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {}
