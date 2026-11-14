---
page_title: "Authentication and scopes"
subcategory: ""
description: |-
  How to authenticate the HubSpot provider and which scopes each resource needs.
---

# Authentication and scopes

The provider authenticates with a single `pat-…` bearer token supplied as the
provider's `access_token` argument or, preferably, via the
`HUBSPOT_ACCESS_TOKEN` environment variable. A token set explicitly in
configuration takes precedence over the environment variable.

```shell
export HUBSPOT_ACCESS_TOKEN="pat-..."
```

```terraform
provider "hubspot" {
  # access_token is read from HUBSPOT_ACCESS_TOKEN when omitted.
}
```

## Service key vs. private app

Two credential types work identically — both are `pat-…` bearer tokens:

- **Service key** (recommended) — HubSpot's successor to private apps for
  data/config integrations. Create one under **Development → Keys → Service
  keys** as a super admin. Service keys support rotation with a 7-day grace
  period and per-key request logs. Their scopes are capped at the creating
  user's permissions.
- **Legacy private app token** — create under **Settings → Integrations →
  Private Apps**. Long-lived and portal-scoped.

OAuth is intentionally **not** supported: OAuth access tokens expire every ~30
minutes and require an interactive install flow, which does not fit
non-interactive Terraform runs.

## HubSpot CLI login

A developer running Terraform from their own machine can authenticate as
themselves instead of with a shared service key. Log in with the
[HubSpot CLI](https://developers.hubspot.com/docs/developer-tooling/local-development/hubspot-cli/install-the-cli),
then set `use_hs_cli`:

```shell
hs account auth
```

```terraform
provider "hubspot" {
  use_hs_cli     = true
  hs_cli_account = "my-sandbox" # name or account ID; defaults to the CLI's default account
}
```

`hs account auth` stores a **personal access key** in `~/.hscli/config.yml`.
The provider exchanges it for short-lived access tokens the same way the CLI
does, renews them during long runs, and never writes to the CLI's config.

The token carries the scopes chosen when the personal access key was
generated (on the page `hs account auth` opens), capped by the user's
permissions. Add the scopes from the table below that your
configuration needs. HubSpot does not accept user-level tokens on every
endpoint: the pipelines API refuses them, so use a service key to manage
`hubspot_pipeline`.

`use_hs_cli` conflicts with `access_token`, and takes precedence over the
`HUBSPOT_ACCESS_TOKEN` environment variable. `HUBSPOT_USE_HS_CLI=true` and
`HUBSPOT_HS_CLI_ACCOUNT` set the same options from the environment.

## Least privilege

The provider **never requests or uses CRM record scopes** (`crm.objects.*`,
beyond the owner read used by the `hubspot_owner` data source). It cannot read
or modify your contacts, companies, or deals. Grant only the configuration
scopes you actually use — HubSpot evaluates schema scopes **per object type**.

## Required scopes by resource

| Resource / data source | Required scopes |
|---|---|
| `hubspot_property`, `hubspot_property_group` (contacts) | `crm.schemas.contacts.read`, `crm.schemas.contacts.write` |
| `hubspot_property`, `hubspot_property_group` (companies) | `crm.schemas.companies.read`, `crm.schemas.companies.write` |
| `hubspot_property`, `hubspot_property_group` (deals) | `crm.schemas.deals.read`, `crm.schemas.deals.write` |
| `hubspot_property`, `hubspot_property_group` (custom objects) | `crm.schemas.custom.read`, `crm.schemas.custom.write` |
| `hubspot_object_schema` | `crm.schemas.custom.read`, `crm.schemas.custom.write` (Enterprise tier) |
| `hubspot_pipeline` | `crm.pipelines.write` (+ the object write scope, e.g. `crm.objects.deals.write` / `crm.objects.tickets.write`) |
| `hubspot_association_label` / `data.hubspot_association_labels` | the read/write schema scopes of **both** object types (custom labels require Professional/Enterprise) |
| `hubspot_list` | `crm.lists.read`, `crm.lists.write` |
| `hubspot_workflow` / `data.hubspot_workflow` | `automation` |
| `data.hubspot_property`, `data.hubspot_properties` | `crm.schemas.{objectType}.read` |
| `data.hubspot_owner` | `crm.objects.owners.read` |
| `data.hubspot_portal` | none |

## Product-tier requirements

Scopes are not the only gate: some surfaces also require a HubSpot product
tier, and a token with the right scopes still gets a `403` on a portal whose
subscription lacks the feature.

| Surface | Account requirement | Quota notes |
|---|---|---|
| Custom properties (`hubspot_property`) | All tiers | Free portals are capped at 10 custom properties per object; paid tiers raise the cap. |
| Custom object schemas (`hubspot_object_schema`) | **Enterprise** | The number of custom object *types* is capped per tier. |
| Multiple pipelines per object (`hubspot_pipeline`) | **Professional/Enterprise** (Sales Hub for deals, Service Hub for tickets) | Pipeline count and 100-stages-per-pipeline caps vary by tier; every portal has one default pipeline on any tier. |
| Custom association labels (`hubspot_association_label`) | **Professional/Enterprise** | Label count per object-type pair is capped per tier. |
| Workflows (`hubspot_workflow`) | **Professional/Enterprise** (any Hub with automation) | Workflow count caps vary by tier. The backing Automation v4 API is a **public beta**. |
| API request volume (all resources) | All tiers | Free/Starter: 100 requests per 10s and 250,000/day; Pro/Enterprise raise both. The provider rate-limits itself and backs off on burst 429s, but fails fast when the daily quota is exhausted. |

## Interpreting 403 errors

A `403` from HubSpot has three common causes, and the provider surfaces which
one applies:

- **Missing scope** — the token lacks a scope listed above. Add it and
  re-issue the token.
- **Product tier** — the feature requires a higher subscription (custom objects
  need Enterprise; association labels and multiple pipelines need
  Professional/Enterprise).
- **Quota** — the daily API limit is exhausted. The provider fails fast rather
  than retrying.
