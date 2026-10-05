// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// ---------------------------------------------------------------------------
// Shared helpers for the reporting data source tests.
// ---------------------------------------------------------------------------

// noViewTracking asserts a raw_json snapshot carries no volatile
// view-tracking fields (they would churn committed snapshots).
var noViewTracking = knownvalue.StringFunc(func(s string) error {
	if strings.Contains(s, "lastViewed") {
		return fmt.Errorf("raw_json must not contain view-tracking fields, got:\n%s", s)
	}
	if !strings.HasSuffix(s, "}\n") {
		return fmt.Errorf("raw_json must be an indented object with a trailing newline, got:\n%s", s)
	}
	return nil
})

// grantCheck matches one permission grant object.
func grantCheck(grantType, id string) knownvalue.Check {
	return knownvalue.ObjectExact(map[string]knownvalue.Check{
		"type": knownvalue.StringExact(grantType),
		"id":   knownvalue.StringExact(id),
	})
}

// reportingSearches returns the query of every collection search (GET
// /dashboards or GET /reports) the fake received.
func reportingSearches(f *fakeHubSpot, collection string) []url.Values {
	var out []url.Values
	for _, r := range f.reportingRequests() {
		if r.Method != "GET" || r.Path != "/"+collection {
			continue
		}
		q, err := url.ParseQuery(r.Query)
		if err != nil {
			continue
		}
		out = append(out, q)
	}
	return out
}

// requireSearch fails unless some search on collection matched every
// expected parameter exactly (multi-valued parameters compared in order) and
// carried none of the absent parameters.
func requireSearch(t *testing.T, f *fakeHubSpot, collection string, want url.Values, absent ...string) {
	t.Helper()
	searches := reportingSearches(f, collection)
next:
	for _, q := range searches {
		for k, v := range want {
			if strings.Join(q[k], "\x00") != strings.Join(v, "\x00") {
				continue next
			}
		}
		for _, k := range absent {
			if _, ok := q[k]; ok {
				continue next
			}
		}
		return
	}
	t.Errorf("no GET /%s search with params %v (absent %v); searches sent: %v", collection, want, absent, searches)
}

// specificDashboard seeds a dashboard with description, a business unit,
// SPECIFIC VIEW+EDIT grants, a tag and a recorded view.
func specificDashboard(f *fakeHubSpot, name string, reportIDs ...string) string {
	id := f.seedDashboard(name, reportIDs...)
	f.mutateReportingObject("dashboards", id, func(o *fakeReportingObject) {
		desc := "Pipeline health for the sales team"
		o.Description = &desc
		o.BusinessUnitID = "5"
		o.PermissionType = "SPECIFIC"
		o.Specific = []fakeSpecificPermission{
			{PermissionType: "VIEW", Grants: []fakeGrant{{GrantType: "TEAM", GranteeID: "42"}}},
			{PermissionType: "EDIT", Grants: []fakeGrant{{GrantType: "USER", GranteeID: "7"}, {GrantType: "USER", GranteeID: "8"}}},
		}
		o.LastViewedAt = "2026-10-02T10:00:00.000Z"
	})
	f.tagReportingObject("dashboards", id, "77", "Exec")
	return id
}

// ---------------------------------------------------------------------------
// Tests.
// ---------------------------------------------------------------------------

// TestAccDashboardDataSource_byIDAndName looks the same dashboard up by id
// and by exact name: every typed field is populated (SPECIFIC grants, tags,
// widgets, report_ids) and raw_json is identical across lookup methods and
// free of view-tracking fields. A second dashboard whose name merely
// contains the searched name must not cause an ambiguity.
func TestAccDashboardDataSource_byIDAndName(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	r1 := f.seedReport("Deals won")  // 1001
	r2 := f.seedReport("Deals lost") // 1002
	id := specificDashboard(f, "Sales overview", r2, r1, r2)
	f.seedDashboard("Sales overview (copy)", r1)

	config := providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_dashboard" "by_id" {
  id = %q
}

data "hubspot_dashboard" "by_name" {
  name = "Sales overview"
}
`, id)
	ds := "data.hubspot_dashboard.by_id"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("id"), knownvalue.StringExact(id)),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("name"), knownvalue.StringExact("Sales overview")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("description"), knownvalue.StringExact("Pipeline health for the sales team")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("business_unit_id"), knownvalue.StringExact("5")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("owner_user_id"), knownvalue.StringExact("1")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("archived"), knownvalue.Bool(false)),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("archived_at"), knownvalue.Null()),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("created_at"), knownvalue.NotNull()),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("created_by_user_id"), knownvalue.StringExact("1")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("updated_at"), knownvalue.NotNull()),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("updated_by_user_id"), knownvalue.StringExact("1")),
				// Typed view-tracking fields are exposed; only raw_json strips them.
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("last_viewed_at"), knownvalue.StringExact("2026-10-02T10:00:00.000Z")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("last_viewed_by_user_id"), knownvalue.StringExact("1")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("permissions"), knownvalue.ObjectExact(map[string]knownvalue.Check{
					"type": knownvalue.StringExact("SPECIFIC"),
					"view": knownvalue.SetExact([]knownvalue.Check{grantCheck("TEAM", "42")}),
					"edit": knownvalue.SetExact([]knownvalue.Check{grantCheck("USER", "7"), grantCheck("USER", "8")}),
				})),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("tags"), knownvalue.ListExact([]knownvalue.Check{
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"id": knownvalue.StringExact("77"), "name": knownvalue.StringExact("Exec"),
					}),
				})),
				// Widgets in layout order (y, x): r2 was placed first.
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("widgets"), knownvalue.ListExact([]knownvalue.Check{
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"report_id": knownvalue.StringExact(r2), "x": knownvalue.Int64Exact(0), "y": knownvalue.Int64Exact(0),
						"width": knownvalue.Int64Exact(6), "height": knownvalue.Int64Exact(4),
					}),
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"report_id": knownvalue.StringExact(r1), "x": knownvalue.Int64Exact(6), "y": knownvalue.Int64Exact(0),
						"width": knownvalue.Int64Exact(6), "height": knownvalue.Int64Exact(4),
					}),
				})),
				// report_ids: sorted, distinct.
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("report_ids"), knownvalue.ListExact([]knownvalue.Check{
					knownvalue.StringExact(r1), knownvalue.StringExact(r2),
				})),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("raw_json"), noViewTracking),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("raw_json"),
					knownvalue.StringRegexp(regexp.MustCompile(`(?s)"permissions".*"specificPermissions".*"widgets"`))),

				statecheck.ExpectKnownValue("data.hubspot_dashboard.by_name", tfjsonpath.New("id"), knownvalue.StringExact(id)),
				statecheck.ExpectKnownValue("data.hubspot_dashboard.by_name", tfjsonpath.New("report_ids"), knownvalue.ListSizeExact(2)),
				statecheck.CompareValuePairs(
					ds, tfjsonpath.New("raw_json"),
					"data.hubspot_dashboard.by_name", tfjsonpath.New("raw_json"),
					compare.ValuesSame()),
				statecheck.CompareValuePairs(
					ds, tfjsonpath.New("permissions"),
					"data.hubspot_dashboard.by_name", tfjsonpath.New("permissions"),
					compare.ValuesSame()),
			},
		}},
	})

	// The by-name lookup searched with q= and then fetched by id with every
	// optional section requested.
	requireSearch(t, f, "dashboards", url.Values{"q": {"Sales overview"}, "limit": {"100"}}, "archived")
	fetched := false
	for _, r := range f.reportingRequests() {
		if r.Method == "GET" && r.Path == "/dashboards/"+id {
			q, _ := url.ParseQuery(r.Query)
			fetched = fetched || q.Get("properties") == "permissions,tags,widgets"
		}
	}
	if !fetched {
		t.Errorf("expected GET /dashboards/%s?properties=permissions,tags,widgets", id)
	}
}

// TestAccDashboardDataSource_notFound covers a name that only appears as a
// substring of names/descriptions (the q= search is a contains-match) and an
// unknown id.
func TestAccDashboardDataSource_notFound(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	id := f.seedDashboard("Sales overview")
	f.mutateReportingObject("dashboards", id, func(o *fakeReportingObject) {
		d := "Sales"
		o.Description = &d
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_dashboard" "test" {
  name = "Sales"
}
`,
				ExpectError: regexp.MustCompile(`(?s)HubSpot\s+dashboard\s+not\s+found.*No\s+active\s+dashboard\s+named\s+"Sales"`),
			},
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_dashboard" "test" {
  id = "999999"
}
`,
				ExpectError: regexp.MustCompile(`(?s)HubSpot\s+dashboard\s+not\s+found.*999999`),
			},
		},
	})
}

// TestAccDashboardDataSource_multipleMatches errors listing every matching ID.
func TestAccDashboardDataSource_multipleMatches(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	a := f.seedDashboard("Marketing")
	b := f.seedDashboard("Marketing")
	f.seedDashboard("marketing") // exact match is case-sensitive

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(srv.URL) + `
data "hubspot_dashboard" "test" {
  name = "Marketing"
}
`,
			ExpectError: regexp.MustCompile(fmt.Sprintf(`(?s)Ambiguous\s+HubSpot\s+dashboard\s+name.*2\s+active\s+dashboards.*%s,\s+%s`, a, b)),
		}},
	})
}

// TestAccDashboardDataSource_archived looks up an archived dashboard by name
// and id with archived = true; without it the archived dashboard is not found.
func TestAccDashboardDataSource_archived(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	id := f.seedDashboard("Old board")
	f.mutateReportingObject("dashboards", id, func(o *fakeReportingObject) {
		o.Archived = true
		o.ArchivedAt = "2026-09-30T12:00:00.000Z"
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_dashboard" "by_name" {
  name     = "Old board"
  archived = true
}

data "hubspot_dashboard" "by_id" {
  id       = %q
  archived = true
}
`, id),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.hubspot_dashboard.by_name", tfjsonpath.New("id"), knownvalue.StringExact(id)),
					statecheck.ExpectKnownValue("data.hubspot_dashboard.by_name", tfjsonpath.New("archived"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("data.hubspot_dashboard.by_name", tfjsonpath.New("archived_at"),
						knownvalue.StringExact("2026-09-30T12:00:00.000Z")),
					statecheck.ExpectKnownValue("data.hubspot_dashboard.by_id", tfjsonpath.New("archived"), knownvalue.Bool(true)),
					statecheck.CompareValuePairs(
						"data.hubspot_dashboard.by_id", tfjsonpath.New("raw_json"),
						"data.hubspot_dashboard.by_name", tfjsonpath.New("raw_json"),
						compare.ValuesSame()),
				},
			},
			{
				Config: providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_dashboard" "test" {
  id = %q
}
`, id),
				ExpectError: regexp.MustCompile(`(?s)HubSpot\s+dashboard\s+not\s+found.*archived\s+=\s+true`),
			},
		},
	})
	requireSearch(t, f, "dashboards", url.Values{"q": {"Old board"}, "archived": {"true"}})
}

// TestAccDashboardDataSource_validation requires exactly one of id / name.
func TestAccDashboardDataSource_validation(t *testing.T) {
	_, srv := newFakeHubSpot(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_dashboard" "test" {
  id   = "1"
  name = "x"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Invalid\s+Attribute\s+Combination`),
			},
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_dashboard" "test" {}
`,
				ExpectError: regexp.MustCompile(`(?s)Exactly\s+one\s+of\s+these\s+attributes\s+must\s+be\s+configured`),
			},
		},
	})
}

// TestAccDashboardDataSource_betaNotEnabled translates the 403 a portal that
// has not opted into the beta returns.
func TestAccDashboardDataSource_betaNotEnabled(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	id := f.seedDashboard("Sales overview")
	f.setReportingDisabled(true)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_dashboard" "test" {
  id = %q
}
`, id),
				ExpectError: regexp.MustCompile(`(?s)Unable\s+to\s+read\s+HubSpot\s+dashboard.*opted\s+into\s+the.*Reporting\s+API\s+public\s+beta`),
			},
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_dashboard" "test" {
  name = "Sales overview"
}
`,
				ExpectError: regexp.MustCompile(`(?s)Unable\s+to\s+search\s+HubSpot\s+dashboards.*opted\s+into\s+the.*Reporting\s+API\s+public\s+beta`),
			},
		},
	})
}
