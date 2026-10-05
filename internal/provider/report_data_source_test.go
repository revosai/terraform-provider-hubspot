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

// specificReport seeds a report with a description, a different owner,
// SPECIFIC VIEW grants (reports carry a single specific permission), a tag
// and a recorded view.
func specificReport(f *fakeHubSpot, name string) string {
	id := f.seedReport(name)
	f.mutateReportingObject("reports", id, func(o *fakeReportingObject) {
		desc := "Closed-won revenue by rep"
		owner := "9"
		o.Description = &desc
		o.OwnerUserID = &owner
		o.PermissionType = "SPECIFIC"
		o.Specific = []fakeSpecificPermission{
			{PermissionType: "VIEW", Grants: []fakeGrant{{GrantType: "TEAM", GranteeID: "42"}, {GrantType: "USER", GranteeID: "7"}}},
		}
		o.LastViewedAt = "2026-10-03T08:00:00.000Z"
	})
	f.tagReportingObject("reports", id, "88", "Revenue")
	return id
}

// TestAccReportDataSource_byIDAndName looks the same report up by id and by
// exact name; raw_json is identical across lookup methods.
func TestAccReportDataSource_byIDAndName(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.seedReport("Revenue by rep (old)") // contains-match only
	id := specificReport(f, "Revenue by rep")

	config := providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_report" "by_id" {
  id = %q
}

data "hubspot_report" "by_name" {
  name = "Revenue by rep"
}
`, id)
	ds := "data.hubspot_report.by_id"
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: config,
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("id"), knownvalue.StringExact(id)),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("name"), knownvalue.StringExact("Revenue by rep")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("description"), knownvalue.StringExact("Closed-won revenue by rep")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("business_unit_id"), knownvalue.StringExact("0")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("owner_user_id"), knownvalue.StringExact("9")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("archived"), knownvalue.Bool(false)),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("archived_at"), knownvalue.Null()),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("created_at"), knownvalue.NotNull()),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("last_viewed_at"), knownvalue.StringExact("2026-10-03T08:00:00.000Z")),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("permissions"), knownvalue.ObjectExact(map[string]knownvalue.Check{
					"type": knownvalue.StringExact("SPECIFIC"),
					"view": knownvalue.SetExact([]knownvalue.Check{grantCheck("TEAM", "42"), grantCheck("USER", "7")}),
					"edit": knownvalue.Null(),
				})),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("tags"), knownvalue.ListExact([]knownvalue.Check{
					knownvalue.ObjectExact(map[string]knownvalue.Check{
						"id": knownvalue.StringExact("88"), "name": knownvalue.StringExact("Revenue"),
					}),
				})),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("raw_json"), noViewTracking),
				statecheck.ExpectKnownValue(ds, tfjsonpath.New("raw_json"),
					knownvalue.StringRegexp(regexp.MustCompile(`(?s)"specificPermissions": \{.*"tags"`))),
				statecheck.ExpectKnownValue("data.hubspot_report.by_name", tfjsonpath.New("id"), knownvalue.StringExact(id)),
				statecheck.CompareValuePairs(
					ds, tfjsonpath.New("raw_json"),
					"data.hubspot_report.by_name", tfjsonpath.New("raw_json"),
					compare.ValuesSame()),
				statecheck.CompareValuePairs(
					ds, tfjsonpath.New("tags"),
					"data.hubspot_report.by_name", tfjsonpath.New("tags"),
					compare.ValuesSame()),
			},
		}},
	})
	requireSearch(t, f, "reports", url.Values{"q": {"Revenue by rep"}, "limit": {"100"}}, "archived")
}

// TestAccReportDataSource_notFound: substring-only matches and unknown ids.
func TestAccReportDataSource_notFound(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.seedReport("Revenue by rep")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_report" "test" {
  name = "Revenue"
}
`,
				ExpectError: regexp.MustCompile(`(?s)HubSpot\s+report\s+not\s+found.*No\s+active\s+report\s+named\s+"Revenue"`),
			},
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_report" "test" {
  id = "424242"
}
`,
				ExpectError: regexp.MustCompile(`(?s)HubSpot\s+report\s+not\s+found.*424242`),
			},
		},
	})
}

// TestAccReportDataSource_multipleMatches errors listing every matching ID.
func TestAccReportDataSource_multipleMatches(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	a := f.seedReport("Pipeline")
	b := f.seedReport("Pipeline")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(srv.URL) + `
data "hubspot_report" "test" {
  name = "Pipeline"
}
`,
			ExpectError: regexp.MustCompile(fmt.Sprintf(`(?s)Ambiguous\s+HubSpot\s+report\s+name.*2\s+active\s+reports.*%s,\s+%s`, a, b)),
		}},
	})
}

// TestAccReportDataSource_archived looks up an archived report.
func TestAccReportDataSource_archived(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	id := f.seedReport("Legacy funnel")
	f.mutateReportingObject("reports", id, func(o *fakeReportingObject) {
		o.Archived = true
		o.ArchivedAt = "2026-09-29T09:00:00.000Z"
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_report" "by_name" {
  name     = "Legacy funnel"
  archived = true
}

data "hubspot_report" "by_id" {
  id       = %q
  archived = true
}
`, id),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.hubspot_report.by_name", tfjsonpath.New("id"), knownvalue.StringExact(id)),
					statecheck.ExpectKnownValue("data.hubspot_report.by_name", tfjsonpath.New("archived"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("data.hubspot_report.by_id", tfjsonpath.New("archived_at"),
						knownvalue.StringExact("2026-09-29T09:00:00.000Z")),
				},
			},
			{
				Config: providerConfig(srv.URL) + `
data "hubspot_report" "test" {
  name = "Legacy funnel"
}
`,
				ExpectError: regexp.MustCompile(`(?s)No\s+active\s+report\s+named\s+"Legacy\s+funnel"`),
			},
		},
	})
}

// TestAccReportDataSource_betaNotEnabled translates the beta 403.
func TestAccReportDataSource_betaNotEnabled(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	id := f.seedReport("Revenue by rep")
	f.disableReporting()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: providerConfig(srv.URL) + fmt.Sprintf(`
data "hubspot_report" "test" {
  id = %q
}
`, id),
			ExpectError: regexp.MustCompile(`(?s)Unable\s+to\s+read\s+HubSpot\s+report.*opted\s+into\s+the.*Reporting\s+API\s+public\s+beta`),
		}},
	})
}
