# API reference

Base URL: `http://localhost:8080/api/v1` (port from [Configuration](configuration.md#server)).
All bodies are JSON. Auth: [Authentication and roles](../security/authentication.md).
Flagship flow with runnable examples: [REST API](../integrations/rest-api.md).
Field values for `evidence_mode`, equivalence status, fix status and finding kinds:
[Evidence and status vocabulary](evidence.md). Error codes: [API errors](api-errors.md).

This page tracks the Goa design (`api/design/*.go`) and the generated OpenAPI 3 spec
(`api/gen/http/openapi3.yaml`); `make docs-contract-check` fails if they diverge.

## Investigations

| Method | Path | Description |
|---|---|---|
| POST | `/investigations` | Body: `title`, `sql`, optional `connection_id`, `analyze`. Creates an investigation and runs plan analysis |
| POST | `/investigations/from-regression` | Body: **`regression_alert_id`** (uuid). Opens, or reuses, an investigation from a regression alert |
| GET | `/investigations` | Query: `limit` (default 20, max 100), `offset` |
| GET | `/investigations/{id}` | Full investigation: evidence, candidate, comparison |
| POST | `/investigations/{id}/suggest-rewrite` | AST-based rewrite suggestions. Returns `candidates[]` (`sql`, `rationale`, `category`). No SQL executes |
| POST | `/investigations/{id}/rank-candidates` | Body: `analyze` (default `false`). Dry-EXPLAINs rewrites and index DDL; returns `baseline`, `candidates[]`, `recommendation` |
| POST | `/investigations/{id}/candidate` | Body: `candidate_sql`, `analyze`, `verify_results`, `binds`. Attaches a candidate, runs compare, optionally verifies results (needs the `query` permission) |
| POST | `/investigations/{id}/fix` | Body: `fix_status` ∈ `proposed`\|`verified`\|`applied`\|`abandoned` (never `confirmed`/`regressed` — those are system-only), `fix_reference` |
| POST | `/investigations/{id}/report` | Query param **`accept_sample_match`** (not a body field). Generates a deterministic engineering report; gated by equivalence — see [Verify result equivalence](../workflows/verify-results.md#the-report-gate) |

## Workspace

| Method | Path | Description |
|---|---|---|
| GET | `/workspace/overview` | Landing summary (stats / attention counts) |
| GET | `/workspace/regressions` | Query: `limit` (default 10, max 50), `include_acknowledged`. Regression inbox |
| POST | `/workspace/regressions/{id}/acknowledge` | Acknowledge one alert (204) |
| GET | `/demo/scenarios` | Guided demo scenarios with date literals computed from the live seed — problem SQL only, no answer-key rewrite |
| GET | `/trust` | Query: `connection_id`. Security & Trust snapshot for the UI |

## Queries

| Method | Path | Description |
|---|---|---|
| POST | `/queries/run` | Body: `sql`, `limit` (default 100, max 1000), `connection_id`. Executes read-only SQL |
| POST | `/queries/explain` | Body: `sql`, `analyze`, `connection_id`. `EXPLAIN (FORMAT JSON)`, or `EXPLAIN (ANALYZE, ...)` when allowed |
| POST | `/queries/explain/compare` | Body: `before_sql`, `after_sql`, `analyze`, `verify_results`, `timing_runs` (1–5, default 1), `connection_id`, `binds`. Standalone plan/result compare |
| GET | `/queries/stats` | Query: `order_by` ∈ `total_time`\|`mean_time`\|`calls`, `limit` (default 20, max 100), `connection_id`. `pg_stat_statements` view |
| POST | `/queries/saved` | Body: `name`, `sql`, `description`, `tags`, `connection_id`. Save a query |
| GET | `/queries/saved` | Query: `limit`, `offset`, `tags`, `connection_id` |
| GET | `/queries/saved/{id}` | Get one saved query |
| DELETE | `/queries/saved/{id}` | Delete (204) |

## Connections

| Method | Path | Description |
|---|---|---|
| GET | `/connections` | `{"items":[{"id","name"}]}`. No secrets returned |

## Schema

| Method | Path | Description |
|---|---|---|
| GET | `/schema` | Query: `connection_id`. Allowed schemas, tables, columns |

## Suggestions

| Method | Path | Description |
|---|---|---|
| GET | `/suggestions/queries` | Query: `intent`, `limit`. Curated/intent-matched SQL |
| GET | `/suggestions/questions` | Query: `connection_id`, `limit` (default 8). Suggested natural-language questions |
| GET | `/suggestions/similar` | Query: `text`, `limit`. Semantic search over saved queries (needs [embeddings](../integrations/semantic-search.md)) |
| POST | `/suggestions/ask` | Body: `question`, `connection_id`. NL → SQL → narrative (needs [LLM](../integrations/llm.md)) |
| POST | `/suggestions/chat` | Body: `question`, `session_id`, `connection_id`. Returns `history`, `follow_ups` (needs LLM) |
| POST | `/suggestions/explain` | Body: `sql`. Plain-English explanation (needs LLM) |

## Reports

| Method | Path | Description |
|---|---|---|
| POST | `/reports/generate` | Body: `sql`, `saved_query_id`, `connection_id`. Workbench LLM narrative report, with a deterministic fallback |
| GET | `/reports/{id}` | Get a report |
| GET | `/reports` | Query: `limit`, `offset`, `saved_query_id`, `connection_id` |
| GET | `/reports/similar` | Query: `text` (required), `connection_id`, `limit` (≤20) |
| POST | `/reports/rewrite` | Body: `report_id`, `instruction`. LLM-assisted narrative revision |
| POST | `/reports/share` | Body: `report_id`, `expires_in_hours` (1–8760). Requires `SECURITY_SHARE_LINKS_ENABLED=true` |
| GET | `/reports/shared/{token}` | **Public, unauthenticated.** View a shared report |
| GET | `/reports/{report_id}/shares` | List active share links for a report |
| POST | `/reports/shares/{id}/revoke` | Revoke a share link |

## Schedules

| Method | Path | Description |
|---|---|---|
| GET | `/schedules` | List |
| POST | `/schedules` | Body: `name`, `saved_query_id` or `sql`, `connection_id`, `interval_expr` (`@every <duration>`), `timezone`, `destination_type` (`webhook`\|`log`), `destination_target`, `enabled` |
| PUT | `/schedules/{id}` | Update the same fields |
| DELETE | `/schedules/{id}` | Delete (204) |
| POST | `/schedules/{id}/run` | Run now |
| GET | `/schedules/{id}/runs` | List runs |
| POST | `/schedule-runs/{run_id}/retry` | Retry a failed run |
| GET | `/webhook-deliveries` | List webhook delivery attempts |

## Dashboards

| Method | Path | Description |
|---|---|---|
| GET | `/dashboards` | List |
| POST | `/dashboards` | Body: `name` |
| GET | `/dashboards/{id}` | Get |
| PUT | `/dashboards/{id}` | Body: `name`, `widgets[]` |
| DELETE | `/dashboards/{id}` | Delete (204) |
| GET | `/dashboards/{id}/resolve` | Get with each widget's data resolved |

## Manual routes (not part of Goa/OpenAPI) {#manual-routes}

Hand-registered in `cmd/server/*.go`. Classified by who they're for:

### Public — never require authentication

| Path | Purpose |
|---|---|
| `GET /health` | Liveness |
| `GET /ready`, `GET /ready/connections` | Readiness — see [Health and monitoring](../operate/monitoring.md) |
| `GET /version` | Build version |
| `GET|POST /auth/login`, `/callback`, `/logout`, `/refresh`, `/auth/session` | Browser OIDC, registered only when OIDC is configured |
| `GET /reports/shared/{token}`, `GET /web/reports/export/shared/pdf` | Shared-report view (Goa route + web export) |

### Authenticated — any signed-in user

| Path | Purpose |
|---|---|
| `GET /api/v1/settings` | LLM, embedding, analytics and auth flags for the UI |
| `GET /api/v1/me`, `/me/organizations` | Current user and their memberships |
| `POST /api/v1/me/organization` | Switch active organization |
| `GET /web/reports/export`, `/export/pdf`, `/export/md`, `/export/json`, `/export/sql` | Report export in various formats |
| `GET /metrics` | Requires auth when `SECURITY_AUTH_ENABLED=true` |

### Administrative — tenant admin or platform admin

| Path | Purpose |
|---|---|
| `GET|POST /api/v1/admin/api-keys`, `POST .../{id}/revoke` | Managed API keys |
| `GET|POST /api/v1/admin/organizations` | Platform admin only |
| `GET|POST|DELETE /api/v1/admin/memberships` | Organization membership |
| `GET|POST|DELETE /api/v1/admin/connection-assignments` | Which connections an org may use |
| `POST|DELETE /api/v1/admin/connection-permissions` | Per-connection actions granted to an org |
| `GET|POST|DELETE /api/v1/admin/connection-secrets` | Per-organization connection credentials |

### Internal — diagnostics, not a stable contract

| Path | Purpose |
|---|---|
| `GET /api/v1/diagnostics/db-privileges` | Database security-boundary audit |
| `GET /api/v1/diagnostics/webhook-policy` | Webhook allowlist snapshot |

Full auth-boundary rules: [Authentication and roles](../security/authentication.md).

## See also

[REST API](../integrations/rest-api.md) · [API errors](api-errors.md) ·
[Evidence and status vocabulary](evidence.md) · [Embedded Go](../integrations/embedded-go.md) ·
[Configuration reference](configuration.md)
