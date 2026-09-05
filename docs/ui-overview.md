# UI overview

The web UI is a React SPA (Vite, Tailwind CSS, shadcn/ui) built from [`frontend/`](https://github.com/pgquery-narrative/pgquerynarrative/tree/main/frontend) and served at `/`.

## Flagship: Query Investigation

Primary path for the product story:

1. **Investigate** (or **Start guided demo** from the landing workspace)
2. Open a scenario (e.g. **Slow dashboard query**) or paste SQL — scenarios ship **problem SQL only**, no prefilled rewrite
3. Review **findings** from the execution plan
4. Click **Suggest rewrite** or **Rank candidates** for a system-proposed candidate (AST rewrites + optional index DDL ranking)
5. **Compare plans** and confirm **equivalence** is Equal
6. **Generate report** → template engineering investigation report (not an LLM narrative)

Concepts behind each step: [Concepts](concepts.md).

## Other surfaces

| Area | What it does |
|------|----------------|
| **Workspace / regression inbox** | Entry points from workload signals into investigations; empty on default demo unless real stats exist — set `APP_ENV=demo` for seeded demo alerts |
| **Query runner** | Ad-hoc read-only SQL, schema browser, connection picker; **Generate report** here produces **LLM narrative** reports |
| **Ask** | Natural language → SQL (optional LLM) |
| **Saved queries** | Persist and re-run; connection badges / filters |
| **Reports** | List investigation and workbench reports; HTML/PDF export where enabled |
| **Dashboards** | Widget dashboards from saved queries and reports |
| **Schedules** | Scheduled report runs and webhook delivery (when runner enabled) |
| **Security & Trust** | Shows configured hardening (readonly, allowlists, ANALYZE, etc.) |
| **Settings → Analytics** | Read-only metrics thresholds from [Configuration](configuration.md#metrics) |

## Two report types

| Type | Where | LLM? |
|------|-------|------|
| **Investigation report** | Investigate → Generate report | No — evidence template from plan metrics and SQL |
| **Workbench report** | Query runner or Ask → Generate report | Yes — narrative from configured provider |

## Connections

Query Runner, Ask, Saved Queries, and Investigations can target a `connection_id` when multiple analytical sources are configured (`DATABASE_CONNECTIONS_JSON`).

## See also

[Quick start](getting-started/quickstart.md) · [Trust model](trust-model.md) · [API examples](api/examples.md) · [Docs overview](index.md)
