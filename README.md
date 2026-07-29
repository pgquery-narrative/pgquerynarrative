<p align="center">
  <img src="docs/assets/logo.png" alt="PgQueryNarrative" width="220">
</p>

<h1 align="center">PgQueryNarrative</h1>

<p align="center">
Postgres-native query intelligence: secure read-only SQL, plan analysis, and analytics at scale.<br>
Run queries against a partitioned 10M-row demo dataset, inspect plans with EXPLAIN, and optionally layer on AI-generated narratives.
</p>

---

## Overview

PgQueryNarrative is a Go application built around **PostgreSQL as the source of truth**. It executes read-only SQL through a dedicated database role, enforces single-statement and schema rules at the app layer, and surfaces query intelligence — metrics, chart suggestions, and **EXPLAIN (FORMAT JSON)** plan analysis with seq-scan detection and index hints.

The demo dataset (`demo.sales`) is **range-partitioned by month** and seeded to **~10M rows** for realistic partition pruning and optimization work. See **[docs/DATASET.md](docs/DATASET.md)** for schema layout, measured sizes, and pruning evidence.

The [React SPA](frontend/) provides a query editor (schema browser, suggestions), saved queries, reports, and export (HTML/PDF). The REST API and [CLI](docs/usage/cli-usage.md) support automation.

## Quick start

**Docker-first** (PostgreSQL + app; no host Go or psql required):

```bash
# 1. Start Postgres
make postgres-up

# 2. Apply migrations (includes monthly range partitions on demo.sales)
make migrate-docker

# 3. Seed ~10M rows (several minutes; see docs/DATASET.md for measured numbers)
make seed-large-docker

# 4. Start the full stack (app + Postgres)
make start-docker
```

Open **http://localhost:8080** for the web UI, or call the API directly ([API examples](docs/api/examples.md)):

```bash
# Run a read-only query
curl -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql": "SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category", "limit": 10}'

# Analyze the query plan (seq scans, cost, index suggestions)
curl -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql": "SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category"}'
```

**Local PostgreSQL** (app on host; Postgres must already be running):

```bash
make start-local
```

See [Configuration](docs/configuration.md) for all environment variables.

## Postgres capabilities

| Area | What PgQueryNarrative does |
|------|----------------------------|
| **Secure read-only access** | Queries run on a dedicated `pgquerynarrative_readonly` role; results wrapped in `SELECT * FROM (<sql>) LIMIT $1`; per-query timeout (30s default). |
| **Query validation** | Single-statement enforcement, `demo` schema allowlist, write/DDL blocklist. |
| **EXPLAIN integration** | `POST /api/v1/queries/explain` runs `EXPLAIN (FORMAT JSON [, ANALYZE, BUFFERS])`, parses the plan tree, flags seq scans and high-cost nodes, suggests indexes. |
| **Scale & partitioning** | `demo.sales` is range-partitioned by month (~49 partitions); reproducible 10M-row seed via `make seed-large-docker`. [docs/DATASET.md](docs/DATASET.md) documents sizes and partition-pruning evidence. |
| **Analytics in SQL** | Period-over-period comparison via window functions (`LAG`, `DATE_TRUNC`); metrics and chart suggestions from result shape. |

## Optional: AI narrative layer

Report generation is **optional**. When configured, PgQueryNarrative can turn query results into business narratives using your choice of LLM (Ollama, OpenAI, Claude, Gemini, Groq). This sits on top of the Postgres query layer — the core value is safe SQL execution and plan analysis, not the LLM.

- [LLM setup](docs/getting-started/llm-setup.md) — provider configuration
- [Embedded integration](docs/getting-started/embedded.md) — use as a library via `pkg/narrative/`

## Requirements

- **Docker** (for `make start-docker` and the Docker-first workflow above), or **PostgreSQL 16+** and **Go 1.25+** (for `make start-local` and building).
- For the full web UI from source: **Node.js** and **npm** (to build the [frontend](frontend/)).

## Commands

| Action | Command |
|--------|--------|
| Start Postgres only | `make postgres-up` |
| Migrate (Docker) | `make migrate-docker` |
| Seed 10M rows (Docker) | `make seed-large-docker` |
| Start full stack (Docker) | `make start-docker` |
| Start (local) | `make start-local` |
| Stop | `make stop` |
| Build | `make build` (frontend + server) |
| Test | `make test` |
| CLI | `make cli CMD='query "SELECT * FROM demo.sales LIMIT 5"'` |

## Project structure

| Path | Purpose |
|------|---------|
| [`cmd/server`](cmd/server) | Application entrypoint; serves API, health/ready, web export, React SPA |
| [`app/`](app/) | Core logic: config, DB, [query runner](app/queryrunner), [metrics](app/metrics), [LLM](app/llm), [narrative](app/story), [service](app/service) |
| [`api/design/`](api/design/) | [Goa](https://goa.design/) API design; generated code in `api/gen/` and root `gen/` |
| [`frontend/`](frontend/) | React SPA (Vite, Tailwind CSS, shadcn/ui); built to `frontend/dist`, served by Go at `/` |
| [`web/`](web/) | Server-side web handlers (report export: [HTML](web/handlers.go), [PDF](web/pdf.go)) |
| [`pkg/narrative/`](pkg/narrative/) | Library client and middleware for [embedded integration](docs/getting-started/embedded.md) |
| [`docs/`](docs/README.md) | Documentation |
| [`test/unit/`](test/unit/), [`test/integration/`](test/integration/), [`test/e2e/`](test/e2e/) | Tests |
| [`changelog/`](changelog/) | Release history |

## Documentation

Full documentation in **[docs/](docs/README.md)**. Preview locally with **`make docs`** → http://localhost:8000.

| Section | Links |
|---------|--------|
| **Getting started** | [Installation](docs/getting-started/installation.md) · [Quick start](docs/getting-started/quickstart.md) · [LLM setup](docs/getting-started/llm-setup.md) · [Embedded integration](docs/getting-started/embedded.md) |
| **User guides** | [Configuration](docs/configuration.md) · [UI overview](docs/ui-overview.md) · [CLI usage](docs/usage/cli-usage.md) |
| **API** | [Reference](docs/api/README.md) · [Examples](docs/api/examples.md) |
| **Reference** | [Deployment](docs/reference/deployment.md) · [Operations](docs/reference/operations.md) · [Troubleshooting](docs/reference/troubleshooting.md) · [PostgreSQL extension](docs/reference/postgres-extension.md) · [Semantic search (pgvector)](docs/reference/semantic-search-pgvector.md) |
| **Development** | [Setup](docs/development/setup.md) · [Testing](docs/development/testing.md) · [Runbook](docs/development/runbook.md) |
| **Dataset & case studies** | [10M-row benchmark & partition pruning](docs/DATASET.md) · [Query optimization case study](docs/case-studies/01-query-optimization.md) |
| **Operations** | [Production ops](docs/ops/PRODUCTION.md) · [RLS demo](docs/ops/rls-demo.md) |

**Contributing & security:** [.github/CONTRIBUTING.md](.github/CONTRIBUTING.md) · [.github/SECURITY.md](.github/SECURITY.md). **Changelog:** [CHANGELOG.md](CHANGELOG.md).

## Branches & releases

| Branch / tag | Purpose |
|--------------|---------|
| [`main`](https://github.com/pgquerynarative/pgquerynarrative/tree/main) | Latest development (Postgres-first query intelligence) |
| [`stable-v2.0.0`](https://github.com/pgquerynarative/pgquerynarrative/tree/stable-v2.0.0) | v2 release line — EXPLAIN API, 10M-row benchmark, parser validation, RLS/pgvector |
| [`stable-v1.0.0`](https://github.com/pgquerynarative/pgquerynarrative/tree/stable-v1.0.0) | v1 release line — AI narrative focus, pre-Postgres pivot |
| Tag [`v2.0.0`](https://github.com/pgquerynarative/pgquerynarrative/releases/tag/v2.0.0) | v2.0.0 release snapshot |
| Tag [`v1.0.0`](https://github.com/pgquerynarative/pgquerynarrative/releases/tag/v1.0.0) | v1.0.0 release snapshot |

## License

MIT. See [LICENSE](LICENSE).
