// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

const defaultBaseURL = "https://api.hubapi.com"

var _ provider.Provider = &HubSpotProvider{}

// HubSpotProvider manages HubSpot portal configuration (the "schema/config
// plane": properties, groups, pipelines, object schemas) via private-app
// token auth. It deliberately does not manage CRM records.
type HubSpotProvider struct {
	version string
}

// New returns a provider constructor for the given build version.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &HubSpotProvider{version: version}
	}
}

// HubSpotProviderModel maps the provider configuration block.
type HubSpotProviderModel struct {
	AccessToken types.String `tfsdk:"access_token"`
	BaseURL     types.String `tfsdk:"base_url"`
}

func (p *HubSpotProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "hubspot"
	resp.Version = p.version
}

func (p *HubSpotProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage HubSpot portal configuration (CRM schema plane) as code: properties, " +
			"property groups, and more. Authenticates with a HubSpot service key (recommended; public beta) " +
			"or a legacy private app access token — both use the same `pat-...` bearer format. " +
			"The provider never requests CRM record scopes.",
		Attributes: map[string]schema.Attribute{
			"access_token": schema.StringAttribute{
				Optional:  true,
				Sensitive: true,
				MarkdownDescription: "HubSpot API credential (`pat-...`): a service key (Development → Keys → " +
					"Service keys; recommended) or a legacy private app access token. May also be set via the " +
					"`HUBSPOT_ACCESS_TOKEN` environment variable.",
			},
			"base_url": schema.StringAttribute{
				Optional: true,
				MarkdownDescription: "HubSpot API base URL. Defaults to `https://api.hubapi.com`. May also be " +
					"set via the `HUBSPOT_BASE_URL` environment variable. Override only for testing.",
			},
		},
	}
}

func (p *HubSpotProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config HubSpotProviderModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if config.AccessToken.IsUnknown() {
		resp.Diagnostics.AddAttributeError(
			path.Root("access_token"),
			"Unknown HubSpot access token",
			"The provider cannot create the HubSpot API client because the access_token configuration value is "+
				"derived from a value that is not yet known. Set a static value or use the HUBSPOT_ACCESS_TOKEN "+
				"environment variable.",
		)
		return
	}

	accessToken := os.Getenv("HUBSPOT_ACCESS_TOKEN")
	if !config.AccessToken.IsNull() {
		accessToken = config.AccessToken.ValueString()
	}
	if accessToken == "" {
		resp.Diagnostics.AddAttributeError(
			path.Root("access_token"),
			"Missing HubSpot access token",
			"Set the access_token provider attribute or the HUBSPOT_ACCESS_TOKEN environment variable to a "+
				"HubSpot private app access token.",
		)
		return
	}

	baseURL := os.Getenv("HUBSPOT_BASE_URL")
	if !config.BaseURL.IsNull() {
		baseURL = config.BaseURL.ValueString()
	}
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	client.DebugLogger = tflog.Debug

	c, err := client.New(client.Config{
		BaseURL:     baseURL,
		AccessToken: accessToken,
		UserAgent:   "terraform-provider-hubspot/" + p.version,
	})
	if err != nil {
		resp.Diagnostics.AddError("Unable to create HubSpot API client", err.Error())
		return
	}

	resp.ResourceData = c
	resp.DataSourceData = c
}

func (p *HubSpotProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewPropertyGroupResource,
		NewPropertyResource,
		NewPipelineResource,
		NewObjectSchemaResource,
		NewAssociationLabelResource,
		NewListResource,
		NewWorkflowResource,
		NewDashboardResource,
		NewReportResource,
	}
}

func (p *HubSpotProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewPropertyDataSource,
		NewPropertiesDataSource,
		NewOwnerDataSource,
		NewPortalDataSource,
		NewAssociationLabelsDataSource,
		NewPipelineDataSource,
		NewObjectSchemaDataSource,
		NewWorkflowDataSource,
		NewDashboardDataSource,
		NewDashboardsDataSource,
		NewReportDataSource,
		NewReportsDataSource,
	}
}

func (p *HubSpotProvider) Functions(_ context.Context) []func() function.Function {
	return []func() function.Function{}
}

func (p *HubSpotProvider) EphemeralResources(_ context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{}
}

// clientFromProviderData is the shared Configure helper for resources and
// data sources: it type-asserts the provider-configured *client.Client.
func clientFromProviderData(data any) (*client.Client, bool) {
	if data == nil {
		return nil, true // ValidateConfig RPCs run before ConfigureProvider.
	}
	c, ok := data.(*client.Client)
	return c, ok
}
