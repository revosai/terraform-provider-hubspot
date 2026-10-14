---
page_title: "Getting started"
subcategory: ""
description: |-
  Configure the provider and create your first HubSpot property group, property, and pipeline.
---

# Getting started

This walkthrough creates a property group, a custom property inside it, and a
deal pipeline — enough to see the provider's core workflow.

## 1. Configure the provider

Pin the provider and supply a token via the environment (see the
[authentication guide](authentication.md)):

```terraform
terraform {
  required_providers {
    hubspot = {
      source  = "revosai/hubspot"
      version = "~> 0.3.1"
    }
  }
}

provider "hubspot" {}
```

```shell
export HUBSPOT_ACCESS_TOKEN="pat-..."
```

## 2. Create a property group and property

```terraform
resource "hubspot_property_group" "machine_info" {
  object_type = "contacts"
  name        = "machine_info"
  label       = "Machine information"
}

resource "hubspot_property" "warranty_status" {
  object_type = "contacts"
  name        = "warranty_status"
  label       = "Warranty status"
  type        = "enumeration"
  field_type  = "select"
  group_name  = hubspot_property_group.machine_info.name

  # List position is the display order.
  options = [
    { label = "Active", value = "active" },
    { label = "Expired", value = "expired" },
  ]
}
```

## 3. Create a pipeline

```terraform
resource "hubspot_pipeline" "sales" {
  object_type = "deals"
  label       = "Sales Pipeline"

  stages = [
    { label = "Appointment Scheduled", display_order = 0, metadata = { probability = "0.2" } },
    { label = "Closed Won", display_order = 1, metadata = { probability = "1.0" } },
  ]
}
```

## 4. Plan and apply

```shell
terraform init
terraform plan
terraform apply
```

The provider works identically under OpenTofu — substitute `tofu` for
`terraform` throughout:

```shell
tofu init
tofu plan
tofu apply
```

`terraform plan` doubles as drift detection: run it any time to see
configuration that has drifted from your portal. To stop managing an object
without changing it in HubSpot, use `terraform state rm`.

## Next steps

- [Authentication and scopes](authentication.md)
- [Destroy and archive behavior](destroy-semantics.md)
- [Promoting configuration from sandbox to production](sandbox-to-production.md)
- Resource reference in the left navigation.
