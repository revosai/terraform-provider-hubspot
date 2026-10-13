# Real-Portal Acceptance Testing

The hermetic suite (fake API) runs on every PR. A second, env-gated layer —
`TestAccReal_*` in `internal/provider/real_api_test.go` — runs the same
lifecycles against a **real HubSpot test portal**, nightly and on manual
dispatch, under both Terraform and OpenTofu
(`.github/workflows/acceptance-real.yml`).

## One-time setup

### 1. Dedicated test portal

Use a portal that contains nothing you care about — e.g. a developer test
account (Enterprise trials on all Hubs) or a dedicated trial instance.
**Never** point the suite at a production portal; the safety guard (below)
enforces this, but the credential should be scoped to a throwaway portal to
begin with.

### 2. API credential (HubSpot UI)

Preferred: create a **service key** in the test portal — **Development →
Keys → Service keys** → *Create service key* (as a super admin), name it
`terraform-provider-acceptance`, grant the scopes below, copy the key.
Service keys rotate with a 7-day grace period — ideal for CI secrets.

Legacy alternative: settings gear → **Integrations → Private Apps** (newer
UIs: Account Management → Integrations → Private Apps) → *Create a private
app* → name it `terraform-provider-acceptance` → **Scopes**: grant
Read+Write for `crm.schemas.contacts` (today's tests) plus
`crm.schemas.companies`, `crm.schemas.deals`, `crm.schemas.custom`, and
`automation` (workflow lifecycle test — Automation v4 beta). No
`crm.objects.*` record scopes. Create → copy the `pat-…` token.

Note the portal's **Hub ID** (top-right account menu / the number in the
app.hubspot.com URL).

### 3. GitHub configuration

- Repository **secret** `HUBSPOT_ACCESS_TOKEN` = the `pat-…` token
  (Settings → Secrets and variables → Actions → Secrets), or:
  `gh secret set HUBSPOT_ACCESS_TOKEN --repo revosai/terraform-provider-hubspot`
- Repository **variable** `HUBSPOT_TEST_PORTAL_ID` = the Hub ID (Variables
  tab), or:
  `gh variable set HUBSPOT_TEST_PORTAL_ID --repo revosai/terraform-provider-hubspot --body "<hub id>"`

The workflow triggers only on `schedule` and `workflow_dispatch` — secrets
never reach fork PRs. First run: Actions → **Real API Tests** → Run workflow.

## How the layer stays safe

- **Portal-ID guard**: before any mutation, the tests call
  `GET /account-info/v3/details` and compare the token's portal ID against
  `HUBSPOT_TEST_PORTAL_ID`; on mismatch they `t.Fatal` (never skip). Wrong
  credential ⇒ zero writes.
- **Randomized names**: every resource is `tf_acc_test_…`-prefixed with a
  random suffix — required because archived HubSpot property names stay
  reserved ~90 days.
- **Serial execution**: no `t.Parallel`; the workflow runs the Terraform and
  OpenTofu legs sequentially (`max-parallel: 1`) and has a concurrency group
  so scheduled/manual runs never overlap on the shared portal.
- **Sweeper**: `make sweep` (and the always-run `sweep` job after each CI
  run) deletes any leaked `tf_acc_test_*` groups/properties, behind the same
  portal guard.

## Running locally

```sh
export HUBSPOT_ACCESS_TOKEN="pat-..."     # test portal only
export HUBSPOT_TEST_PORTAL_ID="12345678"
make testacc-real                          # TF_ACC=1, -run 'TestAccReal'
make sweep                                 # clean leaked test resources
```

Without the env vars, `TestAccReal_*` skip cleanly — the hermetic suite is
unaffected either way.

## Future: ephemeral accounts per run (Tier 1)

`hs test-account create --json` (CLI ≥ 8.3) can headlessly create an
Enterprise-tier developer test account and returns a personal access key;
`DELETE /integrators/test-portals/v3/{id}` removes it. What blocks full
per-run ephemerality today: private-app tokens are UI-only, so the only
headless bearer path is exchanging the account's PAK via the internal
`localdevauth/v1/auth/refresh` endpoint (short-lived tokens, non-contractual
API, PAK scope coverage of `crm.schemas.*` unverified). Revisit if/when
HubSpot ships an API-creatable server-to-server credential; until then, the
long-lived portal + secret is the recommended design (it's also what every
other HubSpot provider does).
