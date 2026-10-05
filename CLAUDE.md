# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project

Terraform provider for HubSpot **portal configuration** (CRM-schema-as-code): properties, property groups, custom object schemas, pipelines, association labels, lists, webhooks, users — deliberately *not* CRM records (contacts/deals), except one generic `hubspot_crm_record` escape hatch for fixtures/seed data. Target: Go + `terraform-plugin-framework` (protocol v6), private-app token auth.

**Current state: Phases 1, 2 & 4 and Reporting (dashboards/reports, beta API) shipped; Phase 3 partially shipped (`hubspot_list` done; list membership + webhooks remain).** Foundations exist: `internal/client` (rate-limited, retrying, typed-error HTTP client — fully tested), provider shell (`internal/provider/provider.go`), and a stateful fake HubSpot server for hermetic acceptance tests (`internal/provider/fake_hubspot_test.go`). Resources: `hubspot_property_group`, `hubspot_property`, `hubspot_pipeline`, `hubspot_object_schema`, `hubspot_association_label`, `hubspot_list`, `hubspot_workflow` (Automation v4 beta), `hubspot_dashboard`, `hubspot_report` (Analytics Reporting beta). Data sources: `hubspot_property`, `hubspot_properties`, `hubspot_owner`, `hubspot_portal`, `hubspot_pipeline`, `hubspot_object_schema`, `hubspot_association_labels`, `hubspot_workflow`, `hubspot_dashboard(s)`, `hubspot_report(s)`. The design and research corpus below remains the source of truth; read the relevant doc before implementing anything.

## Documentation map (read before coding)

- `dev-docs/design/resource-model.md` — **the design.** Tiered resource model, per-resource semantics (RequiresReplace fields, archive-vs-delete, import ID formats), provider config block, cross-cutting decisions. Start here.
- `ROADMAP.md` (repo root) — the **public** roadmap: shipped status, phases, beta-API policy, out-of-scope table. Keep it in sync when scope changes.
- `dev-docs/ROADMAP.md` — the engineering plan behind it (Phase 0 foundations → v1.0, per-phase implementation detail, competitive note).
- `dev-docs/research/01-terraform-provider-best-practices.md` — engineering conventions this repo follows: framework versions, scaffold layout, CRUD/state rules, testing, release/publishing, client design.
- `dev-docs/research/02-hubspot-api-surface.md` — endpoints, scopes, rate limits, and the 10 API quirks (soft-delete, name purgatory, PUT-replace, semantic diffing, pagination).
- `dev-docs/research/03-prior-art.md` — existing HubSpot providers (incl. CleverTap code autopsy) and lessons from Datadog/PagerDuty/Salesforce providers.
- `dev-docs/research/04-crm-admin-use-cases.md` — what admins actually configure, API-feasibility matrix, ranked top-15 use-cases, pain points (sandbox→prod promotion, audit/rollback, multi-portal).
- `dev-docs/research/05-provider-testing.md` — **the testing strategy**: layer map (pure-logic unit → framework-component → hermetic httptest lifecycle → real-portal acceptance), plancheck/statecheck assertions, import/disappears/upgrade test patterns, sweepers, OpenTofu test runs, CI tiering.
- `dev-docs/research/06-documentation.md` — **the documentation strategy**: tfplugindocs generation, examples/templates conventions, MarkdownDescription rules (incl. writing RequiresReplace semantics into descriptions — tfplugindocs doesn't render plan modifiers), guides (auth/scopes matrix, tier gating, destroy semantics), CI doc gates. No CDKTF docs (deprecated Dec 2025).
- `dev-docs/research/07-releasing.md` — **the release strategy**: goreleaser v2 artifacts (binary .sig, manifest with protocol 6.0), RSA GPG key (ECC rejected; non-expiring for OpenTofu), dual-registry publishing (TF webhook / OpenTofu issue-form + periodic scan), changelog automation, version-bump policy, prereleases/backports, pre-tag snapshot builds + upgrade-state gates, published-versions-are-immutable rule.
- `dev-docs/testing-real-portal.md` — real-portal acceptance layer: setup (token, GitHub secret/variable), portal-ID safety guard, sweeper, nightly workflow, ephemeral-account future note.
- `dev-docs/research/08-tdd.md` — **the development workflow (TDD)**: HCL-example-first design, red lifecycle test against a hermetic stateful httpTest fake as the executable spec, property-tests-first for flatten/expand, failing-regression-test-first for bug fixes, kin-openapi validation of the fake against HubSpot's published OpenAPI specs + nightly sandbox contract runs. Contains the repo TDD policy — follow it for every new resource.

Note: once the provider is scaffolded, `docs/` will also hold tfplugindocs **generated** registry docs — never hand-edit those; keep design/research material distinct from the generated tree (relocate to e.g. `dev-docs/` at scaffold time if needed).

## Commands

- `make build` / `make test` (unit + hermetic, no creds) / `make lint` / `make fmt` / `make generate` (tfplugindocs; needs `go get github.com/hashicorp/terraform-plugin-docs` once) / `make testacc`.
- Run one resource's tests: `TF_ACC_TERRAFORM_PATH=/opt/homebrew/bin/terraform go test ./internal/provider/ -run 'TestAccPropertyGroup' -count=1 -v` — acceptance tests are **hermetic** (resource.UnitTest against the fake in `fake_hubspot_test.go`; no portal, no creds needed). Set `TF_ACC_TERRAFORM_PATH` to avoid the harness downloading a Terraform binary.
- TDD is mandatory here — see `dev-docs/research/08-tdd.md` policy: HCL example first, red lifecycle test against the fake, then implement; bug fixes land as failing-test commit + fix commit.

## Non-negotiable design decisions (already made — don't relitigate)

1. **terraform-plugin-framework, never SDKv2.** Go 1.25+, framework v1.19+, protocol v6, no muxing.
2. **Auth = `pat-…` bearer token** (`HUBSPOT_ACCESS_TOKEN`): a HubSpot **service key** (recommended — beta successor to private apps, same token format) or a legacy private-app token; the provider treats them identically. Optional `developer_api_key` for public-app webhook resources only. No OAuth in v1. Service keys have no management API yet (UI-only) — no service-key resource.
3. **Hand-rolled thin client in `internal/client/`** — no official Go SDK exists; community SDKs are stale/partial. Client owns: token-bucket rate limiting (~100 req/10s default) + reactive 429 backoff (policyName-aware: retry `TEN_SECONDLY_ROLLING`, abort `DAILY`), typed errors, cursor pagination, context propagation.
4. **Config plane only; no typed record resources.** `hubspot_contact` etc. are anti-patterns (drift, PII in state, scale). The only record surface is generic `hubspot_crm_record` (upsert by `id_property`, manages only listed properties).
5. **Destroy = archive** on most resources, with a confirming read; never fake a successful delete of non-deletable objects (error suggesting `terraform state rm`). Property/schema names are locked ~90 days after archive — handle the recreate collision explicitly.
6. **Immutable fields get `RequiresReplace` plan modifiers** (plan-time, never apply-time errors); data-destroying replaces emit plan-time warnings.
7. **Import via composite natural keys** (`{objectType}/{name}`, `{objectType}/{pipelineId}`); never auto-adopt on 409 create conflict; HubSpot-defined defaults are adopt-via-import only.
8. **Stages live inline in `hubspot_pipeline`** (whole-pipeline PUT; diff stages by `stage_id`, never list index; always pass `validateReferencesBeforeDelete=true`). Enum options live inline in `hubspot_property` (list position = order, identity = `value`).
9. **Semantic (never raw) equality for JSON-ish attributes** — HubSpot normalizes/reorders/injects defaults (`filter_branch`, displayOrder). Persist state before returning error diagnostics after any successful mutation.
10. Naming: `hubspot_<singular_noun>`; generic resources keyed by `object_type` attribute (one `hubspot_pipeline`, not `hubspot_deal_pipeline`).
11. **OpenTofu is a first-class target.** The provider must work identically under OpenTofu and be published to **both** the Terraform Registry and the OpenTofu Registry (registry.opentofu.org). Never depend on Terraform-CLI-only behavior; test against OpenTofu in CI alongside Terraform; docs/examples must not assume the `terraform` binary exclusively.

## Gotchas that will bite you

- Deal stage `probability` is a **string** ("0.2") in the API — keep it string-typed or suffer float diffs.
- `USER_DEFINED` association `typeId`s and custom-object `objectTypeId`s (`2-XXXX`) are **portal-specific** — resolve by name at read time; cross-portal configs reference by name.
- Search API: separate 5 req/s pool, eventually consistent — never use it for post-create read-back; GET by ID is strongly consistent.
- Workflows (Automation v4) is **beta** with PUT-full-replace + `revisionId` optimistic lock — beta APIs are in scope per the roadmap's API stability policy (raw-JSON schema, resource documented as beta-backed); shipped as `hubspot_workflow` (GET-then-PUT absorbs concurrent revision bumps).
- Reporting API (beta, `2027-03-beta`) has **no report configuration** (query/viz) in reads or writes — reports are created only by clone; widget layout and tags are read-only; per-portal beta opt-in ⇒ 403s (see `reportingErrorDetail`). HubSpot's OpenAPI spec repo is proprietary — fetch it, never vendor it.
- Tier gating: custom objects = Enterprise; association labels/multiple pipelines = Pro/Ent. Translate 403s (scope vs product-tier vs quota) into actionable errors.
- An active competitor exists: `jackemcpherson/terraform-provider-hubspot` (framework-native, same niche) — check it before designing overlapping resources.
