// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

// Real-portal acceptance coverage for hubspot_report (Analytics Reporting
// API public beta). Same env contract and safety guard as real_api_test.go,
// plus:
//
//	HUBSPOT_TEST_SOURCE_REPORT_ID  ID of an existing report in the test
//	                               portal to clone (the API cannot create
//	                               reports from scratch). Unset => skip.
//
// The portal must have opted into the Reporting API beta and the token needs
// the reporting.full.read + reporting.full.write scopes.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

const realReportsPath = "/analytics/reporting/2027-03-beta/reports"

func init() {
	resource.AddTestSweepers("hubspot_report", &resource.Sweeper{
		Name: "hubspot_report",
		F:    sweepRealReports,
	})
}

func realReportConfig(source, name, description string) string {
	desc := ""
	if description != "" {
		desc = fmt.Sprintf("  description      = %q\n", description)
	}
	return realProviderConfig() + fmt.Sprintf(`
resource "hubspot_report" "test" {
  source_report_id = %q
  name             = %q
%s  permissions      = { type = "PRIVATE" }
}
`, source, name, desc)
}

// TestAccReal_reportLifecycle: clone the portal's template report →
// perpetual-diff guard → in-place rename + description → description removal
// → import round-trip, with CheckDestroy confirming the clone was archived
// (not deleted) and the source left untouched.
func TestAccReal_reportLifecycle(t *testing.T) {
	requireRealPortal(t)
	source := os.Getenv("HUBSPOT_TEST_SOURCE_REPORT_ID")
	if source == "" {
		t.Skip("skipping real-portal report test: set HUBSPOT_TEST_SOURCE_REPORT_ID to the ID of an " +
			"existing report in the test portal (reports can only be created by cloning)")
	}

	name := randomRealName("report_")
	renamed := name + "_v2"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkRealReportsArchived(source),
		Steps: []resource.TestStep{
			{
				Config: realReportConfig(source, name, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("name"), knownvalue.StringExact(name)),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("description"), knownvalue.Null()),
					statecheck.ExpectKnownValue("hubspot_report.test",
						tfjsonpath.New("permissions").AtMapKey("type"), knownvalue.StringExact("PRIVATE")),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("business_unit_id"), knownvalue.NotNull()),
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("created_at"), knownvalue.NotNull()),
				},
			},
			{
				Config: realReportConfig(source, name, ""),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: realReportConfig(source, renamed, "Managed by the provider acceptance tests"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_report.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("name"), knownvalue.StringExact(renamed)),
				},
			},
			{
				Config: realReportConfig(source, renamed, "Managed by the provider acceptance tests"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				Config: realReportConfig(source, renamed, ""),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_report.test", tfjsonpath.New("description"), knownvalue.Null()),
				},
			},
			{
				ResourceName:            "hubspot_report.test",
				ImportState:             true,
				ImportStateVerify:       true,
				ImportStateVerifyIgnore: []string{"source_report_id"},
			},
		},
	})
}

// realReportArchived GETs a report: active reports 404 with ?archived=true
// and archived ones 404 without it.
func realReportArchived(token, id string) (activeStatus, archivedStatus int, err error) {
	activeStatus, err = realAPIStatus(token, realReportsPath+"/"+id)
	if err != nil {
		return 0, 0, err
	}
	archivedStatus, err = realAPIStatus(token, realReportsPath+"/"+id+"?archived=true")
	return activeStatus, archivedStatus, err
}

func checkRealReportsArchived(source string) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		token := os.Getenv("HUBSPOT_ACCESS_TOKEN")
		if token == "" {
			return fmt.Errorf("HUBSPOT_ACCESS_TOKEN disappeared mid-test; cannot verify destroy")
		}
		for addr, rs := range s.RootModule().Resources {
			if rs.Type != "hubspot_report" {
				continue
			}
			active, archived, err := realReportArchived(token, rs.Primary.ID)
			if err != nil {
				return fmt.Errorf("%s: checking destroy: %w", addr, err)
			}
			if active != http.StatusNotFound || archived != http.StatusOK {
				return fmt.Errorf("%s: report %s should be archived after destroy (active GET %d, archived GET %d)",
					addr, rs.Primary.ID, active, archived)
			}
		}
		if status, err := realAPIStatus(token, realReportsPath+"/"+source); err != nil || status != http.StatusOK {
			return fmt.Errorf("source report %s must stay active (status %d, err %v)", source, status, err)
		}
		return nil
	}
}

// realAPIListReports pages the reports search for names matching q and
// returns each result's ID and name.
func realAPIListReports(token, q string) ([][2]string, error) {
	var out [][2]string
	after := ""
	for {
		query := url.Values{"limit": {"100"}, "q": {q}}
		if after != "" {
			query.Set("after", after)
		}
		path := realReportsPath + "?" + query.Encode()
		req, err := http.NewRequest(http.MethodGet, realAPIBase+path, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := realHTTPClient.Do(req)
		if err != nil {
			return nil, err
		}
		var body struct {
			Results []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"results"`
			Paging *struct {
				Next *struct {
					After string `json:"after"`
				} `json:"next"`
			} `json:"paging"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("GET %s returned status %d", path, resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, fmt.Errorf("decoding GET %s: %w", path, decodeErr)
		}
		for _, r := range body.Results {
			out = append(out, [2]string{r.ID, r.Name})
		}
		if body.Paging == nil || body.Paging.Next == nil || body.Paging.Next.After == "" {
			return out, nil
		}
		after = body.Paging.Next.After
	}
}

// realAPIArchiveReport archives a report (PATCH {archived:true}); 404 means
// it is already gone.
func realAPIArchiveReport(token, id string) error {
	path := realReportsPath + "/" + url.PathEscape(id)
	req, err := http.NewRequest(http.MethodPatch, realAPIBase+path, bytes.NewReader([]byte(`{"archived":true}`)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := realHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("PATCH %s {archived:true} returned status %d", path, resp.StatusCode)
	}
	return nil
}

func sweepRealReports(_ string) error {
	token, ok, err := sweeperEnv("hubspot_report")
	if err != nil || !ok {
		return err
	}
	reports, err := realAPIListReports(token, realTestPrefix)
	if err != nil {
		return fmt.Errorf("listing reports: %w", err)
	}
	for _, rep := range reports {
		id, name := rep[0], rep[1]
		if !strings.HasPrefix(name, realTestPrefix) {
			continue // q is a substring match on name/description
		}
		log.Printf("[INFO] sweeper hubspot_report: archiving leaked report %q (id %s)", name, id)
		if err := realAPIArchiveReport(token, id); err != nil {
			return fmt.Errorf("sweeping report %s: %w", name, err)
		}
	}
	return nil
}
