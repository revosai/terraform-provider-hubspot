// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// dashboardConfig wraps the body of a single hubspot_dashboard.test resource.
func dashboardConfig(baseURL, body string) string {
	return providerConfig(baseURL) + fmt.Sprintf(`
resource "hubspot_dashboard" "test" {
%s
}
`, body)
}

// hclStrings renders a Go string slice as an HCL list literal.
func hclStrings(ss ...string) string {
	q := make([]string, 0, len(ss))
	for _, s := range ss {
		q = append(q, fmt.Sprintf("%q", s))
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// dashboardRequestsMatching returns the recorded reporting requests with the
// given method whose sub-path matches the regexp.
func dashboardRequestsMatching(f *fakeHubSpot, method, pathRE string) []fakeReportingRequest {
	re := regexp.MustCompile(pathRE)
	var out []fakeReportingRequest
	for _, r := range f.reportingRequests() {
		if r.Method == method && re.MatchString(r.Path) {
			out = append(out, r)
		}
	}
	return out
}

// lastDashboardPatch returns the body of the most recent metadata PATCH (not
// an archive) on the dashboard.
func lastDashboardPatch(f *fakeHubSpot, id string) ([]byte, error) {
	reqs := dashboardRequestsMatching(f, "PATCH", "^/dashboards/"+id+"$")
	for i := len(reqs) - 1; i >= 0; i-- {
		if _, archive := reportingBodyField(reqs[i].Body, "archived"); !archive {
			return reqs[i].Body, nil
		}
	}
	return nil, fmt.Errorf("no metadata PATCH recorded for dashboard %s", id)
}

func dashboardStateID(s *terraform.State) (string, error) {
	rs, ok := s.RootModule().Resources["hubspot_dashboard.test"]
	if !ok {
		return "", fmt.Errorf("hubspot_dashboard.test not in state")
	}
	return rs.Primary.ID, nil
}

// checkDashboardsArchived is the CheckDestroy: every dashboard that was in
// state must still exist in the fake, archived (destroy = archive, never a
// hard delete).
func checkDashboardsArchived(f *fakeHubSpot) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for addr, rs := range s.RootModule().Resources {
			if rs.Type != "hubspot_dashboard" {
				continue
			}
			o := f.reportingObject("dashboards", rs.Primary.ID)
			if o == nil {
				return fmt.Errorf("%s: dashboard %s was hard-deleted; destroy must archive", addr, rs.Primary.ID)
			}
			if !o.Archived {
				return fmt.Errorf("%s: dashboard %s still active after destroy", addr, rs.Primary.ID)
			}
		}
		return nil
	}
}

// TestAccDashboard_lifecycle: create with report_ids → identical config plans
// empty → in-place metadata/permissions/owner update → widget add/remove by
// diff → description removal sends null → import round-trip → destroy
// archives.
func TestAccDashboard_lifecycle(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	r1 := f.seedReport("Deals created")
	r2 := f.seedReport("Deals closed")
	r3 := f.seedReport("Calls logged")

	base := func(name, extra string, reports ...string) string {
		return dashboardConfig(srv.URL, fmt.Sprintf(`
  name       = %q
  report_ids = %s
%s
`, name, hclStrings(reports...), extra))
	}
	private := `
  description = "Weekly forecast"
  permissions = { type = "PRIVATE" }
`
	specific := `
  description   = "Weekly forecast and rep activity"
  owner_user_id = "7"
  permissions = {
    type = "SPECIFIC"
    view = [{ type = "TEAM", id = "42" }, { type = "USER", id = "8" }]
    edit = [{ type = "USER", id = "7" }]
  }
`
	specificNoDesc := `
  owner_user_id = "7"
  permissions = {
    type = "SPECIFIC"
    view = [{ type = "TEAM", id = "42" }, { type = "USER", id = "8" }]
    edit = [{ type = "USER", id = "7" }]
  }
`
	addr := "hubspot_dashboard.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDashboardsArchived(f),
		Steps: []resource.TestStep{
			{
				Config: base("Sales", private, r1, r2),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("name"), knownvalue.StringExact("Sales")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("description"), knownvalue.StringExact("Weekly forecast")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("business_unit_id"), knownvalue.StringExact("0")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner_user_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("created_by_user_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("updated_by_user_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("created_at"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("updated_at"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.SetExact([]knownvalue.Check{
						knownvalue.StringExact(r1), knownvalue.StringExact(r2),
					})),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(2)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets").AtSliceIndex(0).AtMapKey("width"), knownvalue.Int64Exact(6)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("tags"), knownvalue.ListSizeExact(0)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("permissions").AtMapKey("type"), knownvalue.StringExact("PRIVATE")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("permissions").AtMapKey("view"), knownvalue.Null()),
				},
				Check: func(s *terraform.State) error {
					posts := dashboardRequestsMatching(f, "POST", "^/dashboards$")
					if len(posts) != 1 {
						return fmt.Errorf("want 1 dashboard create, got %d", len(posts))
					}
					raw, ok := reportingBodyField(posts[0].Body, "reportIdsToAdd")
					if !ok {
						return fmt.Errorf("create did not pass reportIdsToAdd: %s", posts[0].Body)
					}
					var ids []string
					_ = json.Unmarshal(raw, &ids)
					if strings.Join(ids, ",") != r1+","+r2 {
						return fmt.Errorf("reportIdsToAdd = %v, want [%s %s]", ids, r1, r2)
					}
					// Membership was already complete: no extra widget PUTs.
					if n := len(dashboardRequestsMatching(f, "PUT", "/widgets/")); n != 0 {
						return fmt.Errorf("unexpected widget PUTs after a complete reportIdsToAdd: %d", n)
					}
					// Every read asks for the optional sections.
					for _, g := range dashboardRequestsMatching(f, "GET", "^/dashboards/[0-9]+$") {
						if !strings.Contains(g.Query, "properties=") {
							return fmt.Errorf("dashboard GET without ?properties=: %q", g.Query)
						}
					}
					return nil
				},
			},
			{
				Config: base("Sales", private, r1, r2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Rename + description + owner + SPECIFIC permissions: one PATCH,
				// in place.
				Config: base("Sales leadership", specific, r1, r2),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						// Widgets are untouched by a metadata-only change.
						plancheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(2)),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("name"), knownvalue.StringExact("Sales leadership")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner_user_id"), knownvalue.StringExact("7")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("permissions"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"type": knownvalue.StringExact("SPECIFIC"),
						"view": knownvalue.SetExact([]knownvalue.Check{
							knownvalue.ObjectExact(map[string]knownvalue.Check{"type": knownvalue.StringExact("TEAM"), "id": knownvalue.StringExact("42")}),
							knownvalue.ObjectExact(map[string]knownvalue.Check{"type": knownvalue.StringExact("USER"), "id": knownvalue.StringExact("8")}),
						}),
						"edit": knownvalue.SetExact([]knownvalue.Check{
							knownvalue.ObjectExact(map[string]knownvalue.Check{"type": knownvalue.StringExact("USER"), "id": knownvalue.StringExact("7")}),
						}),
					})),
				},
				Check: func(s *terraform.State) error {
					id, err := dashboardStateID(s)
					if err != nil {
						return err
					}
					body, err := lastDashboardPatch(f, id)
					if err != nil {
						return err
					}
					for _, k := range []string{"name", "description", "ownerUserId", "permissions"} {
						if _, ok := reportingBodyField(body, k); !ok {
							return fmt.Errorf("PATCH body lacks %q: %s", k, body)
						}
					}
					if _, ok := reportingBodyField(body, "businessUnitId"); ok {
						return fmt.Errorf("PATCH body sent unchanged businessUnitId: %s", body)
					}
					o := f.reportingObject("dashboards", id)
					if o.PermissionType != "SPECIFIC" || len(o.Specific) != 2 {
						return fmt.Errorf("fake permissions = %s %+v, want SPECIFIC with VIEW and EDIT", o.PermissionType, o.Specific)
					}
					return nil
				},
			},
			{
				// Widget membership diff: add r3 (PUT), remove r1 (DELETE), keep r2.
				Config: base("Sales leadership", specific, r2, r3),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue(addr, tfjsonpath.New("widgets")),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.SetExact([]knownvalue.Check{
						knownvalue.StringExact(r2), knownvalue.StringExact(r3),
					})),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(2)),
				},
				Check: func(s *terraform.State) error {
					id, err := dashboardStateID(s)
					if err != nil {
						return err
					}
					puts := dashboardRequestsMatching(f, "PUT", "^/dashboards/"+id+"/widgets/")
					if len(puts) != 1 || puts[0].Path != "/dashboards/"+id+"/widgets/"+r3 {
						return fmt.Errorf("widget PUTs = %+v, want exactly one for report %s", puts, r3)
					}
					dels := dashboardRequestsMatching(f, "DELETE", "^/dashboards/"+id+"/widgets/")
					if len(dels) != 1 || dels[0].Path != "/dashboards/"+id+"/widgets/"+r1 {
						return fmt.Errorf("widget DELETEs = %+v, want exactly one for report %s", dels, r1)
					}
					// Metadata unchanged: no new PATCH beyond the previous step's.
					if n := len(dashboardRequestsMatching(f, "PATCH", "^/dashboards/"+id+"$")); n != 1 {
						return fmt.Errorf("widget-only change sent %d metadata PATCHes in total, want 1", n)
					}
					return nil
				},
			},
			{
				// Removing description clears it with an explicit null.
				Config: base("Sales leadership", specificNoDesc, r2, r3),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("description"), knownvalue.Null()),
				},
				Check: func(s *terraform.State) error {
					id, err := dashboardStateID(s)
					if err != nil {
						return err
					}
					body, err := lastDashboardPatch(f, id)
					if err != nil {
						return err
					}
					raw, ok := reportingBodyField(body, "description")
					if !ok || string(raw) != "null" {
						return fmt.Errorf("PATCH did not send description: null: %s", body)
					}
					if f.reportingObject("dashboards", id).Description != nil {
						return fmt.Errorf("description not cleared in the fake")
					}
					return nil
				},
			},
			{
				ResourceName:      addr,
				ImportState:       true,
				ImportStateVerify: true,
				// Imported dashboards leave widget membership unmanaged.
				ImportStateVerifyIgnore: []string{"report_ids"},
			},
		},
	})
}

// TestAccDashboard_clone: clone without cloned reports; the source's
// description (copied by HubSpot) is cleared to match the config; unrelated
// edits stay in place; changing the clone source plans a replacement.
func TestAccDashboard_clone(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	r1 := f.seedReport("Deals created")
	r2 := f.seedReport("Deals closed")
	src := f.seedDashboard("Template", r1, r2)
	other := f.seedDashboard("Other template")
	f.mutateReportingObject("dashboards", src, func(o *fakeReportingObject) {
		d := "Template description"
		o.Description = &d
	})

	cfg := func(name, source string) string {
		return dashboardConfig(srv.URL, fmt.Sprintf(`
  name                    = %q
  clone_from_dashboard_id = %q
  clone_reports           = false
  permissions             = { type = "EVERYONE_VIEW" }
`, name, source))
	}
	addr := "hubspot_dashboard.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDashboardsArchived(f),
		Steps: []resource.TestStep{
			{
				Config: cfg("Copy", src),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("description"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("clone_from_dashboard_id"), knownvalue.StringExact(src)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("clone_reports"), knownvalue.Bool(false)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListExact([]knownvalue.Check{
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"report_id": knownvalue.StringExact(r1)}),
						knownvalue.ObjectPartial(map[string]knownvalue.Check{"report_id": knownvalue.StringExact(r2)}),
					})),
				},
				Check: func(s *terraform.State) error {
					clones := dashboardRequestsMatching(f, "POST", "^/dashboards/"+src+"/clone$")
					if len(clones) != 1 {
						return fmt.Errorf("want one clone request, got %d", len(clones))
					}
					raw, _ := reportingBodyField(clones[0].Body, "cloneReports")
					if string(raw) != "false" {
						return fmt.Errorf("clone body cloneReports = %s, want false", raw)
					}
					id, err := dashboardStateID(s)
					if err != nil {
						return err
					}
					if f.reportingObject("dashboards", id).Description != nil {
						return fmt.Errorf("cloned description was not cleared")
					}
					return nil
				},
			},
			{
				Config: cfg("Copy", src),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: cfg("Copy renamed", src),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					},
				},
			},
			{
				Config:             cfg("Copy renamed", other),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionReplace),
					},
				},
			},
			{
				ResourceName:            addr,
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"clone_from_dashboard_id", "clone_reports"},
			},
		},
	})
}

// TestAccDashboard_cloneReports: clone with cloned reports — the new
// dashboard's widgets point at NEW report IDs; description and owner are
// applied by a follow-up PATCH; toggling clone_reports plans a replacement.
func TestAccDashboard_cloneReports(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	r1 := f.seedReport("Deals created")
	r2 := f.seedReport("Deals closed")
	src := f.seedDashboard("Template", r1, r2)

	cfg := func(cloneReports bool) string {
		return dashboardConfig(srv.URL, fmt.Sprintf(`
  name                    = "EMEA copy"
  description             = "EMEA pipeline"
  owner_user_id           = "9"
  clone_from_dashboard_id = %q
  clone_reports           = %t
  permissions             = { type = "EVERYONE_EDIT" }
`, src, cloneReports))
	}
	addr := "hubspot_dashboard.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDashboardsArchived(f),
		Steps: []resource.TestStep{
			{
				Config: cfg(true),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("description"), knownvalue.StringExact("EMEA pipeline")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("owner_user_id"), knownvalue.StringExact("9")),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(2)),
				},
				Check: func(s *terraform.State) error {
					clones := dashboardRequestsMatching(f, "POST", "^/dashboards/"+src+"/clone$")
					if len(clones) != 1 {
						return fmt.Errorf("want one clone request, got %d", len(clones))
					}
					raw, _ := reportingBodyField(clones[0].Body, "cloneReports")
					if string(raw) != "true" {
						return fmt.Errorf("clone body cloneReports = %s, want true", raw)
					}
					id, err := dashboardStateID(s)
					if err != nil {
						return err
					}
					o := f.reportingObject("dashboards", id)
					for _, w := range o.Widgets {
						if w.ReportID == r1 || w.ReportID == r2 {
							return fmt.Errorf("clone_reports=true widget still references source report %s", w.ReportID)
						}
					}
					return nil
				},
			},
			{
				Config: cfg(true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config:             cfg(false),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionReplace),
					},
				},
			},
		},
	})
}

// TestAccDashboard_cloneArgsAfterImport: an imported dashboard has null clone
// arguments, so adding them to the configuration afterwards is an in-place
// no-op, never a destructive replacement.
func TestAccDashboard_cloneArgsAfterImport(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedDashboard("Template")
	existing := f.seedDashboard("Existing")

	cfg := dashboardConfig(srv.URL, fmt.Sprintf(`
  name                    = "Existing"
  clone_from_dashboard_id = %q
  permissions             = { type = "EVERYONE_EDIT" }
`, src))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             cfg,
				ResourceName:       "hubspot_dashboard.test",
				ImportState:        true,
				ImportStateId:      existing,
				ImportStatePersist: true,
			},
			{
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_dashboard.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_dashboard.test", tfjsonpath.New("id"), knownvalue.StringExact(existing)),
				},
			},
		},
	})
}

// TestAccDashboard_unmanagedWidgets: with report_ids null the provider never
// touches widgets — a widget added in the UI plans empty and survives
// applies.
func TestAccDashboard_unmanagedWidgets(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	r1 := f.seedReport("Deals created")
	cfg := func(name string) string {
		return dashboardConfig(srv.URL, fmt.Sprintf(`
  name        = %q
  permissions = { type = "EVERYONE_VIEW" }
`, name))
	}
	var id string
	addr := "hubspot_dashboard.test"

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: cfg("UI arranged"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(0)),
				},
				Check: func(s *terraform.State) error {
					var err error
					id, err = dashboardStateID(s)
					if err != nil {
						return err
					}
					post := dashboardRequestsMatching(f, "POST", "^/dashboards$")[0]
					if _, ok := reportingBodyField(post.Body, "reportIdsToAdd"); ok {
						return fmt.Errorf("unmanaged create sent reportIdsToAdd: %s", post.Body)
					}
					return nil
				},
			},
			{
				PreConfig: func() {
					f.mutateReportingObject("dashboards", id, func(o *fakeReportingObject) { addWidget(o, r1) })
				},
				Config: cfg("UI arranged"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.Null()),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(1)),
				},
			},
			{
				Config: cfg("UI arranged, renamed"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
					},
				},
				Check: func(_ *terraform.State) error {
					if n := len(dashboardRequestsMatching(f, "DELETE", "/widgets/")); n != 0 {
						return fmt.Errorf("unmanaged widgets were removed (%d DELETEs)", n)
					}
					if n := len(dashboardRequestsMatching(f, "PUT", "/widgets/")); n != 0 {
						return fmt.Errorf("unmanaged widgets were added (%d PUTs)", n)
					}
					if w := f.reportingObject("dashboards", id).Widgets; len(w) != 1 || w[0].ReportID != r1 {
						return fmt.Errorf("UI-added widget not preserved: %+v", w)
					}
					return nil
				},
			},
			{
				// Adopting membership later: report_ids = [] removes the UI widget.
				Config: dashboardConfig(srv.URL, `
  name        = "UI arranged, renamed"
  permissions = { type = "EVERYONE_VIEW" }
  report_ids  = []
`),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.SetSizeExact(0)),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(0)),
				},
			},
		},
	})
}

// TestAccDashboard_reportIdsToAddVerified: reportIdsToAdd is best-effort (an
// archived report is skipped silently), so the provider must verify
// membership and fail clearly — with the dashboard persisted in state so the
// next apply converges instead of leaking an untracked dashboard.
func TestAccDashboard_reportIdsToAddVerified(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	r1 := f.seedReport("Deals created")
	r2 := f.seedReport("Archived meanwhile")
	f.mutateReportingObject("reports", r2, func(o *fakeReportingObject) { o.Archived = true })

	cfg := dashboardConfig(srv.URL, fmt.Sprintf(`
  name        = "Verified"
  permissions = { type = "PRIVATE" }
  report_ids  = %s
`, hclStrings(r1, r2)))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDashboardsArchived(f),
		Steps: []resource.TestStep{
			{
				Config:      cfg,
				ExpectError: regexp.MustCompile(`(?s)Unable to add report.*` + r2),
			},
			{
				PreConfig: func() {
					ids := f.reportingObjectsNamed("dashboards", "Verified")
					if len(ids) != 1 {
						t.Fatalf("want exactly 1 dashboard after the failed create, got %v", ids)
					}
					f.mutateReportingObject("reports", r2, func(o *fakeReportingObject) { o.Archived = false })
				},
				Config: cfg,
				Check: func(s *terraform.State) error {
					ids := f.reportingObjectsNamed("dashboards", "Verified")
					if len(ids) != 2 {
						return fmt.Errorf("want the tainted dashboard replaced (2 total), got %v", ids)
					}
					id, err := dashboardStateID(s)
					if err != nil {
						return err
					}
					for _, other := range ids {
						if other != id && !f.reportingObject("dashboards", other).Archived {
							return fmt.Errorf("tainted dashboard %s was not archived on replace", other)
						}
					}
					if n := len(f.reportingObject("dashboards", id).Widgets); n != 2 {
						return fmt.Errorf("replacement dashboard has %d widgets, want 2", n)
					}
					return nil
				},
			},
		},
	})
}

// TestAccDashboard_disappears: archived or hard-deleted out of band ⇒ removed
// from state and planned for re-creation.
func TestAccDashboard_disappears(t *testing.T) {
	for _, mode := range []string{"archived", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			f, srv := newFakeHubSpot(t)
			cfg := dashboardConfig(srv.URL, `
  name        = "Vanishing"
  permissions = { type = "PRIVATE" }
`)
			var id string
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config: cfg,
						Check: func(s *terraform.State) error {
							var err error
							id, err = dashboardStateID(s)
							return err
						},
					},
					{
						PreConfig: func() {
							if mode == "archived" {
								f.mutateReportingObject("dashboards", id, func(o *fakeReportingObject) { o.Archived = true })
							} else {
								f.deleteReportingObjectOOB("dashboards", id)
							}
						},
						Config: cfg,
						ConfigPlanChecks: resource.ConfigPlanChecks{
							PreApply: []plancheck.PlanCheck{
								plancheck.ExpectResourceAction("hubspot_dashboard.test", plancheck.ResourceActionCreate),
							},
						},
					},
				},
			})
		})
	}
}

// TestAccDashboard_validation: plan-time errors, no API calls.
func TestAccDashboard_validation(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	cases := []struct {
		body string
		want string
	}{
		{`
  name        = "x"
  permissions = { type = "SPECIFIC" }
`, `Missing permission grants`},
		{`
  name        = "x"
  permissions = { type = "PRIVATE", view = [{ type = "USER", id = "1" }] }
`, `Grants require SPECIFIC permissions`},
		{`
  name          = "x"
  clone_reports = true
  permissions   = { type = "PRIVATE" }
`, `clone_reports.*requires.*clone_from_dashboard_id`},
		{`
  name        = "x"
  permissions = { type = "PRIVATE" }
  report_ids  = ["abc"]
`, `numeric`},
		{`
  name        = "x"
  description = ""
  permissions = { type = "PRIVATE" }
`, `description`},
		{`
  name        = "x"
  permissions = { type = "PRIVATE", edit = [{ type = "GROUP", id = "1" }] }
`, `USER`},
		{`
  name        = "x"
  permissions = { type = "SPECIFIC", view = [], edit = [{ type = "USER", id = "1" }] }
`, `Empty permission grant set`},
	}
	steps := make([]resource.TestStep, 0, len(cases))
	for _, c := range cases {
		steps = append(steps, resource.TestStep{
			Config:      dashboardConfig(srv.URL, c.body),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(`(?s)` + c.want),
		})
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps:                    steps,
	})
	if n := len(f.reportingRequests()); n != 0 {
		t.Fatalf("validation errors must be plan-time; %d reporting requests were made", n)
	}
}

// TestAccDashboard_betaNotEnabled: a 403 is translated into the beta opt-in
// guidance.
func TestAccDashboard_betaNotEnabled(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.setReportingDisabled(true)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: dashboardConfig(srv.URL, `
  name        = "Beta"
  permissions = { type = "PRIVATE" }
`),
				ExpectError: regexp.MustCompile(`(?s)HTTP 403.*opted\s+into\s+the\s+Analytics\s+Reporting\s+API\s+public\s+beta`),
			},
		},
	})
}

// TestAccDashboard_importInvalidID: import IDs must be numeric.
func TestAccDashboard_importInvalidID(t *testing.T) {
	_, srv := newFakeHubSpot(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: dashboardConfig(srv.URL, `
  name        = "x"
  permissions = { type = "PRIVATE" }
`),
				ResourceName:  "hubspot_dashboard.test",
				ImportState:   true,
				ImportStateId: "sales-dashboard",
				ExpectError:   regexp.MustCompile(`(?s)Invalid import ID.*numeric`),
			},
		},
	})
}
