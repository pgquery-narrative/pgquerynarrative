<p align="center">
  <img src="docs/assets/logo.png" alt="PgQueryNarrative" width="220">
</p>

<h1 align="center">PgQueryNarrative</h1>

<p align="center">
PgQueryNarrative investigates PostgreSQL queries, proposes bounded changes,<br>
compares plans, verifies results, and records the evidence.
</p>

<p align="center">
  <a href="https://github.com/pgquery-narrative/pgquerynarrative/actions"><img src="https://img.shields.io/github/actions/workflow/status/pgquery-narrative/pgquerynarrative/ci.yml?branch=main&label=CI" alt="CI"></a>
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go 1.26+">
  <img src="https://img.shields.io/badge/PostgreSQL-16%2B-336791?logo=postgresql&logoColor=white" alt="PostgreSQL 16+">
  <img src="https://img.shields.io/github/license/pgquery-narrative/pgquerynarrative" alt="License MIT">
  <a href="https://github.com/pgquery-narrative/pgquerynarrative/pkgs/container/pgquerynarrative"><img src="https://img.shields.io/badge/container-ghcr.io-2496ED" alt="Container"></a>
  <a href="https://github.com/pgquery-narrative/pgquerynarrative/releases"><img src="https://img.shields.io/github/v/release/pgquery-narrative/pgquerynarrative?label=release" alt="Latest release"></a>
  <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/security-policy-blue" alt="Security policy"></a>
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#installation">Installation</a> ·
  <a href="#documentation">Documentation</a> ·
  <a href="#security">Security</a>
</p>

---

A slow query gets a plan, a rewrite or index proposed from its own parse tree when
one applies, a measured before/after comparison, and a check that both queries
return the same rows. Nothing is applied automatically.

- Plan analysis: seq scans, cost, partition pruning, optional `EXPLAIN ANALYZE`
- Rewrite engine: a bounded set of parse-tree patterns (`DATE_TRUNC`, `EXTRACT`,
  `OR` to `UNION ALL`, `IN` to `EXISTS`, and others), not a general optimizer
- Index advice: suggested DDL, projected with HypoPG when it is installed
- Result verification: `VerifiedEqual`, `SampleMatch`, `Different`, `Unverified`,
  `NotRequested`
- Regression detection from `pg_stat_statements`, with an alert inbox
- REST API, MCP server, a PostgreSQL extension, and an embeddable Go client

Query -> plan findings -> candidate -> compare -> verification -> report.

## Quick start

Requires Docker.

```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
make demo
```

Open http://localhost:8080. Guided walkthrough: [Quick start](docs/getting-started/quickstart.md).

## Installation

PgQueryNarrative runs from a container, a release binary, or a source build. See
[Installation](docs/getting-started/installation.md) for prerequisites and every
path, and [Connect your PostgreSQL](docs/getting-started/connect-postgres.md) to
point it at your own database instead of the demo schema.

## Documentation

- [Getting started](docs/getting-started/quickstart.md)
- [Query investigation](docs/workflows/investigate.md)
- [Architecture](docs/architecture.md)
- [Deployment](docs/operate/deployment.md)
- [Configuration](docs/reference/configuration.md)
- [Security](docs/trust-model.md)
- [API](docs/reference/api.md)
- [Development](docs/development/setup.md)

Full site: <https://pgquery-narrative.github.io/pgquerynarrative/>. Preview locally
with `make docs` (http://127.0.0.1:8000).

## Contributing

See [CONTRIBUTING.md](.github/CONTRIBUTING.md).

## Security

- User SQL runs through a dedicated read-only PostgreSQL role, separate from the
  migration identity
- Proposed rewrites, indexes, and DDL are never applied automatically
- Cloud LLM row data is off by default

See [Trust model](docs/trust-model.md) for the full guarantee list and
[SECURITY.md](.github/SECURITY.md) to report a vulnerability.

## License

MIT. See [LICENSE](LICENSE). Third-party attribution: [NOTICE](NOTICE).
