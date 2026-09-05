# Versioning and releases

Version control and build/packaging for PgQueryNarrative.

## Version control

- **Semantic versioning:** [SemVer](https://semver.org/) (MAJOR.MINOR.PATCH). Example: `v1.0.0`.
- **Main branch:** `main` is the default; all releases are cut from `main`.
- **Tags:** Release versions are Git tags: `v1.0.0`, `v1.1.0`. No leading `v` in `changelog/released/` filenames (e.g. `1.0.0.md`).
- **Conventional Commits:** Use `feat:`, `fix:`, `docs:`, `chore:` etc. so changelog and release notes stay consistent.

## Changelog

- **Unreleased:** Edit `changelog/unreleased.md`. Run `make changelog` to regenerate `CHANGELOG.md`.
- **Release:** When cutting a version, move unreleased content into `changelog/released/<version>.md` (e.g. `1.0.0.md`), then run `make changelog`. Commit `CHANGELOG.md` and the new released file before tagging.

## Local release build

From repo root, with a clean tree and version set:

```bash
VERSION=1.0.0 make build-release
```

Produces under `bin/`: server and MCP binaries for `linux/amd64`, `darwin/amd64`, `darwin/arm64`, plus `checksums.txt`. Optionally set `VERSION` via `git describe --tags --always` if you build from a tag.

## GitHub release (CI)

Pushing a tag `v*.*.*` (e.g. `v1.0.0`) triggers the Release workflow. It runs `make generate`, builds server (and optionally MCP) for the same three platforms, creates checksums, and publishes a GitHub Release with the binaries and generated release notes. No manual upload needed.

## Packaging

| Artifact | How |
|----------|-----|
| **Binaries** | CI on tag push, or local `make build-release`. |
| **Docker image** | `docker build -t pgquerynarrative:<tag> .` — the single image (API + built SPA). CI publishes and signs the same image as `ghcr.io/pgquery-narrative/pgquerynarrative:<tag>` on tag push. See [deploy/README.md](../../deploy/README.md). |
| **Changelog** | `make changelog`; commit and include in the release commit before tagging. |

## One-time release checklist

1. Update `changelog/unreleased.md`; run `make changelog`; commit.
2. Move unreleased entries to `changelog/released/<version>.md`; run `make changelog` again; commit.
3. Tag: `git tag -s v1.0.0 -m "Release v1.0.0"`.
4. Push branch and tag: `git push origin main && git push origin v1.0.0`.
5. CI creates the GitHub Release; optionally build and push Docker image with the same tag.

## `pkg/narrative` API stability

The embeddable client (`pkg/narrative`) follows SemVer for **documented public APIs**:

| Surface | Stability | Notes |
|---------|-----------|--------|
| `narrative.Config`, `NewClient`, `Client.RunQuery`, `GenerateReport`, `GetSchema`, `Close` | **Stable** | Safe for downstream imports; breaking changes only in major releases. |
| `narrative.SecurityConfig` fields | **Stable** | Propagates security settings; new optional fields may appear in minor releases. |
| `pkg/narrative/middleware` mount helpers | **Stable** | `MountChi`, `MountGin`, `MountEcho`; use `MountChiSecured` / `WrapSecured` when auth parity with the standalone server is required. |
| Goa-generated service types under `api/gen/` | **Unstable** | Regenerated from `api/design/`; embedders should depend on `pkg/narrative` client methods rather than raw Goa types when possible. |
| `app/*` packages | **Internal** | Not covered by SemVer guarantees for external consumers; may change without a major `pkg/narrative` bump. |

Minor releases may add config fields or middleware options without breaking existing callers. Patch releases are bugfix-only.

See also [Embedded integration](../getting-started/embedded.md).
