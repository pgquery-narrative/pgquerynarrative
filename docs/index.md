# PgQueryNarrative

**PostgreSQL query intelligence that shows its evidence.** Run read-only SQL against PostgreSQL, analyze execution plans, compare improvements, and produce engineering-ready reports — with an optional LLM narrative layer on top.

Web UI: [UI overview](ui-overview.md) · Automation: [REST API](api/README.md) and [CLI](usage/cli-usage.md).

## Recommended path

1. [Quick start](getting-started/quickstart.md)
2. [LLM setup](getting-started/llm-setup.md) (for narrative reports)
3. [Configuration](configuration.md)

## One-command demo

```bash
make demo
```

Open **http://localhost:8080** and follow the guided Query Investigation workflow. See [Demo runbook](DEMO_RUNBOOK.md) for scene-by-scene details.

## Documentation map

| Topic | Document |
|-------|----------|
| Architecture & daily dev | [Development runbook](development/runbook.md) |
| API | [API reference](api/README.md) · [Examples](api/examples.md) |
| Deployment | [Deployment](reference/deployment.md) · [Production checklist](ops/PRODUCTION.md) |
| Operations | [Operations](reference/operations.md) · [Incident runbooks](ops/INCIDENT_RUNBOOKS.md) |
| Troubleshooting | [Troubleshooting](reference/troubleshooting.md) |

## Local preview

Browse this site with search and navigation at **http://localhost:8000**:

```bash
make docs
```

---

**Contributing & security:** [CONTRIBUTING.md](https://github.com/pgquerynarrative/pgquerynarrative/blob/main/.github/CONTRIBUTING.md) · [SECURITY.md](https://github.com/pgquerynarrative/pgquerynarrative/blob/main/.github/SECURITY.md) · **Changelog:** [CHANGELOG.md](https://github.com/pgquerynarrative/pgquerynarrative/blob/main/CHANGELOG.md)
