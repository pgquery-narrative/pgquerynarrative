# PgQueryNarrative

**PostgreSQL query intelligence that shows its evidence.** Investigate expensive queries with plan findings, compare system-proposed rewrites, and produce engineering-ready reports — with an optional LLM narrative layer on the workbench.

| Path | Link |
|------|------|
| Try the demo | [Quick start](getting-started/quickstart.md) |
| Connect your database | [Connect your PostgreSQL](getting-started/connect-postgres.md) |
| Deploy | [Deployment](reference/deployment.md) |
| Trust & scope | [Trust model](trust-model.md) |
| How the product thinks | [Concepts](concepts.md) |

Web UI: [UI overview](ui-overview.md) · API: [Reference](api/README.md) · [Examples](api/examples.md) · CLI: [CLI usage](usage/cli-usage.md)

## Recommended path

1. [Concepts](concepts.md) — investigation, evidence, rewrite engine, what compare proves  
2. [Quick start](getting-started/quickstart.md) — `make demo`  
3. [Trust model](trust-model.md) — then [Connect your PostgreSQL](getting-started/connect-postgres.md) when leaving the demo schema  
4. [LLM setup](getting-started/llm-setup.md) — only if you want workbench narratives / Ask  

## One-command demo

```bash
make demo
```

Open **http://localhost:8080** → **Investigate** → **Slow dashboard query** → **Suggest rewrite** → **Compare plans** → **Generate report**.

For 50→1 partition proof on ~10M rows: run `make demo-bootstrap` first.

## Documentation map

| Topic | Document |
|-------|----------|
| Concepts & trust | [Concepts](concepts.md) · [Trust model](trust-model.md) |
| Getting started | [Quick start](getting-started/quickstart.md) · [Installation](getting-started/installation.md) · [Connect Postgres](getting-started/connect-postgres.md) |
| Product guides | [UI overview](ui-overview.md) · [Configuration](configuration.md) |
| API | [API reference](api/README.md) · [Examples](api/examples.md) |
| Deployment & ops | [Deployment](reference/deployment.md) · [Operations](reference/operations.md) |
| Troubleshooting | [Troubleshooting](reference/troubleshooting.md) |
| Dataset & case study | [Dataset](DATASET.md) · [Query optimization](case-studies/01-query-optimization.md) |
| Development | [Setup](development/setup.md) · [Testing](development/testing.md) · [Dev runbook](development/runbook.md) |

## Local preview

```bash
make docs
```

Then open **http://localhost:8000**.

---

**Contributing & security:** [CONTRIBUTING.md](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/CONTRIBUTING.md) · [SECURITY.md](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/SECURITY.md) · **Changelog:** [CHANGELOG.md](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/CHANGELOG.md)
