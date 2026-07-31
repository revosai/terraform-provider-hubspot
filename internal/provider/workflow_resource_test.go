// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// contactWorkflowConfig renders a CONTACT_FLOW hubspot_workflow whose single
// delay action uses the given delta, so tests can mutate the flow graph.
func contactWorkflowConfig(baseURL, name string, delayMinutes int, enabled bool) string {
	return providerConfig(baseURL) + fmt.Sprintf(`
resource "hubspot_workflow" "test" {
  name      = %q
  flow_type = "CONTACT_FLOW"
  enabled   = %t

  flow_json = jsonencode({
    startActionId = "1"
    actions = [{
      actionId     = "1"
      type         = "SINGLE_CONNECTION"
      actionTypeId = "0-1"
      fields = {
        delta     = "%d"
        time_unit = "MINUTES"
      }
    }]
    enrollmentCriteria = {
      shouldReEnroll = false
      type           = "LIST_BASED"
      listFilterBranch = {
        filterBranchType = "OR"
        filterBranches = [{
          filterBranchType = "AND"
          filters = [{
            filterType = "PROPERTY"
            property   = "email"
            operation  = { operationType = "ALL_PROPERTY", operator = "IS_KNOWN" }
          }]
        }]
      }
    }
  })
}
`, name, enabled, delayMinutes)
}

func platformWorkflowConfig(baseURL, name string) string {
	return providerConfig(baseURL) + fmt.Sprintf(`
resource "hubspot_workflow" "test" {
  name           = %q
  flow_type      = "PLATFORM_FLOW"
  object_type_id = "0-3"

  flow_json = jsonencode({
    actions = []
  })
}
`, name)
}

// TestAccWorkflow_lifecycle is the core test: create a contact workflow, prove
// an identical config plans empty (the semantic-equality type must absorb the
// server-injected flow defaults), edit the flow graph in place (revisionId
// optimistic lock: GET-then-PUT), rename and enable in place, and import.
func TestAccWorkflow_lifecycle(t *testing.T) {
	f, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contactWorkflowConfig(srv.URL, "Welcome new leads", 5, false),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("flow_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("revision_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("flow_type"), knownvalue.StringExact("CONTACT_FLOW")),
					// Server-assigned default for CONTACT_FLOW.
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("object_type_id"), knownvalue.StringExact("0-1")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("enabled"), knownvalue.Bool(false)),
				},
			},
			{
				// Identical config must plan empty despite the server injecting
				// canEnrollFromSalesforce / actionTypeVersion / filter defaults
				// on read-back.
				Config: contactWorkflowConfig(srv.URL, "Welcome new leads", 5, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
			{
				// Change the flow graph: in-place update via GET-then-PUT; the PUT
				// must carry the current revisionId and the server bumps it.
				Config: contactWorkflowConfig(srv.URL, "Welcome new leads", 10, false),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_workflow.test", plancheck.ResourceActionUpdate),
					},
				},
				Check: func(_ *terraform.State) error {
					body := string(f.lastFlowPutBody())
					if !strings.Contains(body, `"revisionId":"1"`) {
						return fmt.Errorf("flow PUT did not carry the current revisionId, body: %s", body)
					}
					return nil
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("revision_id"), knownvalue.StringExact("2")),
				},
			},
			{
				// Rename and enable: in-place update, flow_id preserved.
				Config: contactWorkflowConfig(srv.URL, "Welcome and score new leads", 10, true),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_workflow.test", plancheck.ResourceActionUpdate),
					},
				},
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("name"), knownvalue.StringExact("Welcome and score new leads")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("enabled"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("flow_id"), knownvalue.StringExact("1")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("revision_id"), knownvalue.StringExact("3")),
				},
			},
			{
				ResourceName:      "hubspot_workflow.test",
				ImportState:       true,
				ImportStateVerify: true,
				// The server returns the flow graph in its normalized (expanded)
				// form, which differs textually from the configured JSON even
				// though it is semantically equal; skip the raw string compare.
				ImportStateVerifyIgnore: []string{"flow_json"},
			},
		},
	})
}

// TestAccWorkflow_platformFlow covers a PLATFORM_FLOW on deals with an
// explicitly configured object_type_id and an empty action graph.
func TestAccWorkflow_platformFlow(t *testing.T) {
	_, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: platformWorkflowConfig(srv.URL, "Nudge stale deals"),
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("flow_type"), knownvalue.StringExact("PLATFORM_FLOW")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("object_type_id"), knownvalue.StringExact("0-3")),
					statecheck.ExpectKnownValue("hubspot_workflow.test",
						tfjsonpath.New("enabled"), knownvalue.Bool(false)),
				},
			},
			{
				Config: platformWorkflowConfig(srv.URL, "Nudge stale deals"),
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PreApply: []plancheck.PlanCheck{plancheck.ExpectEmptyPlan()},
				},
			},
		},
	})
}

// TestAccWorkflow_concurrentEditAbsorbed pins the optimistic-lock contract: a
// concurrent edit in the HubSpot UI advances the flow's revisionId, and the
// provider's GET-then-PUT update must pick up the fresh revision rather than
// failing with a stale 409.
func TestAccWorkflow_concurrentEditAbsorbed(t *testing.T) {
	f, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contactWorkflowConfig(srv.URL, "Raced", 5, false),
			},
			{
				PreConfig: func() { f.bumpFlowRevisionOOB("1") }, // now at revision 2
				Config:    contactWorkflowConfig(srv.URL, "Raced", 9, false),
				Check: func(_ *terraform.State) error {
					body := string(f.lastFlowPutBody())
					if !strings.Contains(body, `"revisionId":"2"`) {
						return fmt.Errorf("flow PUT did not carry the concurrently-bumped revisionId, body: %s", body)
					}
					if got := f.flowRevision("1"); got != 3 {
						return fmt.Errorf("flow revision after update = %d, want 3", got)
					}
					return nil
				},
			},
		},
	})
}

// TestAccWorkflow_typeForcesReplace asserts flow_type is immutable.
func TestAccWorkflow_typeForcesReplace(t *testing.T) {
	_, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contactWorkflowConfig(srv.URL, "Mutable Type", 5, false),
			},
			{
				Config:             platformWorkflowConfig(srv.URL, "Mutable Type"),
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
				ConfigPlanChecks: resource.ConfigPlanChecks{
					PostApplyPreRefresh: []plancheck.PlanCheck{
						plancheck.ExpectResourceAction("hubspot_workflow.test", plancheck.ResourceActionReplace),
					},
				},
			},
		},
	})
}

// TestAccWorkflow_disappears verifies drift handling on out-of-band delete.
func TestAccWorkflow_disappears(t *testing.T) {
	f, srv := newFakeHubSpot(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: contactWorkflowConfig(srv.URL, "Vanishing", 5, false),
			},
			{
				PreConfig:          func() { f.deleteFlowOOB("1") },
				RefreshState:       true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
