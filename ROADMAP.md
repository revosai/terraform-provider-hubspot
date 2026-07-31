# Roadmap

The public roadmap for `terraform-provider-hubspot`. Order reflects admin
pain and API readiness; timelines are intentionally absent — phases ship
when they're done. Engineering detail behind each phase lives in
[`dev-docs/ROADMAP.md`](./dev-docs/ROADMAP.md).

## API stability policy

**Beta HubSpot APIs are in scope.** A public-beta API (e.g. Automation v4
for workflows) is enough to build a resource on. Guardrails: the resource is
documented as beta-backed, uses a raw-JSON schema where the API surface is
still moving, and absorbs upstream changes in minor releases while marked
beta. Only the complete absence of a public API puts a feature out of scope.

## Status

### ✅ Shipped (v0.1 — "properties as code")

| Resource | Notes |
|---|---|
| `hubspot_property_group` | Full lifecycle + import |
| `hubspot_property` | All field types, ordered enumeration options, archive-aware destroy, name-purgatory handling, import |

### ✅ Shipped: data sources

`data.hubspot_property`, `data.hubspot_properties` (list),
`data.hubspot_owner`, `data.hubspot_portal`, `data.hubspot_pipeline`,
`data.hubspot_object_schema`, `data.hubspot_association_labels` (list) — the
read-only glue that lets configurations reference HubSpot-defined and unmanaged
objects, and resolve portal-specific IDs (custom-object `2-XXXX`, association
`type_id`) by name.

### 🔜 Next: service keys & further data sources

**Improved service key usage.** HubSpot Service Keys (public beta since
Feb 2026) are the designated successor to legacy private apps for
data-only integrations — same `pat-…` bearer format, so the provider
already accepts them transparently via `access_token`/`HUBSPOT_ACCESS_TOKEN`.
Planned improvements:

1. Documentation-first: recommend service keys (Development → Keys →
   Service keys) over legacy private apps in the auth guide, README, and
   provider description; document the caveats (beta, scopes capped at the
   creating user's permissions, no webhooks — app credentials remain
   required for webhook resources).
2. Migrate CI test credentials to a service key to gain 7-day-grace
   rotation and per-key request logs.
3. When HubSpot ships a service-key management API (creation is UI-only
   today): evaluate `hubspot_service_key` resource/data source and
   ephemeral-resource support (short-lived credential handling).
4. Re-evaluate defaults at service-key GA.

All read-only glue data sources have now shipped (see above), including the
Phase 2 additions (pipeline, object schema, association labels).

### Phase 2 — schema plane complete (v0.3)

| Resource | Backing API | Status |
|---|---|---|
| `hubspot_pipeline` (deals/tickets/custom, stages inline) | Pipelines v3 | ✅ shipped |
| `hubspot_object_schema` (custom objects) | Schemas v3 (Enterprise) | ✅ shipped |
| `hubspot_association_label` (paired/unpaired labels) | Associations v4 | ✅ shipped |
| Data sources: pipeline, object schema, association labels | — | ✅ shipped |

### Phase 3 — lists + webhooks (v0.5)

| Resource | Backing API | Status |
|---|---|---|
| `hubspot_list` (dynamic/static/snapshot, JSON filter tree) | Lists v3 | ✅ shipped |
| `hubspot_list_membership` (static lists, fixtures) | Lists v3 | planned |
| `hubspot_webhook_settings`, `hubspot_webhook_subscription` (public apps; needs a developer API key) | Webhooks v3 | planned |

### Phase 4 — workflows (v0.6, beta API)

Workflows as code via the Automation v4 **beta** API — the single loudest
HubSpot admin pain (backup, rollback, sandbox→production promotion of
automation).

| Resource | Backing API | Status |
|---|---|---|
| `hubspot_workflow` (raw JSON flow graph, `revisionId` optimistic locking) | Automation v4 (**beta**) | ✅ shipped |
| `data.hubspot_workflow` (lookup by flow ID or exact name) | Automation v4 (**beta**) | ✅ shipped |

### Phase 5 — people + escape hatch (v0.7)

| Resource | Notes |
|---|---|
| `hubspot_user` | Provisioning, role/team assignment (teams/roles themselves are read-only in HubSpot's API → data sources) |
| `hubspot_crm_record` | Deliberately generic escape hatch for seed/fixture records (upsert by unique property; manages only listed properties). Not a replacement for typed record resources — see the README FAQ |
| `hubspot_association` | Record-to-record edges for fixtures |

### Phase 6 — v1.0 hardening

Import round-trip and upgrade-state test gates, plan-modifier audit,
rate-limit soak test, full registry documentation (per-resource scopes,
destroy-semantics table, promotion guide), and publication to **both** the
Terraform Registry and the OpenTofu Registry.

### Post-1.0 candidates

Forms (new editor), currencies/FX rates, typed workflow action blocks,
typed list filter blocks, options management on HubSpot-defined enums,
webhooks v4 journal subscriptions, CMS-domain data sources.

## Out of scope — HubSpot has no public API

These become roadmap candidates the moment HubSpot ships an API; today they
cannot be managed by any provider:

| Feature | API status |
|---|---|
| Conditional stage properties (required per pipeline stage) | None — [most-requested gap](https://community.hubspot.com/t5/HubSpot-Ideas/Expose-conditional-stage-properties-via-API/idi-p/1011599) |
| Pipeline rules, stage colors, pipeline team access | UI-only |
| Field-level property permissions | UI-only (not enforced on API writes) |
| Duplicate/dedupe rules | UI-only |
| Rollup property creation | UI-only (read-only via API) |
| Lead scoring criteria | None |
| Saved views / index-page filters | None (use `hubspot_list` instead) |
| Team / role / seat creation | Read-only APIs → data sources |
| Private apps & scopes | UI-only (the provider's own out-of-band credential) |
| Private-app webhook subscriptions | UI-only |
| Email sending domains (DKIM/SPF/DMARC) | None |
| Business units, tracking/consent settings | None |

## Input welcome

If a phase ordering doesn't match your needs, or HubSpot ships an API for
something in the out-of-scope table, please open an issue.
