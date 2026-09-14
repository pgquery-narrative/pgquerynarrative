# Releases and versioning

Version control and build/packaging. For the checklist that gates a tag, see
[RELEASING.md](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/RELEASING.md).

## Version control

- **SemVer** (MAJOR.MINOR.PATCH), scoped as described below — not to the raw diff.
- `main` is the default branch; all releases are cut from it.
- Release versions are Git tags: `v2.2.0`. No leading `v` in `changelog/released/`
  filenames (`2.2.0.md`).
- [Conventional Commits](https://www.conventionalcommits.org/) so changelog and
  release notes stay consistent.

## Changelog

- **Unreleased:** edit `changelog/unreleased.md`. `make changelog` regenerates
  `CHANGELOG.md` from `changelog/unreleased.md` plus `changelog/released/*.md`.
- **`CHANGELOG.md` is generated, never edited** — a hand-written section is
  overwritten by the next `make changelog`.
- **Releasing:** move unreleased content into `changelog/released/<version>.md`,
  then run `make changelog` again.

## Build and package

```bash
VERSION=1.0.0 make build-release
```

Builds the server and MCP binaries **for your current OS/arch** plus
`checksums.txt` — this is a local convenience, not the release matrix.

### GitHub release (CI)

Pushing a tag `v*.*.*` triggers [`release.yml`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/workflows/release.yml),
which runs `make generate`, builds, and publishes:

| Artifact | Platforms / detail |
|---|---|
| Binary archives | `linux/amd64`, `linux/arm64`, `darwin/arm64`, `darwin/amd64`. Each archive bundles the server, MCP server, `migrate`, migration files, the built SPA, the entrypoint script, and an example env file |
| Checksums | `checksums.txt` over the archives |
| SBOM | `sbom.spdx.json` |
| Signatures | Every archive, `checksums.txt`, and the SBOM are cosign-signed (Sigstore v0.3 bundles — verify with cosign v3+) |
| Container image | `ghcr.io/pgquery-narrative/pgquerynarrative:<version>` and `:latest`, `linux/amd64` + `linux/arm64`, built from the root `Dockerfile` with SBOM and provenance attestations, signed and verified by digest |

There is no separate CLI container image — `Dockerfile.cli` is not built by
release CI. See [Supported versions and limits](../reference/versions-limits.md) for
the same table with Go/Node/PostgreSQL support alongside it.

## `pkg/narrative` API stability

The embeddable client follows SemVer for its **documented public** surface:

| Surface | Stability | Notes |
|---|---|---|
| `narrative.Config`, `NewClient`, `Client.RunQuery`, `GenerateReport`, `GetSchema`, `Close` | Stable | Safe for downstream imports |
| `narrative.SecurityConfig` fields | Stable | New optional fields may appear in minor releases |
| `pkg/narrative/middleware` mount helpers | Stable | `MountChi`, `MountGin`, `MountEcho`; use `MountChiSecured`/`WrapSecured` for auth parity |
| Goa-generated types under `api/gen/` | **Unstable** | Regenerated from `api/design/`; depend on `pkg/narrative` client methods, not raw Goa types, where possible |
| `app/*` packages | **Internal** | No SemVer guarantee; may change without a major `pkg/narrative` bump |

A field removed from a REST response is still a break for anyone generating clients
from the published OpenAPI spec, even when `pkg/narrative` is untouched — it doesn't
by itself force a major bump under this table, but it must appear under a
`### Breaking` heading in the changelog and release notes. `v2.1.0` removed
`ExplainQueryResult.execution_time_ms` on exactly these terms — see
[Evidence and status vocabulary](../reference/evidence.md#timing-fields) for the
replacement fields.

`db.RequiredMigrationVersion` is a readiness gate, not a startup gate — see
[Migrations, upgrades, backup](../operate/upgrades.md). When it moves, release notes
must say so and name the version range.

## Versioned documentation

This site documents the **current** `main` only. With a single supported minor line
(see [SECURITY.md](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/SECURITY.md#supported-versions) —
only the latest minor receives fixes), a version switcher would add navigation
overhead without a corresponding second contract to document. Revisit this if the
project ever supports more than one line concurrently.

## See also

[RELEASING.md](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/RELEASING.md) ·
[Embedded Go](../integrations/embedded-go.md) · [Migrations, upgrades, backup](../operate/upgrades.md)
