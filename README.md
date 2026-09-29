<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/assets/symbol-dark.svg">
    <img src="docs/assets/symbol.svg" alt="PgQueryNarrative" width="64">
  </picture>
</p>

# PgQueryNarrative

PgQueryNarrative investigates PostgreSQL queries. It reads a plan, proposes a bounded rewrite or index from that query's parse tree when one applies, compares the plans, checks that both queries return the same rows, and writes the evidence into a report. A person reviews the change. The tool does not apply it.

User SQL runs as a dedicated read-only role, separate from the role that migrates the application's own metadata. Vocabulary: [Concepts](docs/concepts.md). System map: [Architecture](docs/architecture.md).

## Quick start

Requires Docker.

```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
make demo
```

Open http://localhost:8080. The walkthrough, including what `make demo` starts, is [Quick start](docs/getting-started/quickstart.md).

To use your own database: [Connect your PostgreSQL](docs/getting-started/connect-postgres.md).

`pqn` is a separate terminal tool. It talks to PostgreSQL directly and does not need this server. Start here: [pqn quick start](docs/getting-started/pqn-extension.md).

## Install

| Goal | Document |
|---|---|
| Container, release archive, or source build | [Installation](docs/getting-started/installation.md) |
| Production deploy | [Deployment](docs/operations/deployment.md), [Production configuration](docs/operations/production.md) |
| Go, PostgreSQL, and release platforms | [Supported versions and limits](docs/reference/versions-limits.md) |
| What a release contains | [Releases and versioning](docs/project/releases.md) |

Source builds need Go 1.26, a C toolchain (`pg_query_go` is cgo), and PostgreSQL 16 or later. Published archives and the container image are on the [releases page](https://github.com/pgquery-narrative/pgquerynarrative/releases). The tag procedure is [RELEASING.md](RELEASING.md).

## Documentation

Published site: <https://pgquery-narrative.github.io/pgquerynarrative/>.

Local preview: `make docs`, then http://127.0.0.1:8000. Where the files live: [Repository architecture](docs/development/repository.md).

### Investigate

- [Investigate a slow query](docs/workflows/investigate.md)
- [Plan findings](docs/workflows/plan-findings.md)
- [Candidates](docs/workflows/candidates.md)
- [Compare plans](docs/workflows/compare.md)
- [Result verification](docs/workflows/verify-results.md)
- [Regressions](docs/workflows/regressions.md)

Status names (`VerifiedEqual`, `SampleMatch`, and the rest) are defined in [Evidence and status vocabulary](docs/reference/evidence.md).

### Integrate

| Surface | Use it for | Document |
|---|---|---|
| REST API | HTTP access to the running server | [REST API](docs/integrations/rest-api.md), [API reference](docs/reference/api.md), [API errors](docs/reference/api-errors.md) |
| CLI | The same API from a shell (`make cli`) | [CLI](docs/reference/cli.md) |
| MCP server | The same API over MCP | [MCP server](docs/integrations/mcp.md) |
| `pqn` | Terminal and SQL against PostgreSQL, no server | [Install](docs/getting-started/pqn-installation.md), [reference](docs/reference/pqn.md) |
| PostgreSQL extension | SQL that calls the REST API | [PostgreSQL extensions](docs/integrations/postgres-extension.md) |
| Go library | Embed the client or mount its HTTP routes | [Embedded Go](docs/integrations/embedded-go.md) |
| LLM | Optional narrative text. The investigation loop runs without one | [LLM providers](docs/integrations/llm.md) |

The Go module path is `github.com/pgquerynarrative/pgquerynarrative`. The GitHub repository is `pgquery-narrative/pgquerynarrative`. The supported library package is `pkg/narrative`. Keep import paths on the module path above. `internal/` is private to this module.

Configuration for every surface: [Configuration](docs/reference/configuration.md).

### Security and operations

Guarantees and how each one is enforced: [Trust model](docs/trust-model.md).

- [Database roles](docs/security/database-roles.md)
- [Query execution safety](docs/security/query-safety.md)
- [Authentication](docs/security/authentication.md)
- [Organizations and tenancy](docs/security/tenancy.md)
- [Data handling](docs/security/data-handling.md)
- [Health and monitoring](docs/operations/monitoring.md)
- [Migrations, upgrades, backup](docs/operations/upgrades.md)
- [Troubleshooting](docs/operations/troubleshooting.md)

Report a vulnerability in private: [SECURITY.md](.github/SECURITY.md).

## Development

- [Setup](docs/development/setup.md)
- [Testing](docs/development/testing.md)
- [Change workflows](docs/development/change-workflows.md)

## Project

- [CONTRIBUTING.md](.github/CONTRIBUTING.md)
- [CODE_OF_CONDUCT.md](.github/CODE_OF_CONDUCT.md)
- [CHANGELOG.md](CHANGELOG.md)

## License

MIT. See [LICENSE](LICENSE). Third-party attribution: [NOTICE](NOTICE).
