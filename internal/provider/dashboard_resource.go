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

type dashboardResource struct{}

// NewDashboardResource returns the hubspot_dashboard resource.
func NewDashboardResource() resource.Resource { return &dashboardResource{} }

func (r *dashboardResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dashboard"
}

func (r *dashboardResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Not yet implemented.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{Computed: true, MarkdownDescription: "ID."},
		},
	}
}

func (r *dashboardResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Not implemented", "hubspot_dashboard is not implemented yet")
}

func (r *dashboardResource) Read(_ context.Context, _ resource.ReadRequest, _ *resource.ReadResponse) {}

func (r *dashboardResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Not implemented", "hubspot_dashboard is not implemented yet")
}

func (r *dashboardResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {}
