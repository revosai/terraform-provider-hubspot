// Copyright (c) terraform-provider-hubspot authors
// SPDX-License-Identifier: MPL-2.0

package provider_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/knownvalue"
	"github.com/hashicorp/terraform-plugin-testing/statecheck"
	"github.com/hashicorp/terraform-plugin-testing/tfjsonpath"
)

// writeHSCLIConfig gives the test a home directory holding a HubSpot CLI
// config with two personal-access-key accounts, the fake's portal (123456)
// named "sandbox" and the default.
func writeHSCLIConfig(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("HUBSPOT_ACCESS_TOKEN", "")
	t.Setenv("HUBSPOT_USE_HS_CLI", "")
	t.Setenv("HUBSPOT_HS_CLI_ACCOUNT", "")
	dir := filepath.Join(home, ".hscli")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := `defaultAccount: 123456
accounts:
  - name: production
    accountId: 654321
    authType: personalaccesskey
    personalAccessKey: production-key
  - name: sandbox
    accountId: 123456
    authType: personalaccesskey
    personalAccessKey: sandbox-key
`
	if err := os.WriteFile(filepath.Join(dir, "config.yml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
}

func portalChecks() []statecheck.StateCheck {
	return []statecheck.StateCheck{
		statecheck.ExpectKnownValue("data.hubspot_portal.current",
			tfjsonpath.New("portal_id"), knownvalue.StringExact("123456")),
	}
}

func TestAccProvider_hsCLI(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.acceptHSCLIKey("sandbox-key")
	writeHSCLIConfig(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: fmt.Sprintf(`
provider "hubspot" {
  use_hs_cli     = true
  hs_cli_account = "sandbox"
  base_url       = %q
}
data "hubspot_portal" "current" {}
`, srv.URL),
				ConfigStateChecks: portalChecks(),
			},
		},
	})
}

func TestAccProvider_hsCLIFromEnvironment(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.acceptHSCLIKey("sandbox-key")
	writeHSCLIConfig(t)
	// A token in the environment does not override an explicit use_hs_cli.
	t.Setenv("HUBSPOT_ACCESS_TOKEN", "pat-na1-00000000-0000-0000-0000-000000000000")
	t.Setenv("HUBSPOT_USE_HS_CLI", "true")
	t.Setenv("HUBSPOT_BASE_URL", srv.URL)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				// The CLI's default account is the fake's portal.
				Config:            `data "hubspot_portal" "current" {}`,
				ConfigStateChecks: portalChecks(),
			},
		},
	})
}

func TestAccProvider_hsCLIErrors(t *testing.T) {
	f, srv := newFakeHubSpot(t)
	f.acceptHSCLIKey("sandbox-key")
	writeHSCLIConfig(t)

	config := func(attrs string) string {
		return fmt.Sprintf(`
provider "hubspot" {
  base_url = %q
  %s
}
data "hubspot_portal" "current" {}
`, srv.URL, attrs)
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:      config(`use_hs_cli = true` + "\n" + `access_token = "pat-na1-00000000-0000-0000-0000-000000000000"`),
				ExpectError: regexp.MustCompile(`Conflicting HubSpot credentials`),
			},
			{
				Config:      config(`use_hs_cli = true` + "\n" + `hs_cli_account = "staging"`),
				ExpectError: regexp.MustCompile(`no account "staging"`),
			},
			{
				// The fake accepts only the sandbox account's key.
				Config:      config(`use_hs_cli = true` + "\n" + `hs_cli_account = "production"`),
				ExpectError: regexp.MustCompile(`hs account auth`),
			},
			{
				Config:      config(`hs_cli_account = "sandbox"`),
				ExpectError: regexp.MustCompile(`hs_cli_account is set but use_hs_cli is not`),
			},
		},
	})
}
