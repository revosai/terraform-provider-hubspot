// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccReportsDataSource_filters exercises every filter, asserting both the
// results and the query parameters actually sent to HubSpot.
func TestAccReportsDataSource_filters(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	won := f.seedReport("Deals won")
	f.tagReportingObject("reports", won, "88", "Revenue")
	lost := f.seedReport("Deals lost")
	f.mutateReportingObject("reports", lost, func(o *fakeReportingObject) {
		owner := "2"
		o.OwnerUserID = &owner
		o.BusinessUnitID = "5"
	})
	orphan := f.seedReport("Ticket backlog")
	f.mutateReportingObject("reports", orphan, func(o *fakeReportingObject) {
		owner := "3"
		o.OwnerUserID = &owner
		d := "Deals-adjacent"
		o.Description = &d
	})
	old := f.seedReport("Old deals")
	f.mutateReportingObject("reports", old, func(o *fakeReportingObject) {
		o.Archived = true
		o.ArchivedAt = "2026-09-30T12:00:00.000Z"
	})
	board := f.seedDashboard("Sales overview", lost, won)

	config := providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_reports" "all" {}

data "hubspot_reports" "query" {
  query = "deals"
}

data "hubspot_reports" "on_board" {
  dashboard_id = %q
}

data "hubspot_reports" "on_any" {
  on_dashboard = true
}

data "hubspot_reports" "on_none" {
  on_dashboard = false
}

data "hubspot_reports" "owners" {
  owner_user_ids = ["3", "2"]
}

data "hubspot_reports" "tags" {
  tag_ids = ["88"]
}

data "hubspot_reports" "units" {
  business_unit_ids = ["5"]
}

data "hubspot_reports" "archived" {
  archived = true
}
`, board)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("ids"), idsCheck(won, lost, orphan)),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("id"), knownvalue.StringExact("reports")),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("reports").AtSliceIndex(0).AtMapKey("tags"),
					knownvalue.ListExact([]knownvalue.Check{knownvalue.ObjectExact(map[string]knownvalue.Check{
						"id": knownvalue.StringExact("88"), "name": knownvalue.StringExact("Revenue"),
					})})),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("reports").AtSliceIndex(0).AtMapKey("permissions").AtMapKey("type"),
					knownvalue.StringExact("EVERYONE_EDIT")),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("reports").AtSliceIndex(2).AtMapKey("description"),
					knownvalue.StringExact("Deals-adjacent")),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("reports").AtSliceIndex(0).AtMapKey("raw_json"),
					noViewTracking),
				statecheck.ExpectKnownValue("data.hubspot_reports.query", tfjsonpath.New("ids"), idsCheck(won, lost, orphan)),
				statecheck.ExpectKnownValue("data.hubspot_reports.on_board", tfjsonpath.New("ids"), idsCheck(won, lost)),
				statecheck.ExpectKnownValue("data.hubspot_reports.on_board", tfjsonpath.New("id"),
					knownvalue.StringExact("reports?dashboard_id="+board)),
				statecheck.ExpectKnownValue("data.hubspot_reports.on_any", tfjsonpath.New("ids"), idsCheck(won, lost)),
				statecheck.ExpectKnownValue("data.hubspot_reports.on_none", tfjsonpath.New("ids"), idsCheck(orphan)),
				statecheck.ExpectKnownValue("data.hubspot_reports.owners", tfjsonpath.New("ids"), idsCheck(lost, orphan)),
				statecheck.ExpectKnownValue("data.hubspot_reports.tags", tfjsonpath.New("ids"), idsCheck(won)),
				statecheck.ExpectKnownValue("data.hubspot_reports.units", tfjsonpath.New("ids"), idsCheck(lost)),
				statecheck.ExpectKnownValue("data.hubspot_reports.archived", tfjsonpath.New("ids"), idsCheck(old)),
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
	requireSearch(t, f, "reports", base, "q", "dashboardId", "onDashboard", "ownerUserIds", "tagIds", "businessUnitIds", "archived", "after")
	requireSearch(t, f, "reports", with("q", "deals"))
	requireSearch(t, f, "reports", with("dashboardId", board))
	requireSearch(t, f, "reports", with("onDashboard", "true"))
	requireSearch(t, f, "reports", with("onDashboard", "false"))
	requireSearch(t, f, "reports", with("ownerUserIds", "2", "3"))
	requireSearch(t, f, "reports", with("tagIds", "88"))
	requireSearch(t, f, "reports", with("businessUnitIds", "5"))
	requireSearch(t, f, "reports", with("archived", "true"))
}

// TestAccReportsDataSource_pagination seeds more reports than fit on one page
// (limit=100) and proves every page is fetched.
func TestAccReportsDataSource_pagination(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	var first, last string
	for i := 1; i <= 130; i++ {
		id := f.seedReport("Report " + strconv.Itoa(i))
		if i == 1 {
			first = id
		}
		last = id
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(srv.URL) + `
data "hubspot_reports" "all" {}
`,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("ids"), knownvalue.ListSizeExact(130)),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("reports"), knownvalue.ListSizeExact(130)),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("ids").AtSliceIndex(0), knownvalue.StringExact(first)),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("ids").AtSliceIndex(129), knownvalue.StringExact(last)),
				statecheck.ExpectKnownValue("data.hubspot_reports.all", tfjsonpath.New("reports").AtSliceIndex(129).AtMapKey("name"),
					knownvalue.StringExact("Report 130")),
			},
		}},
	})
	requireSearch(t, f, "reports", url.Values{"limit": {"100"}}, "after")
	requireSearch(t, f, "reports", url.Values{"limit": {"100"}, "after": {"100"}})
}

// TestAccReportsDataSource_betaNotEnabled translates the beta 403.
func TestAccReportsDataSource_betaNotEnabled(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.setReportingDisabled(true)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(srv.URL) + `
data "hubspot_reports" "all" {}
`,
			ExpectError: regexp.MustCompile(`(?s)Unable\s+to\s+search\s+HubSpot\s+reports.*opted\s+into\s+the.*Reporting\s+API\s+public\s+beta`),
		}},
	})
}
