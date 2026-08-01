# PgQueryNarrative documentation

PostgreSQL **Query Investigation** workbench: safe read-only SQL, plan evidence, before/after compare, and engineering reports. Optional LLM narratives sit on top — they are not required for the core loop.

**Audience paths**

| Path | Start |
|------|--------|
| Try in minutes | [Quick start](getting-started/quickstart.md) (`make demo`) |
| Connect your DB | [Connect your PostgreSQL](getting-started/connect-postgres.md) |
| Ship to prod | [Production — start here](ops/PRODUCTION.md#start-here) |
| Trust & scope | [Trust model](trust-model.md) |

**Recommended reading order:** [Concepts](concepts.md) → [Quick start](getting-started/quickstart.md) → [Trust model](trust-model.md) → [Configuration](configuration.md).

**Local preview:** `make docs` from the repo root → **http://localhost:8000**.

This file mirrors the MkDocs home page ([index.md](index.md)). Prefer **[index.md](index.md)** when browsing the docs site.

---

## Docs index

### Product

| Document | Description |
|----------|-------------|
| [Concepts](concepts.md) | Investigation loop, evidence, EXPLAIN vs ANALYZE, what compare proves |
| [Trust model](trust-model.md) | Readonly role, allowlists, what the app will not do |
| [UI overview](ui-overview.md) | Investigate, compare, reports, Security & Trust |
| [Demo runbook](DEMO_RUNBOOK.md) | Solo demo scenes + README GIF capture notes |
| [Configuration](configuration.md) | Environment variables |

### Getting started

| Document | Description |
|----------|-------------|
| [Quick start](getting-started/quickstart.md) | `make demo` and first investigation |
| [Installation](getting-started/installation.md) | Docker and local prerequisites |
| [Connect your PostgreSQL](getting-started/connect-postgres.md) | Readonly role, schemas, verify |
| [LLM setup](getting-started/llm-setup.md) | Optional providers (Ollama, cloud) |
| [Embedded integration](getting-started/embedded.md) | Go library / middleware |

### API & automation

| Document | Description |
|----------|-------------|
| [API reference](api/README.md) | Endpoints and errors |
| [API examples](api/examples.md) | Investigation, compare, explain, run |
| [CLI usage](usage/cli-usage.md) | Command-line workflows |

### Operations

| Document | Description |
|----------|-------------|
| [Deployment](reference/deployment.md) | Docker, Compose, Kubernetes, Helm |
| [Production ops](ops/PRODUCTION.md) | Start-here checklist, security, monitoring |
| [Operations](reference/operations.md) | Health, monitoring day-2 |
| [Incident runbooks](ops/INCIDENT_RUNBOOKS.md) | Incident response |
| [Troubleshooting](reference/troubleshooting.md) | Common failures |

### Reference & evidence

| Document | Description |
|----------|-------------|
| [Dataset](DATASET.md) | `demo.sales` partitions and 10M seed |
| [Case study](case-studies/01-query-optimization.md) | Optimization walkthrough |
| [PostgreSQL extension](reference/postgres-extension.md) | Call API from SQL |
| [Semantic search (pgvector)](reference/semantic-search-pgvector.md) | Similar queries / RAG |
| [Versioning](reference/versioning-and-releases.md) | Releases and changelog |

### Development

| Document | Description |
|----------|-------------|
| [Development setup](development/setup.md) | Build, codegen, frontend |
| [Testing](development/testing.md) | Unit, integration, E2E |
| [Dev runbook](development/runbook.md) | Daily developer workflow |

---

**Contributing & security:** [.github/CONTRIBUTING.md](../.github/CONTRIBUTING.md) · [.github/SECURITY.md](../.github/SECURITY.md). **Changelog:** [CHANGELOG.md](../CHANGELOG.md). **Root README:** [../README.md](../README.md).
