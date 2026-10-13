terraform {
  required_providers {
    hubspot = {
      source  = "revosai/hubspot"
      version = "~> 0.3.1"
    }
  }
}

provider "hubspot" {
  # The service key or private app access token is read from the
  # HUBSPOT_ACCESS_TOKEN environment variable.
}
