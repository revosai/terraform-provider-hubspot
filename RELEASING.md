# Releasing

This is a short, user- and maintainer-facing summary. The full engineering
detail lives in [`dev-docs/research/07-releasing.md`](./dev-docs/research/07-releasing.md).

## Versioning policy

The provider follows [Semantic Versioning](https://semver.org/):

- **Pre-1.0 (`0.x`):** the API is still stabilizing. Breaking changes may land
  in a **minor** release (`0.x → 0.(x+1)`); patch releases are backward
  compatible. Pin to the minor with a three-part constraint, e.g.
  `version = "~> 0.3.1"` (≥ 0.3.1, < 0.4.0): it accepts patch releases but
  not the next minor (a two-part `~> 0.3` would allow any 0.x). Read the
  changelog before raising the minor.
- **Post-1.0:** breaking changes only in a **major** release, accompanied by an
  upgrade guide under `docs/guides/`.

**Published versions are immutable.** A released tag is never deleted, moved, or
re-tagged — both registries treat versions as permanent. Mistakes are corrected
by shipping a new patch release, never by rewriting history.

## Release flow (maintainers)

1. Move the `## [Unreleased]` section of [`CHANGELOG.md`](./CHANGELOG.md) to the
   new version with the release date.
2. Tag the release: `git tag vX.Y.Z && git push origin vX.Y.Z`.
3. The [`release`](./.github/workflows/release.yml) workflow runs
   [GoReleaser](https://goreleaser.com/) to build cross-platform binaries, the
   `terraform-registry-manifest.json` (protocol 6.0), `SHA256SUMS`, and a
   detached GPG signature, then publishes a GitHub Release.
4. Both the [Terraform Registry](https://registry.terraform.io/providers/revosai/hubspot)
   and the [OpenTofu Registry](https://search.opentofu.org/provider/revosai/hubspot)
   pick up the new tag.

## Signing

Releases are signed with an **RSA** GPG key (ECC keys are rejected by the
Terraform Registry) that is **non-expiring** (required by the OpenTofu
Registry). The public key is registered with both registries.

## Pre-tag checks

Before tagging, run `goreleaser check` and a snapshot build
(`goreleaser release --snapshot --clean`) locally to confirm every target
platform compiles, and run the state-upgrade acceptance tests before any
`SchemaVersion` bump or major release.
