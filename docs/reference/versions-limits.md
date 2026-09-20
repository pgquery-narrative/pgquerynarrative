# Supported versions and limits

## PostgreSQL

The root `docker-compose.yml` **builds** its Postgres image from `POSTGRES_IMAGE`
(default `postgres:16-alpine`) plus HypoPG 1.4.3, rather than pulling a fixed tag —
see [Configuration – database](configuration.md#database).

| Major version | Status |
|---|---|
| 16 | Default local/Compose base; exercised in CI (Compose-based jobs, `e2e.yml`, `release.yml` smoke test) |
| 17 | Supported (extensions and migrations don't depend on version-specific features); not exercised by CI today |
| 18 | Exercised in CI via the `pgvector/pgvector:pg18` testcontainer image used by Go integration tests |

The separate [`pqn` extension](../getting-started/pqn-installation.md#requirements) needs PostgreSQL 16 or later and is
exercised against 16, 17 and 18 in CI (`make verify-pqn-extension`, `make verify-pqn-image`). The release archives include the `pqn` tool and
the extension files, and a PostgreSQL image with `pqn` is published per major version ([releases](../project/releases.md)).

`pg_stat_statements`, `hypopg`, and `pgvector` (`vector`) are optional extensions —
the application degrades gracefully when any is absent (see
[Regressions](../workflows/regressions.md), [Suggest and rank candidates](../workflows/candidates.md#index-advice-and-hypopg),
and [Semantic search](../integrations/semantic-search.md#fallback-behavior)).

## Go, Node, and build tools

| Tool | Minimum |
|---|---|
| Go | 1.26 (CI runs the toolchain pinned in `.github/actions/setup-go-cgo`) |
| Node.js | 22 (frontend build) |
| CGO | Required — `pg_query_go` is a cgo library; a C toolchain must be available |

## Release platforms

Built by [`release.yml`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/workflows/release.yml)
on a tag push:

| Binary archives | Container image |
|---|---|
| `linux/amd64`, `linux/arm64`, `darwin/arm64`, `darwin/amd64` | `linux/amd64`, `linux/arm64` |

Each archive (`pgquerynarrative-<version>-<os>-<arch>.tar.gz`) bundles the server, the `pqn` tool and extension files,
the MCP server, `migrate`, the migration files, the built frontend, the entrypoint
script, and an example env file — self-contained, no clone required. Every archive,
plus `checksums.txt` and an SPDX SBOM, is signed with cosign (Sigstore v0.3 bundles —
cosign v3+ required to verify). The container image is built and signed the same
way; there is no separate CLI image. `make build-release` (local) builds only your
native OS/arch, not the full matrix. Details: [Releases and versioning](../project/releases.md).

## Hard limits

| Limit | Default | Configurable |
|---|---|---|
| Row count per query | 1,000 (soft), 10,000 (hard ceiling regardless of requested `limit`) | Requested `limit` up to the ceiling |
| Result size | 10 MiB | `QUERY_MAX_RESULT_BYTES` |
| Cell size | 1 MiB | `QUERY_MAX_CELL_BYTES` |
| Column count | 100 | `QUERY_MAX_COLUMNS` |
| SQL length | Bytes, configured at validator construction | — |
| `timing_runs` | 1–5 | Request field |
| Report `similar` results | ≤ 20 | Request field |
| Share-link expiry | 1–8,760 hours (1 year) | `expires_in_hours`, default from `SECURITY_SHARE_LINK_DEFAULT_HOURS` |
| Verification fallback sample | 1,000 rows | Not configurable |
| Request body size | 5 MiB | `SECURITY_MAX_REQUEST_BODY_BYTES` |

## See also

[Configuration reference](configuration.md) · [Releases and versioning](../project/releases.md) ·
[Query execution safety](../security/query-safety.md)
