// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// TestAccWorkflowDataSource_byName looks a workflow up by exact name. Three
// seeded flows force the fake's 2-per-page cursor pagination, so the lookup
// must walk multiple pages.
func TestAccWorkflowDataSource_byName(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.seedFlow("Welcome new leads", "CONTACT_FLOW", "0-1", false)
	f.seedFlow("Nudge stale deals", "PLATFORM_FLOW", "0-3", true)
	f.seedFlow("Re-engage churn risks", "CONTACT_FLOW", "0-1", true)

	config := providerConfig(srv.URL) + `
data "hubspot_workflow" "test" {
  name = "Re-engage churn risks"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("flow_id"), knownvalue.StringExact("3")),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("id"), knownvalue.StringExact("3")),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("flow_type"), knownvalue.StringExact("CONTACT_FLOW")),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("object_type_id"), knownvalue.StringExact("0-1")),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("enabled"), knownvalue.Bool(true)),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("revision_id"), knownvalue.StringExact("1")),
					// The complete flow definition is exposed for backup use.
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("flow_json"), knownvalue.StringRegexp(regexp.MustCompile(`"actions"`))),
				},
			},
		},
	})
}

// TestAccWorkflowDataSource_byID looks a workflow up directly by flow ID.
func TestAccWorkflowDataSource_byID(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.seedFlow("Welcome new leads", "CONTACT_FLOW", "0-1", false)
	f.seedFlow("Nudge stale deals", "PLATFORM_FLOW", "0-3", true)

	config := providerConfig(srv.URL) + `
data "hubspot_workflow" "test" {
  flow_id = "2"
}
`
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: config,
				ConfigStateChecks: []statecheck.StateCheck{
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("name"), knownvalue.StringExact("Nudge stale deals")),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("flow_type"), knownvalue.StringExact("PLATFORM_FLOW")),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("object_type_id"), knownvalue.StringExact("0-3")),
					statecheck.ExpectKnownValue("data.hubspot_workflow.test",
						tfjsonpath.New("enabled"), knownvalue.Bool(true)),
				},
			},
		},
	})
}

// TestAccWorkflowDataSource_errors covers not-found, ambiguous-name, and
// mutually exclusive argument validation.
func TestAccWorkflowDataSource_errors(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.seedFlow("Duplicated", "CONTACT_FLOW", "0-1", false)
	f.seedFlow("Duplicated", "CONTACT_FLOW", "0-1", true)

	cases := []struct {
		name        string
		config      string
		expectError *regexp.Regexp
	}{
		{
			name:        "name not found",
			config:      `data "hubspot_workflow" "test" { name = "No such workflow" }`,
			expectError: regexp.MustCompile(`[Nn]o workflow named`),
		},
		{
			name:        "flow_id not found",
			config:      `data "hubspot_workflow" "test" { flow_id = "999" }`,
			expectError: regexp.MustCompile(`[Nn]o workflow with flow ID`),
		},
		{
			name:        "ambiguous name",
			config:      `data "hubspot_workflow" "test" { name = "Duplicated" }`,
			expectError: regexp.MustCompile(`[Aa]mbiguous`),
		},
		{
			name:        "both flow_id and name",
			config: `data "hubspot_workflow" "test" {
  flow_id = "1"
  name    = "Duplicated"
}`,
			expectError: regexp.MustCompile(`Invalid Attribute Combination`),
		},
		{
			name:        "neither flow_id nor name",
			config:      `data "hubspot_workflow" "test" {}`,
			expectError: regexp.MustCompile(`Missing Attribute Configuration`),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resource.UnitTest(t, resource.TestCase{
				ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
				Steps: []resource.TestStep{
					{
						Config:      providerConfig(srv.URL) + fmt.Sprintf("\n%s\n", tc.config),
						ExpectError: tc.expectError,
					},
				},
			})
		})
	}
}
