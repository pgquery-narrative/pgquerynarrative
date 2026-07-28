# PgQueryNarrative documentation

Run read-only SQL against PostgreSQL, compute metrics and chart suggestions, and generate narrative reports via a configurable LLM. Web UI: [React SPA](ui-overview.md); automation: [REST API](api/README.md) and [CLI](usage/cli-usage.md).

**Recommended path:** [Quick start](getting-started/quickstart.md) → [LLM setup](getting-started/llm-setup.md) (for reports) → [Configuration](configuration.md).

**Local preview:** run `make docs` from the repo root, then open **http://localhost:8000** (MkDocs Material with search and navigation).

---

## Docs index

| Section | Links |
|--------|--------|
| **Architecture** | [Project structure](../README.md#project-structure) (root README) — entrypoint, `app/`, API design, frontend, web handlers |
| **API reference** | [API reference](api/README.md) — endpoints, request/response, errors · [Examples](api/examples.md) |
| **UI overview** | [UI overview](ui-overview.md) — Query editor, saved queries, reports, Settings → Analytics |
| **Configuration** | [Configuration](configuration.md) — Environment variables (server, DB, LLM, embeddings, metrics, security) |
| **Analytics / Phases** | [Configuration – Metrics](configuration.md#metrics) — Trend, anomaly, forecast, correlation, smoothing; read-only in UI at Settings → Analytics |
| **Deployment / Production** | [Deployment](reference/deployment.md) — Docker, Compose, Kubernetes, Helm · [Operations](reference/operations.md) — Health, monitoring |
| **Troubleshooting** | [Troubleshooting](reference/troubleshooting.md) — Common issues and fixes |

---

### Getting started

| Document | Description |
|----------|-------------|
| [Installation](getting-started/installation.md) | Prerequisites, Docker and local run, verification |
| [Quick start](getting-started/quickstart.md) | Minimal steps to run |
| [LLM setup](getting-started/llm-setup.md) | LLM providers (Ollama, Gemini, Claude, OpenAI, Groq) and MCP |
| [Embedded integration](getting-started/embedded.md) | Go library and HTTP middleware (Chi, Gin, Echo) |

### User guides

| Document | Description |
|----------|-------------|
| [CLI usage](usage/cli-usage.md) | Command-line interface for queries and reports |

### Reference

| Document | Description |
|----------|-------------|
| [PostgreSQL extension](reference/postgres-extension.md) | Call the API from SQL |
| [Semantic search (pgvector)](reference/semantic-search-pgvector.md) | Embeddings, similar-query search, RAG |
| [Versioning and releases](reference/versioning-and-releases.md) | Versioning, changelog, release build |

### Development

| Document | Description |
|----------|-------------|
| [Development setup](development/setup.md) | Build, test, codegen, frontend |
| [Testing](development/testing.md) | Unit, integration, E2E tests |
| [Runbook](development/runbook.md) | Daily dev, before commit, tests, build & release |

---

**Contributing & security:** [.github/CONTRIBUTING.md](../.github/CONTRIBUTING.md) · [.github/SECURITY.md](../.github/SECURITY.md). **Changelog:** [CHANGELOG.md](../CHANGELOG.md).
