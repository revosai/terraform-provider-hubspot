# Implementation Roadmap (engineering detail)

> The public, user-facing roadmap is [`/ROADMAP.md`](../ROADMAP.md) — keep the two in sync when scope changes. This document carries the per-phase engineering detail.

Phases assume the conventions in `research/01-terraform-provider-best-practices.md` and the resource semantics in `design/resource-model.md`.

## API stability policy

**Beta HubSpot APIs are in scope.** A public-beta API (e.g. Automation v4 for workflows) is enough to build a resource on, with guardrails: the resource is documented as beta-backed (a `~>` note in its registry page), its schema leans on raw-JSON passthrough where the API surface is still moving (so HubSpot-side changes don't break our schema), and breaking upstream changes are absorbed in MINOR releases while the resource is marked beta. Only the *absence* of any public API puts a feature out of scope (see below).

## Phase 0 — Foundations
- Scaffold from `hashicorp/terraform-provider-scaffolding-framework` (framework v1.19+, Go 1.25, protocol v6). Pick a license before writing code (MPL-2.0 like HashiCorp providers, or Apache-2.0).
- `internal/client/`: bearer auth, token-bucket limiter + 429/5xx retry with policyName awareness, typed errors (`ErrNotFound`), cursor pagination helper, structured HubSpot error-body decoding, configurable base URL + version path segments, `tflog` wire logging.
- Provider `Configure` with token validation (`/account-info/v3/details`), portal_id caching.
- Test harness per `docs/research/05-provider-testing.md`: unit layer (schema ValidateImplementation, flatten/expand tables, validator/plan-modifier tests), hermetic `resource.UnitTest` lifecycle against an httptest fake HubSpot (PR-runnable, no creds; fake scripts 429/eventual-consistency/name-purgatory), `TF_ACC`-gated real-portal acceptance tests (basic/update/`_disappears`/import/empty-replan) with `tf-acc-test` prefix, portal-ID guard, and sweepers. CI: PR = lint + unit + hermetic across TF {n-1, n} × OpenTofu (`TF_ACC_TERRAFORM_PATH` → tofu, `TF_ACC_PROVIDER_HOST=registry.opentofu.org`); nightly = real API + sweep, secrets never in fork-PR runs; tfplugindocs generate-diff gate.

## Near-term — improved service key usage
HubSpot **Service Keys** (public beta Feb 2026; `developers.hubspot.com/changelog/service-keys`) are the designated successor to legacy private apps for data-only integrations. Same `pat-{region}-…` bearer format → already accepted transparently by `access_token`. Work items: (1) docs recommend service keys over private apps everywhere (done in provider description, README, index template, real-portal guide); (2) migrate CI test secret to a service key (7-day-grace rotation, per-key request logs); (3) when a management API ships (creation is UI-only today), evaluate `hubspot_service_key` resource/data source + ephemeral resource; (4) re-check defaults/wording at GA. Caveats to keep documented: beta, scopes capped at creating user's permissions, unusable for webhooks (public-app webhook resources keep `developer_api_key`).

## Phase 1 — v0.1 "properties-as-code" (MVP)
`hubspot_property_group`, `hubspot_property` (all types, enum option ordering, calculation_formula, validation rules block), data sources `hubspot_property`/`hubspot_properties`, `hubspot_owner`, `hubspot_portal`. Import from day one. This alone is adoptable — property drift is the #1 admin pain.

## Phase 2 — v0.3 schema plane complete ✅
`hubspot_object_schema`, `hubspot_pipeline` (stage-diff engine keyed on stage_id + validateReferences), `hubspot_association_label`; data sources `hubspot_pipeline`, `hubspot_object_schema`, `hubspot_association_labels`. **All shipped.** Association-label note: HubSpot never returns the create-time `name`, so it is write-only (RequiresReplace, `ImportStateVerifyIgnore`) and unrecoverable on import; paired labels (`inverse_label`) mint two directional `type_id`s and reading the inverse text takes a second GET on the reverse pair; paired↔unpaired flips force replacement.

## Phase 3 — v0.5 lists + webhooks
`hubspot_list` **shipped** (JSON `filter_branch` with a subset-based semantic-equality custom type in `filterbranch_type.go`: config is treated as equal to the server tree when one is a structural subset of the other, absorbing HubSpot's injected defaults — `filterBranchOperator`, `includeObjectsWithNoValueSet`; an empty-object guard keeps an explicit clear as a real diff; array order is significant — typed filter blocks remain deferred). SNAPSHOT filter edits force replacement; DYNAMIC edits are in-place `update-list-filters`; membership never tracked. Still planned: `hubspot_list_membership`, `hubspot_webhook_settings`, `hubspot_webhook_subscription` (+ `developer_api_key` config path).

## Phase 4 — v0.6 workflows (beta API)
`hubspot_workflow` **shipped** — Automation v4 (public beta, allowed per the API stability policy): raw `flow_json` attribute with a JSON semantic-equality custom type (`flowjson_type.go`, same subset comparison as lists — absorbs injected top-level defaults, per-action `actionTypeVersion`, and enrollment-filter expansion; array order significant, typed action blocks deferred), GET-then-PUT `revisionId` optimistic locking (concurrent UI edits are absorbed by the pre-write GET; a raced 409 surfaces as a typed retryable error), `flow_type` CONTACT_FLOW/PLATFORM_FLOW RequiresReplace, `object_type_id` RequiresReplace (server-defaulted to `0-1` for CONTACT_FLOW), `enabled` defaulting to false, import by flow ID. Data source `hubspot_workflow` **shipped** alongside (lookup by flow ID or exact name via paged listing — ambiguous names error; exposes the complete definition JSON for backups). Loudest admin pain (backup/rollback/promotion — a paid product exists just for this); documented as beta-backed.

## Reporting — v0.3 dashboards & reports (beta API)
`hubspot_dashboard`, `hubspot_report` and data sources `hubspot_dashboard(s)` / `hubspot_report(s)` **shipped** on the Analytics Reporting API public beta (`2027-03-beta`, allowed per the API stability policy; design in `design/resource-model.md` → "Reporting"). Capability-driven: dashboards are fully creatable (create/clone/PATCH/archive, widget membership via `PUT`/`DELETE …/widgets/{reportId}` diffed by report ID; `report_ids = null` leaves UI-arranged widgets unmanaged); reports have no from-scratch create (no public report-configuration format), so `hubspot_report` creates by cloning `source_report_id` (plan-time error otherwise) and adopts existing reports via import; create-time clone args use `requiresReplaceIfPriorNotNull` so imports never plan a replacement. Destroy = archive + confirming read. Shared permissions model maps one HCL shape onto the dashboard (array) and report (single-object) wire forms. Data sources expose `raw_json` (canonical, view-tracking fields stripped) for git snapshots — the read-only/sync-to-repo path, paired with `import` + `-generate-config-out`. Hermetic fake in `fake_reporting_test.go`; first kin-openapi contract test (`reporting_contract_test.go`, opt-in `HUBSPOT_OPENAPI_CONTRACT=1`, spec fetched at a pinned commit and never vendored — it is proprietary). Export endpoints deliberately unsupported (side-effecting, CRM data). Blocked on HubSpot: report configuration as code, widget positioning, tag management.

## Phase 5 — v0.7 people + escape hatch
`hubspot_user` (+ `hubspot_team`/`hubspot_role` data sources), `hubspot_crm_record`, `hubspot_association`; PII/state-security documentation page.

## Phase 6 — v1.0 hardening
Import round-trip tests everywhere (`ImportStateVerify`), plan-modifier audit (every immutable field has RequiresReplace + destroy-impact warnings), rate-limit soak test, state-upgrade paths frozen, docs (per-resource scopes, archive-semantics table, "managing HubSpot defaults" guide, sandbox→prod promotion guide with workspaces/aliases), **dual registry publication** per `docs/research/07-releasing.md` — Terraform Registry (goreleaser + GPG, webhook auto-ingest) and OpenTofu Registry (issue-form submission of provider + non-expiring RSA signing key to `opentofu/registry`; it indexes the same GitHub release artifacts — no separate build needed). Rehearse the pipeline with a prerelease tag (`v0.1.0-rc1`) before the first real release; add `goreleaser release --snapshot` smoke + upgrade-state gate to PR CI from the first tagged release onward.

## Post-1.0 candidates
- `hubspot_form` (Forms v3, new-editor only), `hubspot_currency`/FX rates.
- Typed action blocks for workflows (once Automation v4 stabilizes); typed filter blocks for lists; `hubspot_property_options` (manage options on HubSpot-defined enums like lifecyclestage); webhooks v4 journal subscriptions (beta — evaluate under the API stability policy); CMS-domain data sources.

## Out of scope — no public API (document, watch the HubSpot changelog)

These are admin-relevant features that CANNOT be resources today because HubSpot exposes no API at all (not even beta). Each is a candidate the moment an API ships:

| Feature | Why admins want it | API status |
|---|---|---|
| Conditional stage properties (required properties per pipeline stage) | Data-quality enforcement in the sales process | None — the most-requested gap ([idea thread](https://community.hubspot.com/t5/HubSpot-Ideas/Expose-conditional-stage-properties-via-API/idi-p/1011599)); not even readable |
| Pipeline rules, stage colors, pipeline team access | Process governance | UI-only |
| Field-level property permissions | Restrict who edits fields | UI-only (and not enforced on API writes) |
| Duplicate/dedupe rules | Data quality | UI-only; only record `merge` has an API |
| Rollup property creation | Aggregates across associated records | UI-only; API exposes them read-only |
| Lead scoring criteria (new engine, GA Aug 2025) | Scoring as code | None; score values readable as properties only |
| Saved views / index-page filters | Shared team views | None; lists are the API-manageable substitute |
| Team / role (permission set) / seat creation | Full user management as code | Read-only APIs — that's why `hubspot_team`/`hubspot_role` are data sources |
| Private apps & their scopes | Bootstrap the provider's own credential | UI/projects-CLI only — inherently out-of-band |
| Private-app webhook subscriptions | Webhooks without a developer account | UI / `webhooks.json` in projects only (public-app webhooks v3 ARE covered, Phase 3) |
| Email sending domains (DKIM/SPF/DMARC) | Domain config as code | None |
| Business units, tracking/consent settings | Account structure | None / client-side JS only |

## Competitive note
`jackemcpherson/terraform-provider-hubspot` (framework-native, properties+groups, v0.1.1 released 2026-07-19) is active in the same niche with rigorous design docs. Before each phase, check whether collaboration or differentiation (schema plane completeness, workflows, multi-portal promotion story) is the better move.
