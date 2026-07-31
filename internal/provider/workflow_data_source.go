// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

var (
	_ datasource.DataSource                     = &workflowDataSource{}
	_ datasource.DataSourceWithConfigure        = &workflowDataSource{}
	_ datasource.DataSourceWithConfigValidators = &workflowDataSource{}
)

// workflowDataSource looks up a workflow via the Automation v4 **beta** API,
// either directly by flow ID or by exact name (paging the flows collection,
// erroring when the name is ambiguous). Handy for referencing unmanaged
// automation and for point-in-time backups of a flow definition.
type workflowDataSource struct {
	client *client.Client
}

// NewWorkflowDataSource returns the hubspot_workflow data source.
func NewWorkflowDataSource() datasource.DataSource {
	return &workflowDataSource{}
}

type workflowDataSourceModel struct {
	ID           types.String `tfsdk:"id"`
	FlowID       types.String `tfsdk:"flow_id"`
	Name         types.String `tfsdk:"name"`
	FlowType     types.String `tfsdk:"flow_type"`
	ObjectTypeID types.String `tfsdk:"object_type_id"`
	Enabled      types.Bool   `tfsdk:"enabled"`
	RevisionID   types.String `tfsdk:"revision_id"`
	FlowJSON     types.String `tfsdk:"flow_json"`
}

func (d *workflowDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workflow"
}

func (d *workflowDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a workflow via the Automation v4 API (**public beta**) by flow ID or by " +
			"exact name (an ambiguous name is an error). Exposes the complete flow definition as JSON — useful " +
			"for referencing unmanaged automation or writing point-in-time backups. Requires the `automation` " +
			"scope.",
		Attributes: map[string]schema.Attribute{
			"flow_id": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "The workflow's flow ID. Exactly one of `flow_id` or `name` must be set.",
			},
			"name": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Exact workflow name to look up. Exactly one of `flow_id` or `name` must be set.",
			},
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Identifier for the data source (the flow ID).",
			},
			"flow_type": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Workflow type: `CONTACT_FLOW` or `PLATFORM_FLOW`.",
			},
			"object_type_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Object type the workflow runs on (e.g. `0-1` for contacts).",
			},
			"enabled": schema.BoolAttribute{
				Computed:            true,
				MarkdownDescription: "Whether the workflow is turned on and enrolling objects.",
			},
			"revision_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The flow's current revision, advanced by HubSpot on every write.",
			},
			"flow_json": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The complete flow definition as returned by the Automation v4 API, " +
					"including server-managed fields — suitable for backups. (The `hubspot_workflow` resource's " +
					"`flow_json` argument instead excludes fields managed by typed attributes.)",
			},
		},
	}
}

func (d *workflowDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(
			path.MatchRoot("flow_id"),
			path.MatchRoot("name"),
		),
	}
}

func (d *workflowDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	c, ok := clientFromProviderData(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError("Unexpected data source Configure type",
			fmt.Sprintf("Expected *client.Client, got: %T. This is a bug in the provider.", req.ProviderData))
		return
	}
	d.client = c
}

// flowListPage is one page of GET /automation/v4/flows.
type flowListPage struct {
	Results []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"results"`
	Paging *struct {
		Next *struct {
			After string `json:"after"`
		} `json:"next"`
	} `json:"paging"`
}

// findFlowIDByName pages the flows collection and returns the IDs of every
// flow whose name matches exactly.
func (d *workflowDataSource) findFlowIDsByName(ctx context.Context, name string) ([]string, error) {
	var matches []string
	after := ""
	for {
		q := url.Values{}
		q.Set("limit", "100")
		if after != "" {
			q.Set("after", after)
		}
		var page flowListPage
		if err := d.client.Get(ctx, flowsBasePath, q, &page); err != nil {
			return nil, err
		}
		for _, fl := range page.Results {
			if fl.Name == name {
				matches = append(matches, fl.ID)
			}
		}
		if page.Paging == nil || page.Paging.Next == nil || page.Paging.Next.After == "" {
			return matches, nil
		}
		after = page.Paging.Next.After
	}
}

func (d *workflowDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var config workflowDataSourceModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}

	flowID := config.FlowID.ValueString()
	if flowID == "" {
		name := config.Name.ValueString()
		ids, err := d.findFlowIDsByName(ctx, name)
		if err != nil {
			resp.Diagnostics.AddError("Unable to list HubSpot workflows",
				fmt.Sprintf("GET /%s failed: %s", flowsBasePath, err))
			return
		}
		switch len(ids) {
		case 0:
			resp.Diagnostics.AddError("HubSpot workflow not found",
				fmt.Sprintf("No workflow named %q exists.", name))
			return
		case 1:
			flowID = ids[0]
		default:
			resp.Diagnostics.AddError("Ambiguous HubSpot workflow name",
				fmt.Sprintf("%d workflows are named %q (flow IDs %s). Look the workflow up by flow_id instead.",
					len(ids), name, strings.Join(ids, ", ")))
			return
		}
	}

	// GET by ID for the full, strongly consistent definition.
	p := flowsBasePath + "/" + url.PathEscape(flowID)
	var out map[string]any
	if err := d.client.Get(ctx, p, nil, &out); err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("HubSpot workflow not found",
				fmt.Sprintf("No workflow with flow ID %q exists.", flowID))
			return
		}
		resp.Diagnostics.AddError("Unable to read HubSpot workflow",
			fmt.Sprintf("GET %s failed: %s", p, err))
		return
	}

	wire, _, err := decodeFlow(out)
	if err != nil {
		resp.Diagnostics.AddError("Unable to decode HubSpot workflow",
			fmt.Sprintf("GET %s returned an undecodable flow: %s", p, err))
		return
	}
	full, err := json.Marshal(out)
	if err != nil {
		resp.Diagnostics.AddError("Unable to encode HubSpot workflow",
			fmt.Sprintf("re-encoding the flow definition failed: %s", err))
		return
	}

	state := workflowDataSourceModel{
		ID:           types.StringValue(wire.ID),
		FlowID:       types.StringValue(wire.ID),
		Name:         types.StringValue(wire.Name),
		FlowType:     types.StringValue(wire.Type),
		ObjectTypeID: types.StringValue(wire.ObjectTypeID),
		Enabled:      types.BoolValue(wire.IsEnabled),
		RevisionID:   types.StringValue(wire.RevisionID),
		FlowJSON:     types.StringValue(string(full)),
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
