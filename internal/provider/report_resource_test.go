// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"encoding/json"
	"fmt"
	"net/http"
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

// reportArgs parameterizes hubspot_report test configurations. Empty strings
// omit the attribute.
type reportArgs struct {
	source      string
	name        string
	description string
	unit        string
	owner       string
	permissions string // HCL object literal; defaults to PRIVATE
}

func reportConfig(baseURL string, a reportArgs) string {
	var b strings.Builder
	fmt.Fprintf(&b, "resource \"hubspot_report\" \"test\" {\n")
	if a.source != "" {
		fmt.Fprintf(&b, "  source_report_id = %q\n", a.source)
	}
	fmt.Fprintf(&b, "  name             = %q\n", a.name)
	if a.description != "" {
		fmt.Fprintf(&b, "  description      = %q\n", a.description)
	}
	if a.unit != "" {
		fmt.Fprintf(&b, "  business_unit_id = %q\n", a.unit)
	}
	if a.owner != "" {
		fmt.Fprintf(&b, "  owner_user_id    = %q\n", a.owner)
	}
	perms := a.permissions
	if perms == "" {
		perms = `{ type = "PRIVATE" }`
	}
	fmt.Fprintf(&b, "  permissions      = %s\n}\n", perms)
	return providerConfig(baseURL) + b.String()
}

// reportRequestsSince returns the reporting requests recorded after index n
// that target a report path (method + path).
func reportRequestsSince(f *fakeHubSpot, n int) []fakeReportingRequest {
	all := f.reportingRequests()
	if n > len(all) {
		return nil
	}
	return all[n:]
}

// lastReportPatch returns the body of the most recent non-archive PATCH to
// any /reports/{id}.
func lastReportPatch(f *fakeHubSpot) ([]byte, bool) {
	reqs := f.reportingRequests()
	for i := len(reqs) - 1; i >= 0; i-- {
		r := reqs[i]
		if r.Method != http.MethodPatch || !strings.HasPrefix(r.Path, "/reports/") {
			continue
		}
		if _, ok := reportingBodyField(r.Body, "archived"); ok {
			continue
		}
		return r.Body, true
	}
	return nil, false
}

func countReportPatches(f *fakeHubSpot, id string) int {
	n := 0
	for _, r := range f.reportingRequests() {
		if r.Method == http.MethodPatch && r.Path == "/reports/"+id {
			if _, ok := reportingBodyField(r.Body, "archived"); !ok {
				n++
			}
		}
	}
	return n
}

// checkReportsArchived is a CheckDestroy asserting every hubspot_report in
// state was archived (not hard-deleted) in the fake.
func checkReportsArchived(f *fakeHubSpot) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		for addr, rs := range s.RootModule().Resources {
			if rs.Type != "hubspot_report" {
				continue
			}
			o := f.reportingObject("reports", rs.Primary.ID)
			if o == nil {
				return fmt.Errorf("%s: report %s was hard-deleted; destroy must archive", addr, rs.Primary.ID)
			}
			if !o.Archived {
				return fmt.Errorf("%s: report %s is still active after destroy", addr, rs.Primary.ID)
			}
		}
		return nil
	}
}

func bodyFieldEquals(body []byte, field, want string) error {
	raw, ok := reportingBodyField(body, field)
	if !ok {
		return fmt.Errorf("PATCH body lacks %q: %s", field, body)
	}
	if string(raw) != want {
		return fmt.Errorf("PATCH body %q = %s, want %s (body %s)", field, raw, want, body)
	}
	return nil
}

func bodyLacks(body []byte, fields ...string) error {
	for _, field := range fields {
		if _, ok := reportingBodyField(body, field); ok {
			return fmt.Errorf("PATCH body unexpectedly carries %q: %s", field, body)
		}
	}
	return nil
}

// TestAccReportResource_lifecycle: clone a UI-built template, prove an
// identical config plans empty, update metadata/permissions in place, clear
// the description (PATCH null), import, and archive on destroy.
func TestAccReportResource_lifecycle(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template: deals by stage")
	const id = "1002" // first ID the fake assigns after the seed

	base := reportArgs{source: src, name: "Pipeline by stage", description: "Quarterly pipeline"}
	renamed := base
	renamed.name = "Pipeline by stage (EMEA)"
	renamed.description = "EMEA quarterly pipeline"
	renamed.owner = "7"
	renamed.permissions = `{
    type = "SPECIFIC"
    view = [{ type = "TEAM", id = "42" }]
  }`
	editors := renamed
	editors.permissions = `{
    type = "SPECIFIC"
    edit = [{ type = "USER", id = "7" }, { type = "USER", id = "8" }]
  }`
	noDesc := editors
	noDesc.description = ""

	var mark int
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkReportsArchived(f),
		Steps: []resource.TestStep{
			{
				Config: reportConfig(srv.URL, base),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("id"), knownvalue.StringExact(id)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("source_report_id"), knownvalue.StringExact(src)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("name"), knownvalue.StringExact(base.name)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("description"), knownvalue.StringExact(base.description)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("business_unit_id"), knownvalue.StringExact("0")),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("owner_user_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("permissions").AtMapKey("type"), knownvalue.StringExact("PRIVATE")),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("tags"), knownvalue.ListExact(nil)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("created_at"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("created_by_user_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("updated_at"), knownvalue.NotNull()),
				},
				Check: func(_ *terraform.State) error {
					var clone *fakeReportingRequest
					for _, r := range f.reportingRequests() {
						if r.Method == http.MethodPost && r.Path == "/reports/"+src+"/clone" {
							clone = &r
						}
					}
					if clone == nil {
						return fmt.Errorf("no POST /reports/%s/clone recorded", src)
					}
					if err := bodyFieldEquals(clone.Body, "name", `"Pipeline by stage"`); err != nil {
						return err
					}
					if _, ok := reportingBodyField(clone.Body, "permissions"); !ok {
						return fmt.Errorf("clone body lacks permissions: %s", clone.Body)
					}
					// The follow-up PATCH sets only what the clone did not
					// already match (the description).
					body, ok := lastReportPatch(f)
					if !ok {
						return fmt.Errorf("no follow-up PATCH for the description")
					}
					if err := bodyFieldEquals(body, "description", `"Quarterly pipeline"`); err != nil {
						return err
					}
					if err := bodyLacks(body, "name", "permissions", "businessUnitId", "ownerUserId"); err != nil {
						return err
					}
					if o := f.reportingObject("reports", src); o == nil || o.Archived || o.Name != "Template: deals by stage" {
						return fmt.Errorf("source report was modified: %+v", o)
					}
					return nil
				},
			},
			{
				// Identical config plans empty.
				Config: reportConfig(srv.URL, base),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Rename, new description, new owner, SPECIFIC view
				// permissions: one in-place PATCH.
				PreConfig: func() { mark = len(f.reportingRequests()) },
				Config:    reportConfig(srv.URL, renamed),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_report.test", plancheck.ResourceActionUpdate),
						plancheck.ExpectUnknownValue("hubspot_report.test", tfjsonpath.New("updated_at")),
						plancheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("created_at"), knownvalue.NotNull()),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("id"), knownvalue.StringExact(id)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("name"), knownvalue.StringExact(renamed.name)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("owner_user_id"), knownvalue.StringExact("7")),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("permissions"), knownvalue.ObjectExact(map[string]knownvalue.Check{
						"type": knownvalue.StringExact("SPECIFIC"),
						"view": knownvalue.SetExact([]knownvalue.Check{knownvalue.ObjectExact(map[string]knownvalue.Check{
							"type": knownvalue.StringExact("TEAM"), "id": knownvalue.StringExact("42"),
						})}),
						"edit": knownvalue.Null(),
					})),
				},
				Check: func(_ *terraform.State) error {
					patches := 0
					for _, r := range reportRequestsSince(f, mark) {
						if r.Method == http.MethodPatch {
							patches++
						}
					}
					if patches != 1 {
						return fmt.Errorf("expected exactly one PATCH for the update, got %d", patches)
					}
					body, _ := lastReportPatch(f)
					if err := bodyFieldEquals(body, "name", `"Pipeline by stage (EMEA)"`); err != nil {
						return err
					}
					if err := bodyFieldEquals(body, "ownerUserId", `"7"`); err != nil {
						return err
					}
					if err := bodyFieldEquals(body, "permissions",
						`{"permissionType":"SPECIFIC","specificPermissions":{"permissionType":"VIEW","grants":[{"grantType":"TEAM","granteeId":"42"}]}}`); err != nil {
						return err
					}
					return bodyLacks(body, "archived", "businessUnitId")
				},
			},
			{
				Config: reportConfig(srv.URL, renamed),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// SPECIFIC edit with two grants; only permissions changes.
				Config: reportConfig(srv.URL, editors),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_report.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("permissions").AtMapKey("edit"),
						knownvalue.SetSizeExact(2)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("permissions").AtMapKey("view"),
						knownvalue.Null()),
				},
				Check: func(_ *terraform.State) error {
					body, _ := lastReportPatch(f)
					if err := bodyLacks(body, "name", "description", "ownerUserId"); err != nil {
						return err
					}
					o := f.reportingObject("reports", id)
					if len(o.Specific) != 1 || o.Specific[0].PermissionType != "EDIT" || len(o.Specific[0].Grants) != 2 {
						return fmt.Errorf("fake permissions not updated: %+v", o.Specific)
					}
					return nil
				},
			},
			{
				// Removing description sends an explicit JSON null.
				Config: reportConfig(srv.URL, noDesc),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_report.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("description"), knownvalue.Null()),
				},
				Check: func(_ *terraform.State) error {
					body, _ := lastReportPatch(f)
					if err := bodyFieldEquals(body, "description", "null"); err != nil {
						return err
					}
					if o := f.reportingObject("reports", id); o.Description != nil {
						return fmt.Errorf("description not cleared in the fake: %q", *o.Description)
					}
					return nil
				},
			},
			{
				Config: reportConfig(srv.URL, noDesc),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				ResourceName:      "hubspot_report.test",
				ImportState:       true,
				ImportStateVerify: true,
				// Create-time only: never readable from the API.
				ImportStateVerifyIgnore: []string{"source_report_id"},
			},
		},
	})
}

// TestAccReportResource_cloneInheritsSourceMetadata: the clone copies the
// source's description and business unit; config that differs (or omits the
// description) is reconciled by the follow-up PATCH.
func TestAccReportResource_cloneInheritsSourceMetadata(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template")
	f.mutateReportingObject("reports", src, func(o *fakeReportingObject) {
		d := "template description"
		o.Description = &d
		o.BusinessUnitID = "5"
	})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkReportsArchived(f),
		Steps: []resource.TestStep{
			{
				Config: reportConfig(srv.URL, reportArgs{source: src, name: "Clone", unit: "0", owner: "1"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("description"), knownvalue.Null()),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("business_unit_id"), knownvalue.StringExact("0")),
				},
				Check: func(_ *terraform.State) error {
					body, ok := lastReportPatch(f)
					if !ok {
						return fmt.Errorf("no follow-up PATCH recorded")
					}
					if err := bodyFieldEquals(body, "description", "null"); err != nil {
						return err
					}
					if err := bodyFieldEquals(body, "businessUnitId", `"0"`); err != nil {
						return err
					}
					// The clone is already owned by the token user.
					return bodyLacks(body, "ownerUserId", "name", "permissions")
				},
			},
			{
				Config: reportConfig(srv.URL, reportArgs{source: src, name: "Clone", unit: "0", owner: "1"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccReportResource_cloneNoFollowUpPatch: when the clone already matches
// the configuration, no PATCH is sent.
func TestAccReportResource_cloneNoFollowUpPatch(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: reportConfig(srv.URL, reportArgs{source: src, name: "Clone"}),
				Check: func(_ *terraform.State) error {
					if n := countReportPatches(f, "1002"); n != 0 {
						return fmt.Errorf("expected no follow-up PATCH, got %d", n)
					}
					return nil
				},
			},
		},
	})
}

// TestAccReportResource_importUIReport: a report built in the UI is adopted
// via import without source_report_id, plans empty, accepts a later
// source_report_id without replacement, and is archived on destroy.
func TestAccReportResource_importUIReport(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	id := f.seedReport("Built in the UI")
	other := f.seedReport("Another template")
	f.tagReportingObject("reports", id, "77", "Sales")

	cfg := reportArgs{name: "Built in the UI", permissions: `{ type = "EVERYONE_EDIT" }`}
	withSource := cfg
	withSource.source = other

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkReportsArchived(f),
		Steps: []resource.TestStep{
			{
				Config:             reportConfig(srv.URL, cfg),
				ResourceName:       "hubspot_report.test",
				ImportState:        true,
				ImportStateId:      id,
				ImportStatePersist: true,
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 {
						return fmt.Errorf("expected 1 imported state, got %d", len(states))
					}
					a := states[0].Attributes
					want := map[string]string{
						"id": id, "name": "Built in the UI", "permissions.type": "EVERYONE_EDIT",
						"owner_user_id": "1", "business_unit_id": "0", "tags.#": "1", "tags.0.name": "Sales",
					}
					for k, v := range want {
						if a[k] != v {
							return fmt.Errorf("imported %s = %q, want %q", k, a[k], v)
						}
					}
					if v, ok := a["source_report_id"]; ok && v != "" {
						return fmt.Errorf("source_report_id must stay null after import, got %q", v)
					}
					return nil
				},
			},
			{
				// No source_report_id, but the resource exists: no plan-time
				// error and nothing to change.
				Config: reportConfig(srv.URL, cfg),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Adding source_report_id to an imported report never
				// replaces it (prior value null).
				Config: reportConfig(srv.URL, withSource),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_report.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("id"), knownvalue.StringExact(id)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("source_report_id"), knownvalue.StringExact(other)),
				},
				Check: func(_ *terraform.State) error {
					if n := countReportPatches(f, id); n != 0 {
						return fmt.Errorf("source_report_id alone must not PATCH the report, got %d PATCHes", n)
					}
					return nil
				},
			},
			{
				Config: reportConfig(srv.URL, withSource),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccReportResource_importBlock: the config-driven import workflow
// (import blocks, as used with -generate-config-out) plans no create error.
func TestAccReportResource_importBlock(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	id := f.seedReport("Built in the UI")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:          reportConfig(srv.URL, reportArgs{name: "Built in the UI", permissions: `{ type = "EVERYONE_EDIT" }`}),
				ResourceName:    "hubspot_report.test",
				ImportState:     true,
				ImportStateKind: resource.ImportBlockWithID,
				ImportStateId:   id,
			},
		},
	})
}

// TestAccReportResource_sourceChangeReplaces: source_report_id is
// create-time only, so changing it on a cloned report re-clones.
func TestAccReportResource_sourceChangeReplaces(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	a := f.seedReport("Template A")
	b := f.seedReport("Template B")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkReportsArchived(f),
		Steps: []resource.TestStep{
			{
				Config: reportConfig(srv.URL, reportArgs{source: a, name: "Clone"}),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("id"), knownvalue.StringExact("1003")),
				},
			},
			{
				Config: reportConfig(srv.URL, reportArgs{source: b, name: "Clone"}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_report.test", plancheck.ResourceActionDestroyBeforeCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("id"), knownvalue.StringExact("1004")),
				},
				Check: func(_ *terraform.State) error {
					if o := f.reportingObject("reports", "1003"); o == nil || !o.Archived {
						return fmt.Errorf("replaced clone 1003 should be archived: %+v", o)
					}
					return nil
				},
			},
			{
				// Dropping source_report_id from a cloned report would
				// replace it with a from-scratch create: caught at plan time.
				Config:      reportConfig(srv.URL, reportArgs{name: "Clone"}),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)requires source_report_id.*keep\s+source_report_id`),
			},
		},
	})
}

// TestAccReportResource_missingSourceOnCreate: creating without
// source_report_id is a PLAN-time error (no API call is made).
func TestAccReportResource_missingSourceOnCreate(t *testing.T) {
	f, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      reportConfig(srv.URL, reportArgs{name: "From scratch"}),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`(?s)source_report_id.*no public report-configuration`),
			},
		},
	})
	for _, r := range f.reportingRequests() {
		if r.Method != http.MethodGet {
			t.Fatalf("plan-time error must not mutate anything; saw %s %s", r.Method, r.Path)
		}
	}
}

// TestAccReportResource_invalidPermissions: reports carry one specific
// permission level, and grants require SPECIFIC.
func TestAccReportResource_invalidPermissions(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: reportConfig(srv.URL, reportArgs{source: src, name: "Bad", permissions: `{
    type = "SPECIFIC"
    view = [{ type = "TEAM", id = "1" }]
    edit = [{ type = "USER", id = "2" }]
  }`}),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Too many permission levels for a report`),
			},
			{
				Config:      reportConfig(srv.URL, reportArgs{source: src, name: "Bad", permissions: `{ type = "SPECIFIC" }`}),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Missing permission grants`),
			},
			{
				Config: reportConfig(srv.URL, reportArgs{source: src, name: "Bad", permissions: `{
    type = "PRIVATE"
    view = [{ type = "TEAM", id = "1" }]
  }`}),
				PlanOnly:    true,
				ExpectError: regexp.MustCompile(`Grants require SPECIFIC permissions`),
			},
		},
	})
}

// TestAccReportResource_sourceNotFound: cloning a missing report fails with
// a clear error naming the source.
func TestAccReportResource_sourceNotFound(t *testing.T) {
	_, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      reportConfig(srv.URL, reportArgs{source: "424242", name: "Clone"}),
				ExpectError: regexp.MustCompile(`(?s)Source report not found.*424242`),
			},
		},
	})
}

// TestAccReportResource_disappearsArchived: a report archived out of band is
// dropped from state and planned for re-creation.
func TestAccReportResource_disappearsArchived(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template")
	cfg := reportConfig(srv.URL, reportArgs{source: src, name: "Vanishing"})

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: cfg},
			{
				PreConfig: func() {
					f.mutateReportingObject("reports", "1002", func(o *fakeReportingObject) { o.Archived = true })
				},
				Config: cfg,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_report.test", plancheck.ResourceActionCreate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("id"), knownvalue.StringExact("1003")),
				},
			},
		},
	})
}

// TestAccReportResource_disappearsDeleted: a report hard-deleted out of band
// is dropped from state.
func TestAccReportResource_disappearsDeleted(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{Config: reportConfig(srv.URL, reportArgs{source: src, name: "Vanishing"})},
			{
				PreConfig:          func() { f.deleteReportingObjectOOB("reports", "1002") },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}

// TestAccReportResource_betaNotEnabled: a 403 explains the beta opt-in.
func TestAccReportResource_betaNotEnabled(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template")
	f.disableReporting()

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      reportConfig(srv.URL, reportArgs{source: src, name: "Clone"}),
				ExpectError: regexp.MustCompile(`(?s)opted\s+into\s+the\s+Analytics\s+Reporting\s+API\s+public\s+beta`),
			},
		},
	})
}

// TestAccReportResource_importInvalidID: import IDs must be numeric.
func TestAccReportResource_importInvalidID(t *testing.T) {
	_, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:        reportConfig(srv.URL, reportArgs{name: "x", permissions: `{ type = "EVERYONE_EDIT" }`}),
				ResourceName:  "hubspot_report.test",
				ImportState:   true,
				ImportStateId: "contacts/my-report",
				ExpectError:   regexp.MustCompile(`(?s)Invalid import ID.*numeric`),
			},
		},
	})
}

// TestAccReportResource_permissionsBodyShape guards the wire shape of report
// permissions (single specificPermissions object, never an array).
func TestAccReportResource_permissionsBodyShape(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	src := f.seedReport("Template")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: reportConfig(srv.URL, reportArgs{source: src, name: "Shared", permissions: `{
    type = "SPECIFIC"
    edit = [{ type = "USER", id = "9" }, { type = "TEAM", id = "3" }]
  }`}),
				Check: func(_ *terraform.State) error {
					for _, r := range f.reportingRequests() {
						if r.Method != http.MethodPost {
							continue
						}
						raw, _ := reportingBodyField(r.Body, "permissions")
						var p struct {
							SpecificPermissions json.RawMessage `json:"specificPermissions"`
						}
						if err := json.Unmarshal(raw, &p); err != nil {
							return err
						}
						if !strings.HasPrefix(string(p.SpecificPermissions), "{") {
							return fmt.Errorf("report specificPermissions must be an object: %s", raw)
						}
						return nil
					}
					return fmt.Errorf("no clone request recorded")
				},
			},
			{
				Config: reportConfig(srv.URL, reportArgs{source: src, name: "Shared", permissions: `{
    type = "SPECIFIC"
    edit = [{ type = "TEAM", id = "3" }, { type = "USER", id = "9" }]
  }`}),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}
