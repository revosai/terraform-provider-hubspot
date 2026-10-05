// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

// Real-portal layer for hubspot_dashboard (Analytics Reporting API public
// beta). Same env contract and safety guard as real_api_test.go; the test
// portal must have opted into the Reporting API beta and the token needs the
// reporting.full.read + reporting.full.write scopes.

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

const realReportingBase = "/analytics/reporting/2027-03-beta"

func init() {
	resource.AddTestSweepers("hubspot_dashboard", &resource.Sweeper{
		Name: "hubspot_dashboard",
		F:    sweepRealDashboards,
	})
}

// realAPIJSON performs a request with an optional JSON body against the real
// API and decodes a 2xx JSON response into out (nil discards it). It returns
// the HTTP status.
func realAPIJSON(token, method, path string, body, out any) (int, error) {
	var rd *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, realAPIBase+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := realHTTPClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if out != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decoding %s %s: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

// realAPIFirstReportID returns the ID of any active report in the test
// portal ("" if none): the API cannot create reports, so the widget steps
// borrow an existing one (adding it to a test dashboard does not modify it).
func realAPIFirstReportID(token string) (string, error) {
	var page struct {
		Results []struct {
			ID string `json:"id"`
		} `json:"results"`
	}
	status, err := realAPIJSON(token, http.MethodGet, realReportingBase+"/reports?limit=1", nil, &page)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("GET reports returned status %d (is the portal opted into the Reporting API beta?)", status)
	}
	if len(page.Results) == 0 {
		return "", nil
	}
	return page.Results[0].ID, nil
}

func realDashboardConfig(name, body string) string {
	return realProviderConfig() + fmt.Sprintf(`
resource "hubspot_dashboard" "test" {
  name = %q
%s
}
`, name, body)
}

// TestAccReal_dashboardLifecycle runs the dashboard lifecycle against the
// live Reporting API beta: create → perpetual-diff guard → in-place metadata
// and permissions update → (when the portal has any report) managed widget
// add/remove → import round-trip, with CheckDestroy confirming the dashboard
// was archived, not deleted.
func TestAccReal_dashboardLifecycle(t *testing.T) {
	requireRealPortal(t)
	token := os.Getenv("HUBSPOT_ACCESS_TOKEN")

	reportID, err := realAPIFirstReportID(token)
	if err != nil {
		t.Fatalf("looking up a report for the widget steps: %v", err)
	}

	name := randomRealName("dash_")
	renamed := name + "_v2"
	addr := "hubspot_dashboard.test"
	v1 := `
  description = "Created by the terraform-provider-hubspot acceptance tests"
  permissions = { type = "PRIVATE" }
`
	v2 := `
  permissions = { type = "EVERYONE_VIEW" }
`

	steps := []resource.TestStep{
		{
			Config: realDashboardConfig(name, v1),
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(addr, tfjsonpath.New("id"), knownvalue.NotNull()),
				statecheck.ExpectKnownValue(addr, tfjsonpath.New("name"), knownvalue.StringExact(name)),
				statecheck.ExpectKnownValue(addr, tfjsonpath.New("permissions").AtMapKey("type"), knownvalue.StringExact("PRIVATE")),
				statecheck.ExpectKnownValue(addr, tfjsonpath.New("business_unit_id"), knownvalue.NotNull()),
				statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.Null()),
			},
		},
		{
			// The core reason this layer exists: live-API normalization the
			// fake may model imperfectly must not cause a perpetual diff.
			Config: realDashboardConfig(name, v1),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
			},
		},
		{
			// Rename, clear description, widen permissions: in place.
			Config: realDashboardConfig(renamed, v2),
			ConfigPlanChecks: resource.ConfigPlanChecks{
				PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction(addr, plancheck.ResourceActionUpdate),
				},
			},
			ConfigStateChecks: []statecheck.StateCheck{
				statecheck.ExpectKnownValue(addr, tfjsonpath.New("name"), knownvalue.StringExact(renamed)),
				statecheck.ExpectKnownValue(addr, tfjsonpath.New("description"), knownvalue.Null()),
			},
		},
	}
	if reportID != "" {
		steps = append(steps,
			resource.TestStep{
				Config: realDashboardConfig(renamed, v2+fmt.Sprintf("  report_ids = [%q]\n", reportID)),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("report_ids"), knownvalue.SetExact([]knownvalue.Check{
						knownvalue.StringExact(reportID),
					})),
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(1)),
				},
			},
			resource.TestStep{
				Config: realDashboardConfig(renamed, v2+"  report_ids = []\n"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue(addr, tfjsonpath.New("widgets"), knownvalue.ListSizeExact(0)),
				},
			},
		)
	} else {
		t.Log("test portal has no reports; skipping the widget steps")
	}
	steps = append(steps, resource.TestStep{
		ResourceName:            addr,
		ImportState:             true,
		ImportStateVerify:       true,
		ImportStateVerifyIgnore: []string{"report_ids"},
	})

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkRealDashboardsArchived,
		Steps:                    steps,
	})
}

// checkRealDashboardsArchived asserts every hubspot_dashboard in state was
// archived by destroy: the active GET 404s and the archived GET finds it.
func checkRealDashboardsArchived(s *terraform.State) error {
	token := os.Getenv("HUBSPOT_ACCESS_TOKEN")
	if token == "" {
		return fmt.Errorf("HUBSPOT_ACCESS_TOKEN disappeared mid-test; cannot verify destroy")
	}
	for addr, rs := range s.RootModule().Resources {
		if rs.Type != "hubspot_dashboard" {
			continue
		}
		p := realReportingBase + "/dashboards/" + url.PathEscape(rs.Primary.ID)
		status, err := realAPIStatus(token, p)
		if err != nil {
			return fmt.Errorf("%s: checking destroy: %w", addr, err)
		}
		if status != http.StatusNotFound {
			return fmt.Errorf("%s: dashboard %s still active after destroy (status %d)", addr, rs.Primary.ID, status)
		}
		status, err = realAPIStatus(token, p+"?archived=true")
		if err != nil {
			return fmt.Errorf("%s: checking archived state: %w", addr, err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("%s: dashboard %s not found with archived=true (status %d); destroy must archive",
				addr, rs.Primary.ID, status)
		}
	}
	return nil
}

// sweepRealDashboards archives leaked active tf_acc_test_* dashboards.
func sweepRealDashboards(_ string) error {
	token, ok, err := sweeperEnv("hubspot_dashboard")
	if err != nil || !ok {
		return err
	}
	type dash struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	var leaked []dash
	after := ""
	for {
		q := url.Values{"limit": {"100"}, "q": {realTestPrefix}}
		if after != "" {
			q.Set("after", after)
		}
		var page struct {
			Results []dash `json:"results"`
			Paging  *struct {
				Next *struct {
					After string `json:"after"`
				} `json:"next"`
			} `json:"paging"`
		}
		path := realReportingBase + "/dashboards?" + q.Encode()
		status, err := realAPIJSON(token, http.MethodGet, path, nil, &page)
		if err != nil {
			return fmt.Errorf("listing dashboards: %w", err)
		}
		if status != http.StatusOK {
			return fmt.Errorf("GET %s returned status %d", path, status)
		}
		for _, d := range page.Results {
			if strings.HasPrefix(d.Name, realTestPrefix) {
				leaked = append(leaked, d)
			}
		}
		if page.Paging == nil || page.Paging.Next == nil || page.Paging.Next.After == "" {
			break
		}
		after = page.Paging.Next.After
	}
	for _, d := range leaked {
		log.Printf("[INFO] sweeper hubspot_dashboard: archiving leaked dashboard %q (id %s)", d.Name, d.ID)
		p := realReportingBase + "/dashboards/" + url.PathEscape(d.ID)
		status, err := realAPIJSON(token, http.MethodPatch, p, map[string]any{"archived": true}, nil)
		if err != nil {
			return fmt.Errorf("archiving dashboard %s: %w", d.ID, err)
		}
		if status >= 300 && status != http.StatusNotFound {
			return fmt.Errorf("PATCH %s {archived:true} returned status %d", p, status)
		}
	}
	return nil
}
