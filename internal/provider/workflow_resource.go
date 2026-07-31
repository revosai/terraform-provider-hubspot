// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/hashicorp/terraform-plugin-framework/path"

	"github.com/revosai/terraform-provider-hubspot/internal/client"
)

var (
	_ resource.Resource                = &workflowResource{}
	_ resource.ResourceWithConfigure   = &workflowResource{}
	_ resource.ResourceWithImportState = &workflowResource{}
)

// flowsBasePath is the Automation v4 (beta) flows collection.
const flowsBasePath = "automation/v4/flows"

// flowManagedFields are the top-level flow fields owned by typed attributes
// or by the server: they are overlaid onto the request body on write and
// stripped from the returned flow before the remainder becomes flow_json.
var flowManagedFields = []string{
	"id", "revisionId", "name", "type", "objectTypeId", "isEnabled",
	"flowType", "createdAt", "updatedAt",
}

// workflowResource implements hubspot_workflow: a workflow managed as a raw
// JSON flow graph via the Automation v4 **beta** API (/automation/v4/flows).
// Updates are full-replace PUTs guarded by HubSpot's revisionId optimistic
// lock, handled GET-then-PUT so concurrent UI edits never strand an apply.
type workflowResource struct {
	client *client.Client
}

// NewWorkflowResource returns the hubspot_workflow resource.
func NewWorkflowResource() resource.Resource {
	return &workflowResource{}
}

type workflowResourceModel struct {
	ID           types.String  `tfsdk:"id"`
	FlowID       types.String  `tfsdk:"flow_id"`
	Name         types.String  `tfsdk:"name"`
	FlowType     types.String  `tfsdk:"flow_type"`
	ObjectTypeID types.String  `tfsdk:"object_type_id"`
	Enabled      types.Bool    `tfsdk:"enabled"`
	RevisionID   types.String  `tfsdk:"revision_id"`
	FlowJSON     flowJSONValue `tfsdk:"flow_json"`
}

func (r *workflowResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_workflow"
}

func (r *workflowResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a workflow via the Automation v4 API (`/automation/v4/flows`). Requires " +
			"the `automation` scope.\n\n" +
			"**The Automation v4 API is a public beta**, so this resource is beta-backed per the provider's " +
			"[API stability policy](https://github.com/revosai/terraform-provider-hubspot/blob/main/ROADMAP.md#api-stability-policy): " +
			"the flow graph is deliberately raw JSON, and upstream API changes are absorbed in minor releases.\n\n" +
			"**`flow_json` is passed through as JSON.** HubSpot expands the submitted graph with server-injected " +
			"defaults on read-back (`canEnrollFromSalesforce`, per-action `actionTypeVersion`, expanded enrollment " +
			"filters, …); the provider compares it **semantically**, so an unchanged configuration plans empty.\n\n" +
			"**Updates are full-replace PUTs with optimistic locking.** HubSpot requires the flow's current " +
			"`revisionId` on every PUT; the provider fetches it immediately before writing (GET-then-PUT), so " +
			"edits made concurrently in the HubSpot UI don't strand an apply — but they are overwritten, since " +
			"the configuration is the source of truth.\n\n" +
			"Changing `flow_type` or `object_type_id` forces replacement. Deleting a workflow moves it to the " +
			"deleted state in HubSpot (restorable in the UI for 90 days).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "The workflow's flow ID (identical to `flow_id`).",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"flow_id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Server-assigned flow ID.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"name": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Human-readable workflow name. Mutable (in-place update).",
			},
			"flow_type": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Workflow type: `CONTACT_FLOW` (contact-based) or `PLATFORM_FLOW` (any other " +
					"CRM object). Changing this forces replacement.",
				Validators: []validator.String{
					stringvalidator.OneOf("CONTACT_FLOW", "PLATFORM_FLOW"),
				},
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"object_type_id": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Object type the workflow runs on (e.g. `0-3` for deals). Required for " +
					"`PLATFORM_FLOW`; defaults to `0-1` (contacts) for `CONTACT_FLOW`. Changing this forces " +
					"replacement.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"enabled": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Whether the workflow is turned on and enrolling objects. Defaults to " +
					"`false` so new automation stays off until reviewed. Mutable (in-place update).",
			},
			"revision_id": schema.StringAttribute{
				Computed: true,
				MarkdownDescription: "The flow's current revision, advanced by HubSpot on every write. Used as " +
					"the optimistic lock on updates.",
			},
			"flow_json": schema.StringAttribute{
				CustomType: flowJSONType{},
				Required:   true,
				MarkdownDescription: "The flow graph as JSON (use `jsonencode(...)`): `actions`, " +
					"`enrollmentCriteria`, `startActionId`, and any other Automation v4 flow fields not managed " +
					"by a typed attribute. Compared semantically to absorb HubSpot's server-injected defaults.",
			},
		},
	}
}

func (r *workflowResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	c, ok := clientFromProviderData(req.ProviderData)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected resource Configure type",
			fmt.Sprintf("Expected *client.Client, got %T. This is a bug in the provider.", req.ProviderData),
		)
		return
	}
	r.client = c
}

// buildFlowBody assembles the full write body: the configured flow graph with
// the typed attributes (and, for updates, the optimistic-lock revisionId)
// overlaid on top.
func buildFlowBody(plan workflowResourceModel, revisionID string) (map[string]any, error) {
	body := map[string]any{}
	if err := json.Unmarshal([]byte(plan.FlowJSON.ValueString()), &body); err != nil {
		return nil, fmt.Errorf("flow_json is not a JSON object: %w", err)
	}
	body["name"] = plan.Name.ValueString()
	body["type"] = plan.FlowType.ValueString()
	body["isEnabled"] = plan.Enabled.ValueBool()
	body["flowType"] = "WORKFLOW"
	if !plan.ObjectTypeID.IsNull() && !plan.ObjectTypeID.IsUnknown() {
		body["objectTypeId"] = plan.ObjectTypeID.ValueString()
	}
	if revisionID != "" {
		body["revisionId"] = revisionID
	}
	return body, nil
}

// flowWire is the typed slice of a returned flow; the rest of the graph is
// carried in the raw map it was decoded from.
type flowWire struct {
	ID           string `json:"id"`
	RevisionID   string `json:"revisionId"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	ObjectTypeID string `json:"objectTypeId"`
	IsEnabled    bool   `json:"isEnabled"`
}

// decodeFlow splits a returned flow into its typed fields and the flow-graph
// remainder (managed fields stripped, re-marshaled with sorted keys).
func decodeFlow(raw map[string]any) (flowWire, string, error) {
	var wire flowWire
	buf, err := json.Marshal(raw)
	if err != nil {
		return wire, "", err
	}
	if err := json.Unmarshal(buf, &wire); err != nil {
		return wire, "", err
	}
	rest := make(map[string]any, len(raw))
	for k, v := range raw {
		if slices.Contains(flowManagedFields, k) {
			continue
		}
		rest[k] = v
	}
	restJSON, err := json.Marshal(rest)
	if err != nil {
		return wire, "", err
	}
	return wire, string(restJSON), nil
}

func (r *workflowResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan workflowResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	body, err := buildFlowBody(plan, "")
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("flow_json"), "Invalid flow JSON", err.Error())
		return
	}

	var out map[string]any
	if err := r.client.Post(ctx, flowsBasePath, body, &out); err != nil {
		resp.Diagnostics.AddError(
			"Unable to create HubSpot workflow",
			fmt.Sprintf("POST /%s failed: %s", flowsBasePath, err),
		)
		return
	}
	wire, _, err := decodeFlow(out)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to decode HubSpot workflow create response",
			fmt.Sprintf("POST /%s returned an undecodable flow: %s", flowsBasePath, err),
		)
		return
	}

	// The configured flow_json is kept in state; a later read reconciles it
	// semantically against the server's normalized graph.
	plan.ID = types.StringValue(wire.ID)
	plan.FlowID = types.StringValue(wire.ID)
	plan.Name = types.StringValue(wire.Name)
	plan.FlowType = types.StringValue(wire.Type)
	plan.ObjectTypeID = types.StringValue(wire.ObjectTypeID)
	plan.Enabled = types.BoolValue(wire.IsEnabled)
	plan.RevisionID = types.StringValue(wire.RevisionID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *workflowResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state workflowResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	p := flowsBasePath + "/" + url.PathEscape(state.FlowID.ValueString())
	var out map[string]any
	if err := r.client.Get(ctx, p, nil, &out); err != nil {
		if client.IsNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError(
			"Unable to read HubSpot workflow",
			fmt.Sprintf("GET %s failed: %s", p, err),
		)
		return
	}
	wire, rest, err := decodeFlow(out)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to decode HubSpot workflow",
			fmt.Sprintf("GET %s returned an undecodable flow: %s", p, err),
		)
		return
	}

	state.ID = types.StringValue(wire.ID)
	state.FlowID = types.StringValue(wire.ID)
	state.Name = types.StringValue(wire.Name)
	state.FlowType = types.StringValue(wire.Type)
	state.ObjectTypeID = types.StringValue(wire.ObjectTypeID)
	state.Enabled = types.BoolValue(wire.IsEnabled)
	state.RevisionID = types.StringValue(wire.RevisionID)
	// Semantic equality keeps the prior configured value if equivalent.
	state.FlowJSON = flowJSONValue{StringValue: types.StringValue(rest)}

	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *workflowResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state workflowResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	flowID := state.FlowID.ValueString()
	p := flowsBasePath + "/" + url.PathEscape(flowID)

	// Optimistic lock: PUT is a full replace that must carry the flow's
	// CURRENT revisionId, so fetch it immediately before writing. A revision
	// advanced by a concurrent UI edit is absorbed here — the configuration
	// overwrites it, which is the declarative contract.
	var current map[string]any
	if err := r.client.Get(ctx, p, nil, &current); err != nil {
		resp.Diagnostics.AddError(
			"Unable to read HubSpot workflow before update",
			fmt.Sprintf("GET %s (for the current revisionId) failed: %s", p, err),
		)
		return
	}
	revisionID, _ := current["revisionId"].(string)

	body, err := buildFlowBody(plan, revisionID)
	if err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("flow_json"), "Invalid flow JSON", err.Error())
		return
	}

	var out map[string]any
	if err := r.client.Put(ctx, p, body, &out); err != nil {
		if client.IsConflict(err) {
			resp.Diagnostics.AddError(
				"HubSpot workflow was modified concurrently",
				fmt.Sprintf("PUT %s was rejected by HubSpot's revision lock (revision %s is no longer "+
					"current): the workflow changed between the provider's read and write. Re-run the apply "+
					"to retry against the latest revision. Underlying error: %s", p, revisionID, err),
			)
			return
		}
		resp.Diagnostics.AddError(
			"Unable to update HubSpot workflow",
			fmt.Sprintf("PUT %s failed: %s", p, err),
		)
		return
	}
	wire, _, err := decodeFlow(out)
	if err != nil {
		resp.Diagnostics.AddError(
			"Unable to decode HubSpot workflow update response",
			fmt.Sprintf("PUT %s returned an undecodable flow: %s", p, err),
		)
		return
	}

	plan.ID = state.ID
	plan.FlowID = state.FlowID
	plan.ObjectTypeID = types.StringValue(wire.ObjectTypeID)
	plan.RevisionID = types.StringValue(wire.RevisionID)

	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *workflowResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state workflowResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	p := flowsBasePath + "/" + url.PathEscape(state.FlowID.ValueString())
	if err := r.client.Delete(ctx, p, nil); err != nil {
		if client.IsNotFound(err) {
			return // Already gone; deletion is idempotent.
		}
		resp.Diagnostics.AddError(
			"Unable to delete HubSpot workflow",
			fmt.Sprintf("DELETE %s failed: %s", p, err),
		)
	}
}

func (r *workflowResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("flow_id"), req.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), req.ID)...)
}
