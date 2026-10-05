# Resource Model Design

Target: Go, terraform-plugin-framework (protocol v6). Auth: HubSpot Private App token; optional developer-account key for public-app webhook resources.

## 0. Design principles

1. **Two planes.** HubSpot splits into a **schema/config plane** (object schemas, properties, pipelines, association definitions, lists, users — low cardinality, changed deliberately, versioned poorly by HubSpot) and a **data plane** (contacts, companies, deals — high cardinality, mutated continuously by reps/workflows/integrations). The provider is excellent at the first and deliberately minimal at the second. This matches every analogous provider (Datadog, Zendesk, Salesforce — see `docs/research/03-prior-art.md`).
2. **HubSpot archives, it rarely deletes.** `terraform destroy` archives; per-resource or provider-level flags opt into hard purge where the API supports it. Destroy behavior is documented per resource (§6).
3. **Server-assigned IDs, immutable names.** Lean on `RequiresReplace` plan modifiers and composite natural-key import IDs (`{objectType}/{name}`); most HubSpot IDs are only unique within a parent scope and differ across portals.
4. **Never auto-adopt.** A 409 on create is an error, not an adoption; users adopt existing/HubSpot-defined objects via explicit `terraform import`.
5. **Least privilege.** The config plane never requests CRM record scopes (only `hubspot_crm_record` users need them) — advertise this.

## 1. Tiered roadmap

### Tier 1 — config-as-code (core)

| Resource | HubSpot API | Why |
|---|---|---|
| `hubspot_property` | `crm/v3/properties/{objectType}` | #1 source of cross-environment drift |
| `hubspot_property_group` | `.../groups` | trivial CRUD, dependency of properties |
| `hubspot_object_schema` | `crm/v3/schemas` | custom object definitions are pure schema |
| `hubspot_pipeline` (stages inline) | `crm/v3/pipelines/{objectType}` | whole-pipeline PUT matches declarative semantics |
| `hubspot_association_label` | `crm/v4/associations/{from}/{to}/labels` | association definitions are schema, not data |
| `hubspot_list` | `crm/v3/lists` | dynamic-list filter definitions are declarative config |
| `hubspot_webhook_settings` / `hubspot_webhook_subscription` | `webhooks/v3/{appId}/…` | classic infra config; **requires developer API key** (second auth domain) |

### Tier 2 — defensible, second wave

- `hubspot_user` (`settings/v3/users`): create/deprovision, `role_id`, `primary_team_id`, `secondary_team_ids`. Roles and teams **cannot be created via API** → `hubspot_role` / `hubspot_team` are data sources only. No selective team-removal API → replace semantics. Must tolerate SCIM-managed users.
- `hubspot_form` (Forms v3, new-editor `formType: hubspot` only) and `hubspot_currency` / FX rates — full APIs, low effort.
- Property validation rules — folded into `hubspot_property` as a `validation` block (dedicated `/crm/v3/property-validations/...` API; a differentiator, almost nobody knows it exists).
- `hubspot_workflow` — **raw-JSON resource, planned Phase 4 (pre-1.0).** Automation v4 has full CRUD and PUT-full-replace + `revisionId` optimistic locking that fits Terraform; the API is **beta**, which is acceptable per the roadmap's API stability policy (beta APIs are in scope with guardrails). Ship as `flow_json` (jsonencode) with semantic-equality diffing and GET-then-PUT revisionId handling, documented as beta-backed; typed action blocks only if/when the API stabilizes. Workflow backup/rollback/promotion is the loudest admin pain (see use-case research).
- `hubspot_owner` — data source only (read-only API; owner `id`, not `userId`, goes into `hubspot_owner_id` record properties).

### Tier 3 — record management (deliberate escape hatch, not the product)

Typed record resources (`hubspot_contact`, `hubspot_deal`) are an anti-pattern: drift explosion (every rep edit diffs), PII in Terraform state (GDPR liability — state backups aren't erasable), O(records) refresh cost, and merge/dedupe breaking state identity. Prior art unanimously excludes them.

Legitimate niche: seed/reference records ("House Account" company, catalog-like custom-object rows), CI test fixtures, demo portals. Serve it with **one generic resource**:

```hcl
resource "hubspot_crm_record" "house_account" {
  object_type = "companies"      # or "2-12345"
  id_property = "domain"         # unique property for upsert identity; null → hs_object_id
  properties = {
    domain = "internal.example.com"
    name   = "House Account"
  }
  # Only listed properties are managed; server-side changes to OTHER properties never diff.
}
```

- Create via `batch/upsert` (single item) when `id_property` set — idempotent adoption; Read fetches only managed property keys (bounds drift by construction); Delete archives, optional `purge_on_destroy` for GDPR delete.
- Companion `hubspot_association` (record-to-record edge) for fixtures.
- Documented loudly: not for bulk data; PII lands in state.

## 2. Tier 1 resource specs (key semantics)

### `hubspot_property`
- `name`, `object_type`, `has_unique_value` → `RequiresReplace` (replace **destroys record data** — emit plan-time warning). `type` → RequiresReplace; `field_type` transitions within a type family are PATCHable.
- Delete = archive; ~90-day name purgatory — catch the recreate-same-name failure and suggest restore-via-UI or a new name.
- `option` = list block, position = order: provider computes `displayOrder = index` on write, sorts read-back by server displayOrder, ignores raw values. Option identity = `value`; changing a value warns about orphaned record values.
- Computed: `hubspot_defined`, `calculated`, modification-metadata booleans. Use `modificationMetadata` in ModifyPlan to fail fast on read-only definitions.
- `calculation_formula` supported (API-created calc properties are API-editable only — clean exclusive ownership).
- Import: `{objectType}/{name}`.

### `hubspot_property_group`
- `object_type`, `name` → RequiresReplace; `label`, `display_order` mutable. HubSpot-defined groups can't be deleted — clear error. Import: `{objectType}/{groupName}`.

### `hubspot_object_schema`
- `name` → RequiresReplace, and replacing destroys all records → require explicit `force_delete = true` or fail with guidance. Delete is two-phase (soft-delete; hard delete via `?archived=true` only when 0 records) — surface "records still exist" 4xx actionably.
- Inline `properties[]` are create-time bootstrap only (never diffed post-create); steer lifecycle to `hubspot_property`.
- Computed: `object_type_id` (`2-XXXX`, portal-specific), `fully_qualified_name` (`p{portalId}_{name}`). Import: objectTypeId (accept fullyQualifiedName, normalize).

### `hubspot_pipeline` (stages inline)
- Stages never exist apart from their pipeline and `displayOrder` is pipeline-global → inline nested list, not a separate stage resource. Match config↔state stages by `stage_id` (computed if not pinned), never by list index — reordering must plan as `display_order` change, not delete+create.
- Update via whole-pipeline PUT with `validateReferencesBeforeDelete=true` (+`validateDealStageUsagesBeforeDelete`) so HubSpot rejects deleting in-use stages instead of stranding records; surface with remediation text.
- Deal stages: `metadata.probability` as **string** ("0.2") to avoid float round-trip diffs. Tickets: `metadata.ticketState`.
- `default` pipeline: not deletable — support adopt-via-import; Delete errors with "remove from config or terraform state rm".
- Import: `{objectType}/{pipelineId}`.

### `hubspot_association_label`
- `from_object_type`, `to_object_type`, `name` → RequiresReplace. `inverse_label` set ⇒ PAIRED (two directional typeIds — store both); null↔non-null flip ⇒ RequiresReplace (API can't convert); text edits are in-place PUT.
- Computed: `type_id`, `inverse_type_id`, `category`. `HUBSPOT_DEFINED` labels are not manageable — error at import. Deleting a label removes it from all record associations — document blast radius.
- Import: `{from}/{to}/{typeId}`.

### `hubspot_list`
- `object_type_id`, `processing_type` (DYNAMIC|MANUAL|SNAPSHOT) → RequiresReplace; SNAPSHOT filters are create-time-only (conditional RequiresReplace on `filter_branch`).
- `filter_branch` = JSON passthrough with **semantic equality** custom type (HubSpot normalizes/reorders/injects defaults — the hardest normalization problem in the provider; budget for it). Typed filter blocks rejected for v1 (huge, evolving grammar).
- Update: `update-list-name` / `update-list-filters` (replaces whole tree — matches Terraform). Delete restorable 90 days.
- Never track DYNAMIC membership. `hubspot_list_membership` (separate resource, fixtures only) validates MANUAL/SNAPSHOT at plan time; read back membership after write.

### `hubspot_webhook_settings` + `hubspot_webhook_subscription`
- Settings is a per-app singleton (Create=Update=PUT; Delete clears targetUrl with a warning — no true delete). Subscription: only `active` is mutable; everything else RequiresReplace (cheap, non-destructive).
- Requires `developer_api_key` provider attr; error cleanly if absent. v3-shaped now; keep the door open for a future journal-based (v4) resource rather than overloading this one.
- Import: `{appId}` / `{appId}/{subscriptionId}`.

### Reporting: `hubspot_dashboard`, `hubspot_report` (+ data sources)

Backed by the **Analytics Reporting API, public beta** (`/analytics/reporting/2027-03-beta/…`, shipped 2026-09-15; per-portal opt-in via a product update). Beta-backed per the API stability policy. Source of truth for the wire shapes: HubSpot's OpenAPI spec `PublicApiSpecs/Analytics/Reporting/Rollouts/327896/2027-03-beta/reporting.json` in `HubSpot/HubSpot-public-api-spec-collection` (proprietary — **never vendor it into this repo**; the contract test fetches it at run time).

**What the API can and cannot do (drives the whole design):**

| Capability | API | Provider surface |
|---|---|---|
| Dashboard create / clone / PATCH metadata / archive / restore | ✅ | `hubspot_dashboard` resource |
| Dashboard widgets: add/remove a report (`PUT`/`DELETE …/widgets/{reportId}`) | ✅ | `report_ids` (set) on `hubspot_dashboard` |
| Widget **layout** (x, y, width, height) | read-only | computed `widgets[]` |
| Report create **from scratch** | ❌ (no public report-configuration format) | — (plan-time error) |
| Report **clone** + PATCH metadata / archive / restore | ✅ | `hubspot_report` resource (create = clone of `source_report_id`) |
| Report configuration (query, visualization) | ❌ not even readable | not capturable — documented gap |
| Tags | read-only | computed `tags[]` |
| Search / get (incl. archived) | ✅ | 4 data sources + `raw_json` snapshots |
| Export (CSV/PDF/… emailed to users) | side-effecting action on record data | **out of scope** (decision #4) |

Scopes: `reporting.full.read` (read/search), `reporting.full.write` (create, clone, archive, restore, batch, widgets), `reporting.full.edit` (PATCH metadata + widgets, no create/archive). A 403 (or 404 on every reporting route) usually means the portal has not opted into the beta or the token lacks the scope — translate into an actionable diagnostic (`reportingErrorDetail`).

**Shared `permissions` block** (both resources; single nested attribute, required — the API requires it on create):

```hcl
permissions = {
  type = "SPECIFIC"                      # PRIVATE | EVERYONE_VIEW | EVERYONE_EDIT | SPECIFIC
  view = [{ type = "TEAM", id = "42" }]  # set of grants (USER|TEAM); only with SPECIFIC
  edit = [{ type = "USER", id = "7" }]
}
```

Wire mapping: dashboards carry `specificPermissions` as an **array** of `{permissionType: VIEW|EDIT, grants}`; reports carry a **single** `{permissionType, grants}` object — so a report allows only one of `view`/`edit` (plan-time validation). `SPECIFIC` requires ≥1 grant; non-`SPECIFIC` forbids grants. Empty grant sets are stored as null.

#### `hubspot_dashboard`
- `name` (req), `description` (opt; null clears via PATCH `null`), `business_unit_id` (opt+computed), `owner_user_id` (opt+computed; set by PATCH after create — create has no owner field), `permissions` (req).
- `report_ids` (opt set): when set, the provider owns widget membership exactly (adds via PUT, removes via DELETE, diff by report ID); when **null**, widgets are unmanaged (UI-arranged dashboards stay untouched). Create passes `reportIdsToAdd` (documented best-effort) and then reconciles + verifies.
- `clone_from_dashboard_id` / `clone_reports` (opt): create via `POST …/{id}/clone`. Create-time only — `RequiresReplace` only when the prior state value is non-null (`requiresReplaceIfPriorNotNull`), so imported dashboards never plan a replacement.
- Computed: `id`, `widgets[] {report_id, x, y, width, height}`, `tags[] {id, name}`, `created_at`, `created_by_user_id`, `updated_at`, `updated_by_user_id`.
- Read: `GET …/dashboards/{id}?properties=permissions,tags,widgets`; 404 or `archived=true` ⇒ remove from state.
- Destroy = **archive** (`PATCH {archived: true}`, which cannot be combined with other fields) + confirming read (`GET ?archived=true`). Restorable.
- Import: `{dashboardId}`.

#### `hubspot_report`
- Create = `POST …/reports/{source_report_id}/clone {name, permissions}` then PATCH for `description` / `business_unit_id` / `owner_user_id`. `source_report_id` null on create ⇒ **plan-time** error explaining that HubSpot has no public report-configuration format (build the template in the UI, then clone or import it). `requiresReplaceIfPriorNotNull` on `source_report_id`.
- Same metadata attributes as the dashboard (`name`, `description`, `business_unit_id`, `owner_user_id`, `permissions`) + computed `tags`, timestamps.
- Destroy = archive + confirming read (same as dashboard). An **imported** report is archived on destroy too — document `removed { lifecycle { destroy = false } }` (Terraform ≥ 1.7, OpenTofu ≥ 1.7) for releasing management without archiving.
- Import: `{reportId}`.

#### Data sources (the read-only / sync-to-repo surface)
- `hubspot_dashboard` (by `id` xor exact `name`; not-found and multiple-matches diagnostics), `hubspot_dashboards` (filters `query`, `owner_user_ids`, `tag_ids`, `business_unit_ids`, `archived`, `include_widgets`), `hubspot_report` (by `id` xor exact `name`), `hubspot_reports` (filters `query`, `dashboard_id`, `on_dashboard`, `owner_user_ids`, `tag_ids`, `business_unit_ids`, `archived`). All paginate fully (`limit=100`, `after` cursor).
- Every item exposes all typed fields **plus `raw_json`**: the API object canonicalized (sorted keys, 2-space indent, trailing newline) with volatile view-tracking fields (`lastViewedAt`, `lastViewedByUserId`) stripped — so `local_file` snapshots committed to git diff only on real changes, and fields HubSpot adds later (e.g. a future report configuration) are captured without a provider release.
- Sync-to-repo workflow (guide `reporting-as-code`): `import` blocks + `plan -generate-config-out` (Terraform and OpenTofu) to adopt existing dashboards/reports; `local_file` + `raw_json` for full-fidelity snapshots; `check` blocks for drift alerts. Terraform-only list resources (`terraform query`) are a possible later addition, never the documented primary path (decision #11).

## 3. Data sources (ship early — they unlock references to unmanaged config)

`hubspot_property` / `hubspot_properties`, `hubspot_pipeline` (exposes `stages[]` with IDs — critical for referencing default-pipeline stages), `hubspot_object_schema`, `hubspot_owner` (by email), `hubspot_team`, `hubspot_role`, `hubspot_association_labels` (needed for HUBSPOT_DEFINED typeIds, e.g. primary company = 1), `hubspot_portal` (account-info: portal_id, time_zone). All name-based lookups need both "not found" and "multiple matches" diagnostics.

## 4. Cross-cutting decisions

- **HubSpot-defined defaults: adopt-via-import, never auto-manage.** Delete on an adopted non-deletable object returns a clear error offering `terraform state rm` — never fake success.
- **Partial failure:** one resource = one logical object; where a resource fans out into multiple writes (stages, options, memberships), apply in dependency order and **always persist state before returning error diagnostics** (framework: `resp.State.Set` even on error) so the next apply converges.
- **Rate limiting:** token-bucket ~100 req/10s default (tunable), reactive 429 backoff honoring `Retry-After`/policyName (`TEN_SECONDLY_ROLLING` retry, `DAILY` abort with actionable message), retry idempotent verbs on 5xx, never retry ambiguous non-idempotent POSTs (except documented-idempotent `batch/upsert`). Separate 5 req/s sub-limiter if search is ever used.
- **Provider config:**

```hcl
provider "hubspot" {
  access_token      = var.hubspot_token   # env HUBSPOT_ACCESS_TOKEN; sensitive
  developer_api_key = var.dev_key         # optional; webhook resources only
  base_url          = "https://api.hubapi.com"  # override for mocking
  rate_limit {
    max_requests_per_10s = 100
    max_retries          = 5
    retry_max_wait       = "60s"
  }
  default_purge_on_destroy = false
}
```

Validate token at Configure with `GET /account-info/v3/details` (cache `portal_id`). OAuth-app tokens out of scope for v1. Support provider aliases (multi-portal sandbox→prod promotion is the headline use case) with per-alias rate limiters.

## 5. Delete semantics summary

| Resource | Destroy behavior |
|---|---|
| property | archive (auto-purge ~90d; name locked meanwhile) |
| object schema | soft-delete; hard delete only at 0 records |
| list | delete (restorable 90d) |
| pipeline | true delete, guarded by validateReferencesBeforeDelete |
| association label | true delete (strips label from all records) |
| webhook subscription | true delete |
| dashboard | archive (restorable) + confirming read |
| report | archive (restorable) + confirming read; imported reports too — use a `removed` block to release without archiving |
| user | deprovision (hard remove; Super Admins not deletable via API) |
| crm_record | archive; optional GDPR purge flag |
