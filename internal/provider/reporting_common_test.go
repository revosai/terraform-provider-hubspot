// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func grantSet(t *testing.T, grants ...[2]string) types.Set {
	t.Helper()
	var g []apiPermissionGrant
	for _, x := range grants {
		g = append(g, apiPermissionGrant{GrantType: x[0], GranteeID: x[1]})
	}
	s, d := grantsToSet(g)
	if d.HasError() {
		t.Fatal(d)
	}
	return s
}

func permObj(t *testing.T, typ string, view, edit types.Set) types.Object {
	t.Helper()
	o, d := types.ObjectValue(reportingPermissionsAttrTypes, map[string]attr.Value{
		"type": types.StringValue(typ), "view": view, "edit": edit,
	})
	if d.HasError() {
		t.Fatal(d)
	}
	return o
}

var nullGrants = types.SetNull(reportingGrantObjectType)

// TestDashboardPermissionsRoundTrip is a property test: for random grant
// sets, expand → wire JSON → decode → flatten yields an equal object
// (order-insensitive), and the wire shape uses the dashboard ARRAY form.
func TestDashboardPermissionsRoundTrip(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	rng := rand.New(rand.NewSource(42))
	for i := 0; i < 200; i++ {
		view := randomGrants(rng)
		edit := randomGrants(rng)
		if len(view) == 0 && len(edit) == 0 {
			view = [][2]string{{"USER", "1"}}
		}
		in := permObj(t, "SPECIFIC", grantSet(t, view...), grantSet(t, edit...))

		wire, d := dashboardPermissionsToWire(ctx, in)
		if d.HasError() {
			t.Fatal(d)
		}
		buf, _ := json.Marshal(wire)
		var decoded apiDashboardPermissions
		if err := json.Unmarshal(buf, &decoded); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(buf), `"specificPermissions":[`) {
			t.Fatalf("dashboard specificPermissions must be an array: %s", buf)
		}
		out, d := dashboardPermissionsToObject(&decoded)
		if d.HasError() {
			t.Fatal(d)
		}
		if !out.Equal(in) {
			t.Fatalf("round trip mismatch:\n in=%v\nout=%v\nwire=%s", in, out, buf)
		}
	}
}

func randomGrants(rng *rand.Rand) [][2]string {
	n := rng.Intn(4)
	seen := map[string]bool{}
	var out [][2]string
	for i := 0; i < n; i++ {
		g := [2]string{reportingGrantTypes[rng.Intn(2)], fmt.Sprint(rng.Intn(5))}
		if seen[g[0]+g[1]] {
			continue
		}
		seen[g[0]+g[1]] = true
		out = append(out, g)
	}
	return out
}

func TestReportPermissionsWireShape(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	in := permObj(t, "SPECIFIC", nullGrants, grantSet(t, [2]string{"TEAM", "9"}, [2]string{"USER", "3"}))
	wire, d := reportPermissionsToWire(ctx, in)
	if d.HasError() {
		t.Fatal(d)
	}
	buf, _ := json.Marshal(wire)
	want := `{"permissionType":"SPECIFIC","specificPermissions":{"permissionType":"EDIT","grants":[{"grantType":"TEAM","granteeId":"9"},{"grantType":"USER","granteeId":"3"}]}}`
	if string(buf) != want {
		t.Fatalf("report wire:\n got %s\nwant %s", buf, want)
	}
	out, d := reportPermissionsToObject(wire)
	if d.HasError() || !out.Equal(in) {
		t.Fatalf("round trip mismatch: %v %v", out, d)
	}

	// Non-SPECIFIC types never send grants and flatten to null grant sets.
	plain := permObj(t, "EVERYONE_VIEW", nullGrants, nullGrants)
	wire, _ = reportPermissionsToWire(ctx, plain)
	if wire.SpecificPermissions != nil {
		t.Fatalf("EVERYONE_VIEW must not carry specificPermissions: %+v", wire)
	}
	if out, _ := reportPermissionsToObject(wire); !out.Equal(plain) {
		t.Fatalf("plain round trip mismatch: %v", out)
	}
	if out, _ := reportPermissionsToObject(nil); !out.IsNull() {
		t.Fatalf("nil permissions must flatten to null")
	}
}

func TestValidatePermissions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p := path.Root("permissions")
	view := grantSet(t, [2]string{"USER", "1"})
	edit := grantSet(t, [2]string{"TEAM", "2"})

	cases := []struct {
		name      string
		obj       types.Object
		forReport bool
		wantErr   string
	}{
		{"private ok", permObj(t, "PRIVATE", nullGrants, nullGrants), false, ""},
		{"specific view ok", permObj(t, "SPECIFIC", view, nullGrants), true, ""},
		{"specific both ok on dashboard", permObj(t, "SPECIFIC", view, edit), false, ""},
		{"specific both rejected on report", permObj(t, "SPECIFIC", view, edit), true, "Too many permission levels"},
		{"same grantee at both levels", permObj(t, "SPECIFIC", view, grantSet(t, [2]string{"USER", "1"})), false, "Grantee in both view and edit"},
		{"specific without grants", permObj(t, "SPECIFIC", nullGrants, nullGrants), false, "Missing permission grants"},
		{"grants without specific", permObj(t, "EVERYONE_EDIT", view, nullGrants), false, "Grants require SPECIFIC"},
		{"unknown skipped", types.ObjectUnknown(reportingPermissionsAttrTypes), true, ""},
		{"null skipped", types.ObjectNull(reportingPermissionsAttrTypes), true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := validatePermissions(ctx, tc.obj, p, tc.forReport)
			got := ""
			if d.HasError() {
				got = d.Errors()[0].Summary()
			}
			if (tc.wantErr == "") != (got == "") || (tc.wantErr != "" && !strings.Contains(got, tc.wantErr)) {
				t.Fatalf("got %q, want %q", got, tc.wantErr)
			}
		})
	}
}

func TestCanonicalSnapshotJSON(t *testing.T) {
	t.Parallel()
	a := []byte(`{"name":"Q4","id":"7","lastViewedAt":"2026-10-01T00:00:00Z","lastViewedByUserId":"1",` +
		`"widgets":[{"reportId":"9","report":{"id":"9","lastViewedAt":"x"},"widgetLayout":{"y":0,"x":6,"width":6,"height":4}}],` +
		`"bigId":12345678901234567890}`)
	b := []byte(`{"bigId":12345678901234567890,"id":"7","name":"Q4","lastViewedAt":"2026-10-05T00:00:00Z",` +
		`"widgets":[{"widgetLayout":{"height":4,"width":6,"x":6,"y":0},"report":{"id":"9"},"reportId":"9"}]}`)
	ca, err := canonicalSnapshotJSON(a)
	if err != nil {
		t.Fatal(err)
	}
	cb, err := canonicalSnapshotJSON(b)
	if err != nil {
		t.Fatal(err)
	}
	if ca != cb {
		t.Fatalf("snapshots differ only by volatile fields / key order but rendered differently:\n%s\n---\n%s", ca, cb)
	}
	if strings.Contains(ca, "lastViewed") {
		t.Fatalf("volatile keys not stripped:\n%s", ca)
	}
	if !strings.Contains(ca, "12345678901234567890") {
		t.Fatalf("large numbers must survive verbatim:\n%s", ca)
	}
	if !strings.HasSuffix(ca, "}\n") || !strings.Contains(ca, "\n  \"id\": \"7\"") {
		t.Fatalf("expected 2-space indent and trailing newline:\n%s", ca)
	}
	if _, err := canonicalSnapshotJSON([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestWidgetsToListStableOrder(t *testing.T) {
	t.Parallel()
	ws := []apiWidget{
		{ReportID: "3", WidgetLayout: apiWidgetLayout{X: 6, Y: 4, Width: 6, Height: 4}},
		{ReportID: "1", WidgetLayout: apiWidgetLayout{X: 0, Y: 0, Width: 6, Height: 4}},
		{ReportID: "2", WidgetLayout: apiWidgetLayout{X: 6, Y: 0, Width: 6, Height: 4}},
	}
	l, d := widgetsToList(ws)
	if d.HasError() {
		t.Fatal(d)
	}
	var got []string
	for _, e := range l.Elements() {
		obj, ok := e.(types.Object)
		if !ok {
			t.Fatalf("element %T is not an object", e)
		}
		id, ok := obj.Attributes()["report_id"].(types.String)
		if !ok {
			t.Fatalf("report_id is not a string")
		}
		got = append(got, id.ValueString())
	}
	if !reflect.DeepEqual(got, []string{"1", "2", "3"}) {
		t.Fatalf("widget order = %v", got)
	}
	if ids := widgetReportIDs(append(ws, ws[0])); !reflect.DeepEqual(ids, []string{"1", "2", "3"}) {
		t.Fatalf("widgetReportIDs = %v", ids)
	}
}
