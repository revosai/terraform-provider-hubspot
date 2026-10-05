# Terraform Provider for HubSpot

[![Terraform Registry](https://img.shields.io/badge/dynamic/json?url=https%3A%2F%2Fregistry.terraform.io%2Fv1%2Fproviders%2Frevosai%2Fhubspot&query=%24.version&label=terraform%20registry&color=7B42BC&logo=terraform)](https://registry.terraform.io/providers/revosai/hubspot/latest)
[![Tests](https://github.com/revosai/terraform-provider-hubspot/actions/workflows/test.yml/badge.svg)](https://github.com/revosai/terraform-provider-hubspot/actions/workflows/test.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/revosai/terraform-provider-hubspot)](https://goreportcard.com/report/github.com/revosai/terraform-provider-hubspot)
[![Go Version](https://img.shields.io/github/go-mod/go-version/revosai/terraform-provider-hubspot)](https://go.dev/)
[![License: MPL 2.0](https://img.shields.io/badge/license-MPL%202.0-blue.svg)](./LICENSE)

Manage your HubSpot portal configuration as code. This provider targets the
HubSpot **configuration plane** — the structural setup of your portal
(properties, groups, pipelines, custom object schemas, association labels,
lists) — deliberately **not** CRM records (contacts, companies, deals)
themselves. It works identically with Terraform and OpenTofu.

**Why?** HubSpot admins have no good answer for promotion, drift, and audit:
sandbox→production deploys can't promote *edits* to existing assets, the
audit log is Enterprise-only with a 30-day window, and replicating
configuration across portals is brittle because HubSpot IDs are
portal-specific. Terraform solves all three: the same module applied to
several portals, `plan` as drift detection, and git history as the audit
trail.

## Supported resources

| Resource | Manages | Destroy behavior | Import ID |
|---|---|---|---|
| [`hubspot_property_group`](./docs/resources/property_group.md) | CRM property groups — the named sections that organize properties in the HubSpot UI | Deletes the group | `{object_type}/{name}` |
| [`hubspot_property`](./docs/resources/property.md) | Custom CRM property definitions on any object type, including enumeration options (list order = display order) and all field types | **Archives** the property — HubSpot reserves the name for ~90 days ("name purgatory"); the provider reports an actionable error if you recreate the name too soon | `{object_type}/{name}` |
| [`hubspot_pipeline`](./docs/resources/pipeline.md) | Deal, ticket, and custom-object pipelines with inline stages (matched by `stage_id`, so reorders/renames are in-place updates, not destroy-create) | Deletes the pipeline, guarded against orphaning records; the default pipeline is adopt-via-import only | `{object_type}/{pipeline_id}` |
| [`hubspot_object_schema`](./docs/resources/object_schema.md) | Custom object definitions (Enterprise tier): labels, display/required/searchable properties, bootstrap properties, associations | **Deletes** the object type and all its records — gated behind `force_delete = true`; two-phase archive-then-purge | `{object_type_id}` (e.g. `2-12345`) |
| [`hubspot_association_label`](./docs/resources/association_label.md) | Custom association labels between two object types (Pro/Ent), paired or unpaired | **Deletes** the label — removing it from every record association that uses it | `{from_object_type}/{to_object_type}/{type_id}` |
| [`hubspot_list`](./docs/resources/list.md) | CRM lists (`MANUAL`/`DYNAMIC`/`SNAPSHOT`) — the list *definition* only, never membership; `filter_branch` is compared semantically to absorb server-injected defaults | **Archives** the list (restorable within 90 days) | `{list_id}` |
| [`hubspot_workflow`](./docs/resources/workflow.md) | Workflows via the Automation v4 **beta** API — raw JSON `flow_json` graph compared semantically, `revisionId` optimistic locking handled GET-then-PUT | **Deletes** the workflow (moves to HubSpot's deleted state, restorable in the UI within 90 days) | `{flow_id}` |
| [`hubspot_dashboard`](./docs/resources/dashboard.md) | Reporting dashboards via the Analytics Reporting **beta** API — metadata, permissions, exact widget membership (`report_ids`), create-time cloning; widget layout and tags are read-only | **Archives** the dashboard (restorable) | `{dashboard_id}` |
| [`hubspot_report`](./docs/resources/report.md) | Reports via the Analytics Reporting **beta** API — created by cloning a UI-built template report (no from-scratch create in the API), metadata and permissions; the report's query/visualization stays UI-managed | **Archives** the report (restorable) — also for imported reports; use a `removed` block to stop managing without archiving | `{report_id}` |

All nine resources support the full lifecycle: create, in-place update,
replace on immutable-field changes (planned at plan time via `RequiresReplace`,
with data-loss warnings in the docs), drift detection (out-of-band deletions
are re-created, out-of-band edits are corrected), and `terraform import`.

### Data sources

| Data source | Looks up |
|---|---|
| [`hubspot_property`](./docs/data-sources/property.md) | Any property (including HubSpot-defined defaults like `lifecyclestage`) by `object_type` + `name` |
| [`hubspot_properties`](./docs/data-sources/properties.md) | Every property on an `object_type` (filter with HCL, e.g. custom-only) — the list companion to the singular source |
| [`hubspot_owner`](./docs/data-sources/owner.md) | A CRM owner by `email` or `owner_id` — the `id` output feeds `hubspot_owner_id` property values |
| [`hubspot_portal`](./docs/data-sources/portal.md) | The authenticated portal's ID, account type, time zone, currency, and UI domain |
| [`hubspot_pipeline`](./docs/data-sources/pipeline.md) | A pipeline (and its stages) by `object_type` + `pipeline_id` — e.g. to reference the built-in `default` pipeline's stage IDs |
| [`hubspot_object_schema`](./docs/data-sources/object_schema.md) | A custom object schema by name, resolving its portal-specific `object_type_id` (`2-XXXX`) |
| [`hubspot_association_labels`](./docs/data-sources/association_labels.md) | Every association label between an object-type pair — resolve portal-specific `type_id`s by name |
| [`hubspot_workflow`](./docs/data-sources/workflow.md) | A workflow by `flow_id` or exact `name` — exposes the complete flow definition JSON, e.g. for point-in-time backups |
| [`hubspot_dashboard`](./docs/data-sources/dashboard.md) / [`hubspot_dashboards`](./docs/data-sources/dashboards.md) | A dashboard by `id` or exact `name`, or a filtered search — widgets, permissions, tags, plus `raw_json` snapshots for committing reporting configuration to git |
| [`hubspot_report`](./docs/data-sources/report.md) / [`hubspot_reports`](./docs/data-sources/reports.md) | A report by `id` or exact `name`, or a filtered search (e.g. every report on a dashboard) — with `raw_json` snapshots |

### Example

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

More runnable examples live in [`examples/`](./examples/); full argument
reference in [`docs/`](./docs/) (rendered on the registries once published).

### Roadmap & scope

The full public roadmap lives in [`ROADMAP.md`](./ROADMAP.md). In short:

- **Shipped:** properties, property groups, pipelines, custom object schemas, association labels, lists, workflows (Automation v4 beta API); data sources for property, properties (list), owner, portal, pipeline, object schema, association labels, and workflow
- **Next (rest of Phase 3):** list membership (static-list fixtures), public-app webhooks
- **Phase 5:** users, `hubspot_crm_record` fixture escape hatch
- **Phase 6:** v1.0 hardening + publication to both registries

Beta HubSpot APIs are in scope (documented as beta-backed, raw-JSON schemas
while the API moves). Some admin features **cannot** be managed by any
provider because HubSpot has no public API for them at all — see the
[out-of-scope table](./ROADMAP.md#out-of-scope--hubspot-has-no-public-api)
in the roadmap.

## Requirements

- [Terraform](https://developer.hashicorp.com/terraform/downloads) >= 1.0 or [OpenTofu](https://opentofu.org/docs/intro/install/) >= 1.6
- [Go](https://golang.org/doc/install) 1.25+ (only to build the provider from source)

## Using the provider

```terraform
terraform {
  required_providers {
    hubspot = {
      source = "revosai/hubspot"
    }
  }
}

provider "hubspot" {
  # Authentication uses a HubSpot service key (or legacy private
  # app token), read from the HUBSPOT_ACCESS_TOKEN environment variable.
}
```

### Authentication & scopes

Create a [service key](https://developers.hubspot.com/docs/apps/developer-platform/build-apps/authentication/account-service-keys)
in your HubSpot portal (**Development → Keys → Service keys**; requires a
super admin — service keys are HubSpot's successor to legacy private apps
and support rotation with a 7-day grace period) and export it. A legacy
[private app](https://developers.hubspot.com/docs/apps/legacy-apps/private-apps/overview)
token works identically:

```shell
export HUBSPOT_ACCESS_TOKEN="pat-..."
```

Grant only the configuration scopes you need — HubSpot evaluates them **per
object type**:

| Managing | Required scopes |
|---|---|
| Contact properties/groups | `crm.schemas.contacts.read`, `crm.schemas.contacts.write` |
| Company properties/groups | `crm.schemas.companies.read`, `crm.schemas.companies.write` |
| Deal properties/groups | `crm.schemas.deals.read`, `crm.schemas.deals.write` |
| Custom-object properties/groups | `crm.schemas.custom.read`, `crm.schemas.custom.write` |
| Custom object schemas | `crm.schemas.custom.read`, `crm.schemas.custom.write` (Enterprise tier) |
| Deal/ticket pipelines | `crm.pipelines.write` (+ `crm.objects.deals.write` / `crm.objects.tickets.write`) |
| Association labels | the read/write schema scopes of both object types (Pro/Ent for custom labels) |
| Lists | `crm.lists.read`, `crm.lists.write` |
| Owners / portal data sources | `crm.objects.owners.read` (owners); no scope needed for portal |

The provider never requests or uses CRM **record** scopes
(`crm.objects.*`) — it cannot read or modify your contacts, companies, or
deals. A `403` from HubSpot usually means the token is missing a scope *or*
the portal's product tier lacks the feature; the provider's error messages
say which scope to check.

### Multi-portal / sandbox promotion

Because resources are addressed by name (not portal-specific IDs), the same
configuration applies cleanly to multiple portals — use one workspace or
provider alias per portal and promote changes with `plan`/`apply`:

```terraform
provider "hubspot" {
  alias        = "sandbox"
  access_token = var.sandbox_token
}

provider "hubspot" {
  alias        = "production"
  access_token = var.production_token
}
```

## Developing the provider

Build, lint, and test with the included `GNUmakefile`:

```shell
make build    # go build ./...
make test     # unit + acceptance tests
make lint     # golangci-lint
make testacc  # acceptance tests (TF_ACC=1)
```

Acceptance tests are **hermetic by default** — they run the full Terraform
lifecycle against an in-process fake HubSpot API that emulates real API
behavior (rate limits, archive semantics, name purgatory), so no credentials
or real portal are required and the suite finishes in seconds. CI runs the
same suite against both Terraform (1.13, 1.14) and OpenTofu.

Development is strictly **test-driven** — the desired HCL and a failing
lifecycle test come before any implementation. See
[`CONTRIBUTING.md`](./CONTRIBUTING.md) for the workflow and
[`dev-docs/`](./dev-docs/) for the design decisions and research behind the
resource model (archive-vs-delete semantics, import ID formats, rate-limit
strategy, and more).

To regenerate documentation after schema changes, run `make generate`
(uses [tfplugindocs](https://github.com/hashicorp/terraform-plugin-docs)).

## FAQ

**Why doesn't the provider manage contacts, companies, or deals?**
Records are the wrong shape for Terraform: they're edited continuously by
sales reps, workflows, and integrations (so `plan` would show perpetual
drift), they put PII into Terraform state (a GDPR liability — state backups
aren't erasable), and refreshing thousands of records doesn't scale. Every
mature SaaS provider (Datadog, Zendesk, Salesforce) draws the same line:
configuration yes, transactional records no. A narrow `hubspot_crm_record`
escape hatch for seed/fixture records is planned (Phase 5).

**What does `terraform destroy` actually do?**
Whatever the HubSpot API does — which is usually *archive*, not delete.
Properties are archived and their names stay reserved for ~90 days; the
provider tells you explicitly when a name is in that "purgatory" window.
Each resource's destroy behavior is documented in its registry page and in
the table above. The provider never fakes a successful delete of something
HubSpot won't remove.

**Can I manage HubSpot's built-in default properties or pipelines?**
Not by declaring them — the provider never silently adopts objects it didn't
create. Explicitly `terraform import` a HubSpot-defined object if you want
to manage it; where HubSpot forbids deletion, destroy returns a clear error
suggesting `terraform state rm`.

**Service key or private app token?**
Either works — both are `pat-…` bearer tokens and the provider treats them
identically. Prefer **service keys** (HubSpot's designated successor to
legacy private apps for data-only integrations, public beta): they support
zero-downtime rotation and per-key request logs. Note service-key scopes
are capped at the creating user's permissions, so create it as a super
admin.

**Does it work with OAuth apps instead of tokens?**
No. OAuth access tokens expire every 30 minutes and need an interactive
install flow — the wrong shape for non-interactive Terraform runs. Private
app tokens are long-lived, portal-scoped, and rotatable. (OAuth support may
be reconsidered if there's demand.)

**Is my access token stored in Terraform state?**
The provider config's `access_token` is marked sensitive and, when supplied
via the `HUBSPOT_ACCESS_TOKEN` environment variable, never appears in your
configuration or state at all — that's the recommended setup.

**How does the provider handle HubSpot rate limits?**
A client-side token bucket stays under the burst limit proactively, and 429
responses are retried automatically with backoff (honoring `Retry-After`).
If your *daily* API quota is exhausted, the provider fails fast with a clear
message instead of retrying pointlessly.

**Is it safe to build on beta HubSpot APIs (e.g. workflows)?**
Resources backed by beta APIs are explicitly marked as beta-backed in their
documentation and use raw-JSON schemas where the API surface is still
moving, so upstream changes don't break your state. Expect these resources
to evolve faster than the rest of the provider.

**Can I migrate from another HubSpot provider (CleverTap, jackemcpherson, …)?**
There is no automatic state migration from third-party HubSpot providers —
their resource schemas and IDs are incompatible with this provider's. The
supported path is adoption via import: remove the resource from the old
provider's management (`terraform state rm`), then `terraform import` it
here using this provider's ID format (e.g. `contacts/customer_tier`). The
HubSpot objects themselves are untouched by the switch.

**Why is feature X missing?**
Check the [out-of-scope table](./ROADMAP.md#out-of-scope--hubspot-has-no-public-api)
first — most gaps exist because HubSpot has no public API for that feature.
If an API exists and the resource just isn't built yet, it's on the
[roadmap](./ROADMAP.md) or worth an issue.

**Terraform or OpenTofu?**
Both, as equals. The same binary serves both tools, CI runs the acceptance
suite against both, and the provider will be published to both registries.

## Support & security

- **Questions / how-to:** [GitHub Discussions](https://github.com/revosai/terraform-provider-hubspot/discussions) — see [`SUPPORT.md`](./SUPPORT.md).
- **Bugs & feature requests:** [open an issue](https://github.com/revosai/terraform-provider-hubspot/issues/new/choose).
- **Security vulnerabilities:** report privately per [`SECURITY.md`](./SECURITY.md) — never in a public issue, and never paste `pat-…` tokens or state.
- **Contributing:** see [`CONTRIBUTING.md`](./CONTRIBUTING.md) and the [Code of Conduct](./CODE_OF_CONDUCT.md).

## License

This project is licensed under the [Mozilla Public License 2.0](./LICENSE).
