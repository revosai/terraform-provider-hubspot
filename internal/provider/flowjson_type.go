// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

// flowJSONType is a string custom type holding an Automation v4 flow graph as
// JSON. The beta API expands a submitted flow with server-injected defaults on
// read-back (top-level keys like `canEnrollFromSalesforce` and `timeWindows`,
// `actionTypeVersion` on every action, and Lists-style filter expansion inside
// enrollment criteria), so a byte-for-byte comparison of the configured JSON
// against the returned JSON diffs forever. Semantic equality treats the
// configured graph as equal to the server graph when one is a structural
// subset of the other (see jsonSubset), suppressing injected defaults while
// still surfacing real edits.
//
// Known limitation (shared with filterBranchType — typed action blocks are
// deferred until the API stabilizes): element order within `actions` and
// filter arrays is significant, so a server that reordered array elements
// would show a false diff. HubSpot preserves submitted order in practice.
type flowJSONType struct {
	basetypes.StringType
}

var _ basetypes.StringTypable = flowJSONType{}

func (t flowJSONType) Equal(o attr.Type) bool {
	other, ok := o.(flowJSONType)
	if !ok {
		return false
	}
	return t.StringType.Equal(other.StringType)
}

func (t flowJSONType) String() string {
	return "flowJSONType"
}

func (t flowJSONType) ValueFromString(_ context.Context, in basetypes.StringValue) (basetypes.StringValuable, diag.Diagnostics) {
	return flowJSONValue{StringValue: in}, nil
}

func (t flowJSONType) ValueFromTerraform(ctx context.Context, in tftypes.Value) (attr.Value, error) {
	attrValue, err := t.StringType.ValueFromTerraform(ctx, in)
	if err != nil {
		return nil, err
	}
	sv, ok := attrValue.(basetypes.StringValue)
	if !ok {
		return nil, fmt.Errorf("unexpected value type %T from StringType.ValueFromTerraform", attrValue)
	}
	return flowJSONValue{StringValue: sv}, nil
}

func (t flowJSONType) ValueType(_ context.Context) attr.Value {
	return flowJSONValue{}
}

// flowJSONValue is the value type for flowJSONType.
type flowJSONValue struct {
	basetypes.StringValue
}

var (
	_ basetypes.StringValuableWithSemanticEquals = flowJSONValue{}
	_ xattr.ValidateableAttribute                = flowJSONValue{}
)

func (v flowJSONValue) Type(_ context.Context) attr.Type {
	return flowJSONType{}
}

func (v flowJSONValue) Equal(o attr.Value) bool {
	other, ok := o.(flowJSONValue)
	if !ok {
		return false
	}
	return v.StringValue.Equal(other.StringValue)
}

// ValidateAttribute rejects flow_json values that are not JSON objects at
// plan time, so a malformed jsonencode() fails before any API call.
func (v flowJSONValue) ValidateAttribute(_ context.Context, req xattr.ValidateAttributeRequest, resp *xattr.ValidateAttributeResponse) {
	if v.IsNull() || v.IsUnknown() {
		return
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(v.ValueString()), &m); err != nil {
		resp.Diagnostics.AddAttributeError(
			req.Path,
			"Invalid flow JSON",
			"flow_json must be a JSON object (use jsonencode({...})): "+err.Error(),
		)
	}
}

// StringSemanticEquals reports whether the two JSON flow graphs are equivalent
// modulo HubSpot's server-injected defaults. Both values are known, non-null
// strings when the framework calls this. If either fails to parse as JSON, it
// returns false so the raw diff surfaces rather than being silently swallowed.
func (v flowJSONValue) StringSemanticEquals(_ context.Context, newValuable basetypes.StringValuable) (bool, diag.Diagnostics) {
	var diags diag.Diagnostics

	newValue, ok := newValuable.(flowJSONValue)
	if !ok {
		diags.AddError(
			"Semantic Equality Check Error",
			fmt.Sprintf("expected value type flowJSONValue but got %T. This is a bug in the provider.", newValuable),
		)
		return false, diags
	}

	var a, b any
	if err := json.Unmarshal([]byte(v.ValueString()), &a); err != nil {
		return false, diags
	}
	if err := json.Unmarshal([]byte(newValue.ValueString()), &b); err != nil {
		return false, diags
	}
	return jsonSubset(a, b) || jsonSubset(b, a), diags
}
