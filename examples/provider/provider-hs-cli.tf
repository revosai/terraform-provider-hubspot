# Authenticate as the developer logged in with the HubSpot CLI
# (`hs account auth`), instead of with a shared service key.
provider "hubspot" {
  use_hs_cli = true

  # The CLI account to use, by name or account ID. Defaults to the CLI's
  # default account.
  hs_cli_account = "my-sandbox"
}
