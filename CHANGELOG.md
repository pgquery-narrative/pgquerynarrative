# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries: edit `changelog/unreleased.md` then run `make changelog`.

## [Unreleased]

### Added

- **Logging (zerolog):** `LOG_LEVEL` (debug, info, warn, error) and `LOG_PRETTY` (colorful console output for local dev). When set, app uses zerolog for leveled, structured logs; request logging uses one message per request (`http request`) with level by status (4xx/5xx → error, /health, /ready, /version → debug, else info). Documented in Configuration – Logging.
- **Migration 000011:** Set `default_transaction_read_only = on` for the readonly role so the database enforces read-only at session level.
- **Configurable windows:** `METRICS_MAX_TIMESERIES_PERIODS` (default 24, range 2–120) controls the maximum number of periods included in time-series output ("last N" for charts and period history). Configuration doc now has a "Configurable windows" subsection listing all window-related vars (trend periods, moving avg, max time-series periods, seasonal lag, min periods for seasonality). Settings UI shows max time-series periods when present.
- **Multiple database connections:** New `GET /api/v1/connections` endpoint plus optional `connection_id` support across run/save/list/report/schema/ask flows. Saved queries and reports now persist `connection_id`.

### Changed

- **Documentation refresh:** Updated configuration, API reference/examples, UI overview, quick start, CLI usage notes, and troubleshooting to reflect multi-connection behavior and current request/response fields.

### Planned (Release 2)

Additional analytics: further cohort metrics and seasonal adjustments.

## [2.0.0] - 2026-06-28

Postgres-first repositioning: secure read-only SQL, plan analysis, and analytics at scale, with optional AI narratives.

### Added

- **10M-row benchmark dataset:** Monthly range-partitioned `demo.sales`, reproducible via `make seed-large-docker` ([docs/DATASET.md](docs/DATASET.md)).
- **EXPLAIN JSON API:** `POST /api/v1/queries/explain` with seq-scan detection, cost flags, and index suggestions.
- **Parser-based SQL validation:** `pg_query_go` parse-tree walk in `app/queryrunner/validator.go` (replaces substring blocklist).
- **`pg_stat_statements` dashboard:** Query stats UI and `GET /api/v1/queries/stats`.
- **RLS multi-tenant demo:** Row-level security on `demo.sales` ([docs/ops/rls-demo.md](docs/ops/rls-demo.md)).
- **pgvector semantic search:** HNSW index on saved-query embeddings.
- **SQL period comparison:** `LAG` window functions in `app/queryrunner/period_comparison.go`; Go metrics path is fallback.
- **Case study:** 1.1s → 145ms covering index optimization on 10M rows ([docs/case-studies/01-query-optimization.md](docs/case-studies/01-query-optimization.md)).
- **Production ops docs:** Backup, migrations, monitoring (see deployment/operations reference docs).

### Changed

- README and public positioning lead with Postgres query intelligence; AI narrative layer is optional.
- `default_transaction_read_only` enforced on the read-only database role.

## [1.0.0]

### Planned (Release 2)

Additional analytics: further cohort metrics, configurable windows, and seasonal adjustments.

### Added
- **Isolation Forest anomaly detection:** `METRICS_ANOMALY_METHOD=isolation_forest` now supported; calculator branches on method and uses Isolation Forest (random trees, median split, anomaly score) when set; z-score remains default
- **Rate limit burst:** Token-bucket limiter; `SECURITY_RATE_LIMIT_BURST` is now used (refill at RPM, cap at burst); new keys start with full bucket
- **Cohort analysis:** When a dimension column name contains `cohort` (e.g. `cohort_month`), metrics calculator aggregates by (cohort, period) and fills `metrics.cohorts` with period values and optional retention %; period column can be text or numeric
- **Cohort in report UI:** Report detail Analytics card shows a Cohorts section (cohort label, retention %, period–value table) when `metrics.cohorts` is present
- **Cohort in HTML/PDF export:** Report export (HTML and PDF) includes a Cohorts section with the same structure
- **E2E test for cohort:** `Generate_Cohorts` subtest in reports E2E verifies cohort-shaped query produces `metrics.cohorts`
- **Docs:** Cohort input shape documented (Configuration – Cohort analysis); UI overview and docs index updated; reference to removed file removed from changelog
- **Period comparison:** Automatic period-over-period (e.g. this month vs last month) when query results have a date/time column and numeric measures; derived % change and trend (up/down/flat); "Vs previous period" block in query results and report UI with optional period labels
- **Run query API:** `period_comparison` array and optional `period_current_label` / `period_previous_label` on run response
- **Reports API:** `metrics.period_current_label` and `metrics.period_previous_label` when time series is present
- **Configurable trend threshold:** `PERIOD_TREND_THRESHOLD_PERCENT` (default 0.5) for when to label change as up/down vs flat
- **Narrative:** LLM prompt rule to include at least one takeaway on period-over-period change when time series metrics exist
- **Metrics:** Support for PostgreSQL `NUMERIC` (pgtype.Numeric) in column profiling and aggregation so measures like `SUM(...)` appear in period comparison
- Groq LLM provider: `LLM_PROVIDER=groq`, `LLM_MODEL`, `LLM_API_KEY` (OpenAI-compatible API; e.g. llama-3.3-70b-versatile)
- OpenAI (GPT) LLM provider: `LLM_PROVIDER=openai`, `LLM_MODEL`, `LLM_API_KEY` (Chat Completions API)
- Claude LLM provider: `LLM_PROVIDER=claude`, `LLM_MODEL`, `LLM_API_KEY` (Anthropic Messages API)
- Gemini LLM provider: `LLM_PROVIDER=gemini`, `LLM_MODEL`, `LLM_API_KEY` for report generation
- MCP server (`cmd/mcp-server`): tools for Claude desktop / Cursor (run query, generate report, list saved/reports); `config/mcp-example.json`, docs
- **MCP schema, context, and query suggestions:** Schema API `GET /api/v1/schema` (queryable tables/columns from `information_schema`); suggestions API `GET /api/v1/suggestions/queries?intent=...&limit=...` (curated examples + saved-query match by intent); MCP tools `get_schema`, `get_context` (schema + saved queries merged), `suggest_queries`, `list_schemas`, `ask_question`, `explain_sql`; `app/catalog`, `app/suggestions`
- **Ask (NL→SQL→report):** `POST /api/v1/suggestions/ask` — natural-language question → generated SQL → run → narrative report in one step
- **Explain SQL:** `POST /api/v1/suggestions/explain` — plain-English explanation of a SQL query (one or two sentences)
- **Semantic search (similar queries):** `GET /api/v1/suggestions/similar?text=...` — embedding-based retrieval of saved queries similar to text; RAG context in report generation when embeddings enabled
- **Embeddings:** `EMBEDDING_BASE_URL`, `EMBEDDING_MODEL`; in-memory or pgvector (migration 000007); powers similar-query and RAG
- **Configurable schemas:** `DATABASE_ALLOWED_SCHEMAS` (default `public,demo`) — comma-separated schemas queries may access; migration 000010 grants readonly access to `public`
- **demo.sales_summary view:** Read-only aggregated view (migration 000009) for schema discovery and queries
- **Health, readiness, metrics, version:** `GET /health` (liveness), `GET /ready` (readiness, DB check), `GET /metrics` (pool stats), `GET /version` (build version)
- **CORS:** `CORS_ORIGINS` for configurable allowed origins
- **Report export:** HTML (`/web/reports/export?id=...`) and PDF (`/web/reports/export/pdf?id=...`) download; auth-protected when `SECURITY_AUTH_ENABLED` true
- **Query Runner UI:** Schema browser (left sidebar, schema→tables→columns, click to insert); query suggestions card (fetches API, click to run); Ctrl+E focus editor, Ctrl+Enter run; session-stored query history (last 10)
- Report UI: show LLM provider and model; improved report card layout and CSS
- PostgreSQL extension for calling PgQueryNarrative from SQL
- CLI tool for Docker-only usage
- API documentation, contributing guidelines, security policy
- Security scanning (secret scan, CodeQL, govulncheck, gosec)
- **Chart suggestions:** By data structure (time series → line/area; category+value → bar/pie; table); suggestion buttons and chart-type dropdown built from API on query page; area chart support; report page shows suggested charts; unit tests (app/charts/suggester_test.go)
- **Advanced metrics:** Richer time-series (last N periods, 3-period moving average); anomaly detection (z-score, configurable threshold); trend analysis (linear regression over last 6 periods, direction and summary); report API (`periods`, `moving_average`, `anomalies`, `trend_summary`); report UI (trend summary, anomalies list, period history table); unit tests (app/metrics/calculator_test.go)
- **Authentication:** Optional API key (Bearer token) for `/api/*` and `/web/reports/export*`; `SECURITY_AUTH_ENABLED`, `SECURITY_API_KEY`; 401 when missing or invalid; `/health` and `/ready` always unauthenticated
- **Rate limiting:** Per-client IP; `SECURITY_RATE_LIMIT_RPM` (0 = disabled), `SECURITY_RATE_LIMIT_BURST`; 429 when exceeded
- **Audit trail:** `app.audit_logs` table and migration; request logging middleware records API_REQUEST (path, status, identity); AUTH_FAILURE and RATE_LIMIT_EXCEEDED on auth/rate-limit events
- **Testing:** Unit tests for auth (ValidateRequest) and ratelimit (NewLimiter, Allow); integration test for audit store (Record + DB); E2E tests for GET /health and GET /ready
- **Versioning and releases:** Versioning and releases doc (`docs/reference/versioning-and-releases.md`); `make build-release` for multi-arch server + MCP binaries and checksums; release workflow builds server and MCP for linux/amd64, darwin/amd64, darwin/arm64

### Changed
- **Documentation:** Reorganized into `docs/api/`, `docs/usage/`; added Period comparison and Metrics section in configuration (`PERIOD_TREND_THRESHOLD_PERCENT`); API examples in `docs/api/examples.md`, CLI usage in `docs/usage/cli-usage.md`; docs index and cross-links updated
- Documentation: single generic LLM setup guide (Ollama, Gemini, Claude, OpenAI, Groq, MCP); docs shortened and standardized
- Go 1.23 → 1.24; PostgreSQL 18 as default (16, 17, 18 supported)
- Docker: postgres:18-alpine, memory limits

### Fixed
- E2E migration: roles created in 000001 so 000003 GRANT succeeds; migration permission errors
- CLI shell and argument passing for Alpine/Docker
- Postgres init script role creation order
- **Narrative number scale:** LLM prompt now formats sample data with comma-separated thousands and instructs the model to preserve exact magnitude when citing metrics (avoids e.g. 848M instead of 84.8M)

### Security
- Secret scanning, dependency vulnerability scanning, CodeQL, gosec
- Optional API authentication (Bearer token), per-IP rate limiting, and audit logging to `app.audit_logs`

[Unreleased]: https://github.com/pgquerynarrative/pgquerynarrative/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/pgquerynarrative/pgquerynarrative/releases/tag/v1.0.0
