# UI overview

The web UI is a React SPA (Vite, Tailwind, shadcn/ui) built from
[`frontend/`](https://github.com/pgquery-narrative/pgquerynarrative/tree/main/frontend)
and served from `/` by the same server that serves the API.

## Flagship: Query Investigation

1. **Investigate** (or **Start guided demo** from the landing workspace)
2. Open a scenario (e.g. **Slow dashboard query**) or paste SQL; scenarios ship
   problem SQL only, never a prefilled rewrite
3. Review **findings** from the execution plan
4. **Suggest rewrite** or **Rank candidates** for a system-proposed candidate
5. **Compare plans** and confirm equivalence
6. **Generate report**: a template engineering report, not an LLM narrative

Concepts behind each step: [Concepts](../concepts.md); the task guides: [Investigate a
slow query](../workflows/investigate.md) onward.

## Other pages

| Page | Route | What it does |
|---|---|---|
| Dashboard (landing) | `/` | Workspace overview, regression inbox entry point |
| Investigate | `/investigate`, `/investigate/:id` | The flagship loop above |
| Query runner | `/query` | Ad-hoc read-only SQL, schema browser, connection picker; **Generate report** here produces an **LLM narrative** report |
| Saved queries | `/saved` | Persist and re-run queries; semantic search when embeddings are configured |
| Reports | `/reports`, `/reports/:id` | List and view investigation and workbench reports; export and share |
| Shared report | `/shared/:token` | Public view of a report shared via a link (no auth) |
| Dashboards | `/dashboards`, `/dashboards/:id` | Widget dashboards built from saved queries and reports |
| Schedules | `/schedules` | Scheduled report runs and webhook/log delivery |
| Query stats | `/stats` | `pg_stat_statements` view for the current connection |
| Security & Trust | `/security` | Configured hardening for the current connection |
| Settings | `/settings` | LLM, embedding, analytics and auth flags (read-only view of server config) |

## Two report types

| Type | Where | LLM? |
|---|---|---|
| **Investigation report** | Investigate → Generate report | No, a deterministic template from plan metrics and SQL |
| **Workbench report** | Query runner or Ask → Generate report | Uses the configured LLM, with a deterministic fallback if it fails |

More: [Reports and sharing](reports.md).

## Connections

Query runner, Ask, saved queries, and investigations can all target a `connection_id`
when more than one analytical source is configured. See
[Multiple connections](../workflows/connections.md).

## See also

[Quick start](../getting-started/quickstart.md) · [Trust model](../trust-model.md) ·
[REST API](../integrations/rest-api.md) · [Docs overview](../index.md)
