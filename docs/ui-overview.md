# UI overview

The web UI is a React SPA (Vite, Tailwind CSS, shadcn/ui) built from [`frontend/`](../frontend/) and served at `/`.

## Flagship: Query Investigation

Primary path for the product story:

1. **Investigate** (or **Start guided demo** from the landing workspace)
2. Open a scenario (e.g. **Slow dashboard query**) or paste SQL
3. Review **findings** from the execution plan
4. Edit or accept a **candidate** rewrite → **Compare plans**
5. **Generate report** → engineering investigation report

Concepts behind each step: [Concepts](concepts.md). Timed demo: [Demo runbook](DEMO_RUNBOOK.md).

## Other surfaces

| Area | What it does |
|------|----------------|
| **Workspace / regression inbox** | Entry points from workload signals into investigations |
| **Query runner** | Ad-hoc read-only SQL, schema browser, connection picker |
| **Ask** | Natural language → SQL (optional LLM) |
| **Saved queries** | Persist and re-run; connection badges / filters |
| **Reports** | List and open reports; HTML/PDF export where enabled |
| **Security & Trust** | Shows configured hardening (readonly, allowlists, ANALYZE, etc.) |
| **Settings → Analytics** | Read-only metrics thresholds from [Configuration](configuration.md#metrics) |

## Connections

Query Runner, Ask, Saved Queries, and Investigations can target a `connection_id` when multiple analytical sources are configured (`DATABASE_CONNECTIONS_JSON`).

## See also

[Quick start](getting-started/quickstart.md) · [Trust model](trust-model.md) · [API examples](api/examples.md) · [Docs overview](index.md)
