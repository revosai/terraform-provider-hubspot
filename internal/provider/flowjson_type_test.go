// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr/xattr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
)

// TestFlowJSONSemanticEquals exercises the JSON subset-equality that
// suppresses the Automation v4 API's server-injected flow defaults while
// still catching genuine edits to the graph.
func TestFlowJSONSemanticEquals(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		a, b  string
		equal bool
	}{
		{
			name:  "identical",
			a:     `{"actions":[]}`,
			b:     `{"actions":[]}`,
			equal: true,
		},
		{
			name:  "server injects top-level defaults",
			a:     `{"actions":[{"actionId":"1","type":"SINGLE_CONNECTION"}]}`,
			b:     `{"actions":[{"actionId":"1","type":"SINGLE_CONNECTION","actionTypeVersion":0}],"canEnrollFromSalesforce":false,"timeWindows":[],"blockedDates":[]}`,
			equal: true,
		},
		{
			name:  "server expands enrollment filter tree",
			a:     `{"actions":[],"enrollmentCriteria":{"type":"LIST_BASED","listFilterBranch":{"filterBranchType":"OR","filterBranches":[]}}}`,
			b:     `{"actions":[],"enrollmentCriteria":{"type":"LIST_BASED","unEnrollObjectsNotMeetingCriteria":false,"listFilterBranch":{"filterBranchType":"OR","filterBranchOperator":"OR","filterBranches":[]}}}`,
			equal: true,
		},
		{
			name:  "changed action field is a diff",
			a:     `{"actions":[{"actionId":"1","fields":{"delta":"5"}}]}`,
			b:     `{"actions":[{"actionId":"1","fields":{"delta":"10"}}]}`,
			equal: false,
		},
		{
			name:  "added action is a diff",
			a:     `{"actions":[{"actionId":"1"}]}`,
			b:     `{"actions":[{"actionId":"1"},{"actionId":"2"}]}`,
			equal: false,
		},
		{
			name:  "invalid JSON falls back to not-equal",
			a:     `{not json`,
			b:     `{"actions":[]}`,
			equal: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			a := flowJSONValue{StringValue: basetypes.NewStringValue(tc.a)}
			b := flowJSONValue{StringValue: basetypes.NewStringValue(tc.b)}
			got, diags := a.StringSemanticEquals(context.Background(), b)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if got != tc.equal {
				t.Errorf("StringSemanticEquals(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.equal)
			}
			// Semantic equality must be symmetric for subset-based comparison.
			rev, diags := b.StringSemanticEquals(context.Background(), a)
			if diags.HasError() {
				t.Fatalf("unexpected diagnostics: %v", diags)
			}
			if rev != tc.equal {
				t.Errorf("reversed StringSemanticEquals = %v, want %v", rev, tc.equal)
			}
		})
	}
}

// TestFlowJSONValidateAttribute pins plan-time validation: flow_json must be
// a JSON object, not an array, scalar, or malformed text.
func TestFlowJSONValidateAttribute(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "object ok", value: `{"actions":[]}`, wantErr: false},
		{name: "array rejected", value: `[1,2]`, wantErr: true},
		{name: "scalar rejected", value: `"actions"`, wantErr: true},
		{name: "malformed rejected", value: `{oops`, wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			v := flowJSONValue{StringValue: basetypes.NewStringValue(tc.value)}
			resp := &xattr.ValidateAttributeResponse{}
			v.ValidateAttribute(context.Background(), xattr.ValidateAttributeRequest{Path: path.Root("flow_json")}, resp)
			if resp.Diagnostics.HasError() != tc.wantErr {
				t.Errorf("ValidateAttribute(%s) error = %v, want %v", tc.value, resp.Diagnostics.HasError(), tc.wantErr)
			}
		})
	}
}
