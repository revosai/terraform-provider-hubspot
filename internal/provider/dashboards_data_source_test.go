// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"net/url"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/compare"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

func idsCheck(ids ...string) knownvalue.Check {
	checks := make([]knownvalue.Check, 0, len(ids))
	for _, id := range ids {
		checks = append(checks, knownvalue.StringExact(id))
	}
	return knownvalue.ListExact(checks)
}

// TestAccDashboardsDataSource_filters exercises every filter, asserting both
// the results and the query parameters actually sent to HubSpot.
func TestAccDashboardsDataSource_filters(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	sales := f.seedDashboard("Sales overview") // owner 1, BU 0
	f.tagReportingObject("dashboards", sales, "77", "Exec")
	mkt := f.seedDashboard("Marketing funnel")
	f.mutateReportingObject("dashboards", mkt, func(o *fakeReportingObject) {
		owner := "2"
		o.OwnerUserID = &owner
		o.BusinessUnitID = "5"
		d := "Sales-assist leads"
		o.Description = &d // q= also matches descriptions
	})
	svc := f.seedDashboard("Service desk")
	f.mutateReportingObject("dashboards", svc, func(o *fakeReportingObject) {
		owner := "3"
		o.OwnerUserID = &owner
		o.BusinessUnitID = "6"
	})
	f.tagReportingObject("dashboards", svc, "78", "Ops")
	old := f.seedDashboard("Old sales board")
	f.mutateReportingObject("dashboards", old, func(o *fakeReportingObject) {
		o.Archived = true
		o.ArchivedAt = "2026-09-30T12:00:00.000Z"
	})

	config := providerConfig(srv.URL) + `
data "hubspot_dashboards" "all" {}

data "hubspot_dashboards" "query" {
  query = "sales"
}

data "hubspot_dashboards" "owners" {
  owner_user_ids = ["3", "2"]
}

data "hubspot_dashboards" "tags" {
  tag_ids = ["77"]
}

data "hubspot_dashboards" "units" {
  business_unit_ids = ["5", "6"]
}

data "hubspot_dashboards" "archived" {
  archived = true
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.hubspot_dashboards.all", tfjsonpath.New("ids"), idsCheck(sales, mkt, svc)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.all", tfjsonpath.New("id"), knownvalue.StringExact("dashboards")),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.all", tfjsonpath.New("dashboards").AtSliceIndex(1).AtMapKey("name"),
					knownvalue.StringExact("Marketing funnel")),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.all", tfjsonpath.New("dashboards").AtSliceIndex(1).AtMapKey("owner_user_id"),
					knownvalue.StringExact("2")),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.all", tfjsonpath.New("dashboards").AtSliceIndex(0).AtMapKey("tags"),
					knownvalue.ListExact([]knownvalue.Check{knownvalue.ObjectExact(map[string]knownvalue.Check{
						"id": knownvalue.StringExact("77"), "name": knownvalue.StringExact("Exec"),
					})})),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.all", tfjsonpath.New("dashboards").AtSliceIndex(0).AtMapKey("permissions").AtMapKey("type"),
					knownvalue.StringExact("EVERYONE_EDIT")),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.all", tfjsonpath.New("dashboards").AtSliceIndex(0).AtMapKey("raw_json"),
					noViewTracking),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.query", tfjsonpath.New("ids"), idsCheck(sales, mkt)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.query", tfjsonpath.New("id"),
					knownvalue.StringExact("dashboards?query=sales")),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.owners", tfjsonpath.New("ids"), idsCheck(mkt, svc)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.tags", tfjsonpath.New("ids"), idsCheck(sales)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.units", tfjsonpath.New("ids"), idsCheck(mkt, svc)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.archived", tfjsonpath.New("ids"), idsCheck(old)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.archived", tfjsonpath.New("dashboards").AtSliceIndex(0).AtMapKey("archived"),
					knownvalue.Bool(true)),
			},
		}},
	})

	base := url.Values{"limit": {"100"}, "properties": {"permissions,tags"}}
	with := func(k string, v ...string) url.Values {
		out := url.Values{k: v}
		for bk, bv := range base {
			out[bk] = bv
		}
		return out
	}
	requireSearch(t, f, "dashboards", base, "q", "ownerUserIds", "tagIds", "businessUnitIds", "archived", "after")
	requireSearch(t, f, "dashboards", with("q", "sales"))
	// Set filters are sent as repeated (exploded) parameters, sorted.
	requireSearch(t, f, "dashboards", with("ownerUserIds", "2", "3"))
	requireSearch(t, f, "dashboards", with("tagIds", "77"))
	requireSearch(t, f, "dashboards", with("businessUnitIds", "5", "6"))
	requireSearch(t, f, "dashboards", with("archived", "true"))
}

// TestAccDashboardsDataSource_includeWidgets: widgets are only requested (and
// widgets/report_ids only populated) with include_widgets = true; with them,
// each item's raw_json equals the singular data source's raw_json.
func TestAccDashboardsDataSource_includeWidgets(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	r1 := f.seedReport("Deals won")
	r2 := f.seedReport("Deals lost")
	id := specificDashboard(f, "Sales overview", r2, r1)

	config := providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_dashboards" "slim" {}

data "hubspot_dashboards" "full" {
  include_widgets = true
}

data "hubspot_dashboard" "one" {
  id = %q
}
`, id)
	slim := tfjsonpath.New("dashboards").AtSliceIndex(0)
	full := tfjsonpath.New("dashboards").AtSliceIndex(0)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.hubspot_dashboards.slim", slim.AtMapKey("widgets"), knownvalue.Null()),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.slim", slim.AtMapKey("report_ids"), knownvalue.Null()),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.slim", slim.AtMapKey("raw_json"),
					knownvalue.StringFunc(func(s string) error {
						if regexp.MustCompile(`"widgets"`).MatchString(s) {
							return fmt.Errorf("slim raw_json should not include widgets: %s", s)
						}
						return nil
					})),
				// Permissions (SPECIFIC grants) are present on list items too.
				statecheck.ExpectKnownValue("data.hubspot_dashboards.slim", slim.AtMapKey("permissions"), knownvalue.ObjectExact(map[string]knownvalue.Check{
					"type": knownvalue.StringExact("SPECIFIC"),
					"view": knownvalue.SetExact([]knownvalue.Check{grantCheck("TEAM", "42")}),
					"edit": knownvalue.SetExact([]knownvalue.Check{grantCheck("USER", "7"), grantCheck("USER", "8")}),
				})),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.full", full.AtMapKey("widgets"), knownvalue.ListSizeExact(2)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.full", full.AtMapKey("report_ids"), idsCheck(r1, r2)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.full", full.AtMapKey("last_viewed_at"),
					knownvalue.StringExact("2026-10-02T10:00:00.000Z")),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.full", tfjsonpath.New("id"),
					knownvalue.StringExact("dashboards?include_widgets=true")),
				statecheck.CompareValuePairs(
					"data.hubspot_dashboards.full", full.AtMapKey("raw_json"),
					"data.hubspot_dashboard.one", tfjsonpath.New("raw_json"),
					compare.ValuesSame()),
				statecheck.CompareValuePairs(
					"data.hubspot_dashboards.full", full.AtMapKey("widgets"),
					"data.hubspot_dashboard.one", tfjsonpath.New("widgets"),
					compare.ValuesSame()),
			},
		}},
	})
	requireSearch(t, f, "dashboards", url.Values{"properties": {"permissions,tags"}})
	requireSearch(t, f, "dashboards", url.Values{"properties": {"permissions,tags,widgets"}})
}

// TestAccDashboardsDataSource_empty returns empty (not null) lists.
func TestAccDashboardsDataSource_empty(t *testing.T) {
	_, srv := newFakeHubSpot(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(srv.URL) + `
data "hubspot_dashboards" "none" {
  query = "nothing matches"
}
`,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.hubspot_dashboards.none", tfjsonpath.New("ids"), knownvalue.ListSizeExact(0)),
				statecheck.ExpectKnownValue("data.hubspot_dashboards.none", tfjsonpath.New("dashboards"), knownvalue.ListSizeExact(0)),
			},
		}},
	})
}

// TestAccDashboardsDataSource_betaNotEnabled translates the beta 403.
func TestAccDashboardsDataSource_betaNotEnabled(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.setReportingDisabled(true)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(srv.URL) + `
data "hubspot_dashboards" "all" {}
`,
			ExpectError: regexp.MustCompile(`(?s)Unable\s+to\s+search\s+HubSpot\s+dashboards.*opted\s+into\s+the.*Reporting\s+API\s+public\s+beta`),
		}},
	})
}
