// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// TestReportingDataSourceSchemas validates the four reporting data source
// schemas and checks that the singular schema and each list item have exactly
// the shared item shape (so flattened objects always fit).
func TestReportingDataSourceSchemas(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		ds    datasource.DataSource
		kind  reportingObjectKind
		items string // list attribute; "" for singular
	}{
		{NewDashboardDataSource(), dashboardKind, ""},
		{NewReportDataSource(), reportKind, ""},
		{NewDashboardsDataSource(), dashboardKind, "dashboards"},
		{NewReportsDataSource(), reportKind, "reports"},
	}
	for _, c := range cases {
		resp := &datasource.SchemaResponse{}
		c.ds.Schema(ctx, datasource.SchemaRequest{}, resp)
		if resp.Diagnostics.HasError() {
			t.Fatalf("%T schema diagnostics: %v", c.ds, resp.Diagnostics)
		}
		if d := resp.Schema.ValidateImplementation(ctx); d.HasError() {
			t.Fatalf("%T schema validation: %v", c.ds, d)
		}
		for _, frag := range []string{"public beta", "not exposed", "lastViewedAt", "intentionally not offered"} {
			if !strings.Contains(resp.Schema.MarkdownDescription, frag) {
				t.Errorf("%T description lacks %q", c.ds, frag)
			}
		}
		want := types.ObjectType{AttrTypes: reportingItemAttrTypes(c.kind)}
		root, ok := resp.Schema.Type().(types.ObjectType)
		if !ok {
			t.Fatalf("%T schema type is %T", c.ds, resp.Schema.Type())
		}
		got := root
		if c.items != "" {
			list, ok := root.AttrTypes[c.items].(types.ListType)
			if !ok {
				t.Fatalf("%T %s is not a list", c.ds, c.items)
			}
			if got, ok = list.ElemType.(types.ObjectType); !ok {
				t.Fatalf("%T %s items are not objects", c.ds, c.items)
			}
		}
		if !got.Equal(want) {
			t.Errorf("%T item type mismatch:\n got  %s\n want %s", c.ds, got, want)
		}
	}
}

func TestReportingIDLess(t *testing.T) {
	ids := []string{"1000", "b", "999", "20", "a"}
	sort.Slice(ids, func(i, j int) bool { return reportingIDLess(ids[i], ids[j]) })
	if got := strings.Join(ids, ","); got != "20,999,1000,a,b" {
		t.Fatalf("got %s", got)
	}
}

func TestReportingListFiltersID(t *testing.T) {
	ctx := context.Background()
	f := newReportingListFilters()
	if got := f.id("dashboards"); got != "dashboards" {
		t.Fatalf("empty filters id = %q", got)
	}
	owners := types.SetValueMust(types.StringType, []attr.Value{types.StringValue("3"), types.StringValue("2")})
	d := reportingCommonFilters(ctx, f, types.StringValue("sales"), owners, types.SetNull(types.StringType),
		types.SetNull(types.StringType), types.BoolValue(false))
	if d.HasError() {
		t.Fatal(d)
	}
	f.boolean("on_dashboard", "onDashboard", types.BoolValue(false), false)
	if got := f.id("reports"); got != "reports?on_dashboard=false&owner_user_ids=2%2C3&query=sales" {
		t.Fatalf("id = %q", got)
	}
	if got := f.api.Encode(); got != "onDashboard=false&ownerUserIds=2&ownerUserIds=3&q=sales" {
		t.Fatalf("api query = %q", got)
	}
}

func TestReportingItemObjectWidgets(t *testing.T) {
	ctx := context.Background()
	raw := json.RawMessage(`{"id":"5","name":"D","businessUnitId":"0","archived":false,"createdAt":"c","updatedAt":"u",
		"lastViewedAt":"v","lastViewedByUserId":"1",
		"widgets":[{"reportId":"9","widgetLayout":{"x":0,"y":0,"width":6,"height":4}},
		           {"reportId":"3","widgetLayout":{"x":6,"y":0,"width":6,"height":4}},
		           {"reportId":"9","widgetLayout":{"x":0,"y":4,"width":6,"height":4}}]}`)

	full, d := reportingItemObject(ctx, dashboardKind, raw, true)
	if d.HasError() {
		t.Fatal(d)
	}
	attrs := full.Attributes()
	if got := attrs["report_ids"].String(); got != `["3","9"]` {
		t.Errorf("report_ids = %s", got)
	}
	if widgets, ok := attrs["widgets"].(types.List); !ok || len(widgets.Elements()) != 3 {
		t.Errorf("widgets = %s, want 3 widgets", attrs["widgets"])
	}
	if !attrs["permissions"].IsNull() {
		t.Errorf("permissions should be null when the response omits them")
	}
	if !attrs["last_viewed_at"].Equal(types.StringValue("v")) {
		t.Errorf("last_viewed_at not flattened")
	}
	rawJSON, ok := attrs["raw_json"].(types.String)
	if s := rawJSON.ValueString(); !ok || strings.Contains(s, "lastViewed") || !strings.HasSuffix(s, "}\n") {
		t.Errorf("raw_json not canonical: %s", s)
	}

	slim, d := reportingItemObject(ctx, dashboardKind, raw, false)
	if d.HasError() {
		t.Fatal(d)
	}
	if !slim.Attributes()["widgets"].IsNull() || !slim.Attributes()["report_ids"].IsNull() {
		t.Errorf("widgets/report_ids must be null without include_widgets")
	}

	noWidgets, d := reportingItemObject(ctx, dashboardKind, json.RawMessage(`{"id":"6","name":"E","widgets":[]}`), true)
	if d.HasError() {
		t.Fatal(d)
	}
	if got := noWidgets.Attributes()["report_ids"].String(); got != "[]" {
		t.Errorf("empty dashboard report_ids = %s, want []", got)
	}
}
