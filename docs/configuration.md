# Configuration

PgQueryNarrative is configured via **environment variables** only. Sensible defaults apply for local use. Invalid config produces clear startup errors (see [Troubleshooting](reference/troubleshooting.md)).

## Loading config

| Method | Usage |
|--------|--------|
| **Env** | `export PGQUERYNARRATIVE_PORT=8081` then start. |
| **.env** | Create `.env` in project root (gitignored); `export $(cat .env | xargs)` before starting. Do not commit secrets. |
| **Docker Compose** | Set `environment` under `app` in [docker-compose.yml](../docker-compose.yml). |

---

## Logging

| Variable | Default | Description |
|---------|---------|-------------|
| `LOG_LEVEL` | `info` | Log level: `debug`, `info`, `warn`, `error`. Uses zerolog; all app logs are leveled. |
| `LOG_PRETTY` | (unset → pretty) | When **unset** or set to a truthy value (e.g. `true`, `1`), logs are human-readable and colorful (ideal for local dev). Set to `false` or `0` for one-JSON-line-per-log (e.g. production, aggregators). |
| `LOG_DEBUG` | (empty) | `1` or `true` = extra verbose logging (query execution, report generation). Independent of `LOG_LEVEL`. |

---

## Server

| Variable | Default | Description |
|---------|---------|-------------|
| `PGQUERYNARRATIVE_HOST` | `0.0.0.0` | Bind address. |
| `PGQUERYNARRATIVE_PORT` | `8080` | Server port. |
| `PGQUERYNARRATIVE_READ_TIMEOUT` | `15s` | Request read timeout. |
| `PGQUERYNARRATIVE_WRITE_TIMEOUT` | `300s` | Response write timeout (LLM + report paths need headroom). |
| `SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout. |
| `CORS_ORIGINS` | (empty) | Comma-separated origins for CORS; when set, `Access-Control-Allow-Origin` is sent for matching request origins. Empty = same-origin only. |

---

## Database

| Variable | Default | Description |
|---------|---------|-------------|
| `POSTGRES_IMAGE` | `postgres:18-alpine` | Docker Postgres image (root Compose). |
| `DATABASE_HOST` | `localhost` | Database host. |
| `DATABASE_PORT` | `5432` | Database port. |
| `DATABASE_NAME` | `pgquerynarrative` | Database name. |
| `DATABASE_USER` | `pgquerynarrative_app` | Application user (migrations, saved_queries, reports). |
| `DATABASE_PASSWORD` | `pgquerynarrative_app` | Application password. |
| `DATABASE_READONLY_USER` | `pgquerynarrative_readonly` | Read-only user (query execution). |
| `DATABASE_READONLY_PASSWORD` | `pgquerynarrative_readonly` | Read-only password. |
| `DATABASE_SSL_MODE` | `disable` | SSL mode: `disable` \| `require` \| `verify-full`. |
| `DATABASE_MAX_CONNECTIONS` | `10` | Max connection pool size. |
| `QUERY_TIMEOUT` | `30s` | Query execution timeout. |
| `DATABASE_ALLOWED_SCHEMAS` | `public,demo` | Comma-separated schemas queries may access. Use your own schemas here (e.g. `public` or `public,analytics`). |
| `DATABASE_DEFAULT_CONNECTION_ID` | `default` | Default data connection ID used when `connection_id` is omitted in API/UI/MCP requests. |
| `DATABASE_CONNECTIONS_JSON` | (empty) | Optional JSON array of additional read-only data connections. Each item supports: `id`, `name`, `host`, `port`, `database`, `readOnlyUser`, `readOnlyPassword`, `sslMode`, `queryTimeout`, `allowedSchemas`. |

### Multiple database connections {#multiple-database-connections}

PgQueryNarrative supports multiple read-only data connections for query/report/schema flows. App tables (`saved_queries`, `reports`, audit, embeddings) stay in the single app DB configured by `DATABASE_*`.

- If `DATABASE_CONNECTIONS_JSON` is empty, one `default` connection is derived from `DATABASE_HOST`, `DATABASE_PORT`, `DATABASE_NAME`, and read-only credentials.
- If `DATABASE_CONNECTIONS_JSON` is set, those connections are added on top of `default`.
- Requests may pass `connection_id`; when omitted or unknown, server falls back to `DATABASE_DEFAULT_CONNECTION_ID`.

Example:

```bash
export DATABASE_DEFAULT_CONNECTION_ID=default
export DATABASE_CONNECTIONS_JSON='[
  {
    "id": "staging",
    "name": "Staging",
    "host": "staging-db.internal",
    "port": 5432,
    "database": "analytics_staging",
    "readOnlyUser": "analytics_ro",
    "readOnlyPassword": "secret",
    "sslMode": "require",
    "queryTimeout": "30s",
    "allowedSchemas": ["public","analytics"]
  },
  {
    "id": "prod",
    "name": "Production",
    "host": "prod-db.internal",
    "port": 5432,
    "database": "analytics_prod",
    "readOnlyUser": "analytics_ro",
    "readOnlyPassword": "secret",
    "sslMode": "require",
    "queryTimeout": "30s",
    "allowedSchemas": ["public","analytics"]
  }
]'
```

---

## LLM {#llm}

Required for [report generation](api/README.md#reports). See [LLM setup](getting-started/llm-setup.md).

| Variable | Default | Description |
|---------|---------|-------------|
| `LLM_PROVIDER` | `ollama` | `ollama` \| `gemini` \| `claude` \| `openai` \| `groq`. |
| `LLM_MODEL` | `llama3.2` | Model name. |
| `LLM_BASE_URL` | `http://localhost:11434` | LLM API base URL. Docker with Ollama on host: `http://host.docker.internal:11434`. |
| `LLM_API_KEY` | (empty) | API key (required for cloud providers). |

---

## Embeddings (optional) {#embeddings}

Used for [GET /api/v1/suggestions/similar](api/README.md#suggestions) and RAG context in report generation. When not set, those features are disabled.

| Variable | Default | Description |
|---------|---------|-------------|
| `EMBEDDING_BASE_URL` | (empty) | Embedding API URL. If empty and `LLM_PROVIDER=ollama`, defaults to `LLM_BASE_URL`. |
| `EMBEDDING_MODEL` | `nomic-embed-text` | Embedding model (e.g. Ollama `nomic-embed-text`). |

Ollama: `ollama pull nomic-embed-text`. See [Semantic search (pgvector)](reference/semantic-search-pgvector.md).

---

## MCP (Claude desktop / Cursor) {#mcp-claude-desktop--cursor}

For [LLM setup – MCP](getting-started/llm-setup.md#mcp-claude-desktop--cursor):

1. Build: `make build-mcp` → `bin/mcp-server`.
2. Edit MCP config:
   - **Claude:** macOS `~/Library/Application Support/Claude/claude_desktop_config.json`; Windows `%APPDATA%\Claude\`; Linux `~/.config/Claude/`.
   - **Cursor:** `.cursor/mcp.json` in the project root (or Settings → MCP). See `config/mcp-example.json` for the template.
3. Add under `mcpServers` (replace path):
   ```json
   "pgquerynarrative": {
     "command": "/path/to/pgquerynarrative/bin/mcp-server"
   }
   ```
   If the app is not at http://localhost:8080, set `"env": { "PGQUERYNARRATIVE_URL": "http://localhost:PORT" }`. If the app has auth enabled (`SECURITY_AUTH_ENABLED=true`), set `"env": { "PGQUERYNARRATIVE_API_KEY": "your-secret-key" }` (same value as `SECURITY_API_KEY`). See `config/mcp-example.json`.
4. Restart the client. Available tools: `run_query`, `generate_report`, `list_saved_queries`, `get_report`, `list_reports`, `get_schema`, `list_connections`, `get_context`, `suggest_queries`, `list_schemas`, `ask_question`, `explain_sql`. For multi-connection setups, these tools accept optional `connection_id` on relevant calls.

---

## Metrics

Time-series and period-comparison behaviour. Values are shown read-only in **Settings → Analytics** in the web UI. Out-of-range values are clamped at load.

### Configurable windows {#configurable-windows}

These variables control the **time/period windows** used for analytics (trend, moving average, time-series length, seasonality). You can tune them for shorter or longer lookbacks.

| Variable | Default | Description |
|---------|---------|-------------|
| `METRICS_TREND_PERIODS` | `6` | Number of periods used for linear regression trend (2–24). Affects the trend sentence in the narrative (e.g. "increasing over the last 6 periods"). |
| `METRICS_MOVING_AVG_WINDOW` | `3` | Simple moving average window length (2–24). Used for the moving average value in time-series metrics. |
| `METRICS_MAX_TIMESERIES_PERIODS` | `24` | Maximum number of periods kept in the time-series **period list** sent to the UI and API. Range 2–120. See [How time-series windowing works](#how-time-series-windowing-works) below. |
| `METRICS_MAX_SEASONAL_LAG` | `12` | Maximum seasonal period to try (2–24). |
| `METRICS_MIN_PERIODS_FOR_SEASONALITY` | `12` | Minimum series length to detect seasonality. |

#### How time-series windowing works {#how-time-series-windowing-works}

When a query returns **time-series data** (one date/time column plus one or more numeric measure columns), the metrics calculator:

1. **Aggregates** by period (e.g. by day or month) and sorts periods by time.
2. **Compares last two periods** for the narrative: "current period" vs "previous period" (e.g. revenue change %). This comparison is **not** limited by `METRICS_MAX_TIMESERIES_PERIODS`.
3. **Builds the period list** for charts and the API: from the full sorted list of periods, only the **last N** are kept, where **N = METRICS_MAX_TIMESERIES_PERIODS** (default 24, clamped to 2–120). So if the query returns 100 days, the report gets the most recent 24 (or your configured N) in `metrics.time_series.<measure>.periods`.
4. **Trend and forecast** use the full series for their calculations (all periods in the result); only the **displayed** period list is capped at N.

**Summary:** `METRICS_MAX_TIMESERIES_PERIODS` caps how many periods appear in the time-series **period list** (charts, API). It does **not** change the "current vs prior period" comparison, the trend period count (`METRICS_TREND_PERIODS`), or the narrative headline.

### Other metrics

| Variable | Default | Description |
|---------|---------|-------------|
| `PERIOD_TREND_THRESHOLD_PERCENT` | `0.5` | Min % change to label trend "up"/"down"; below = "flat". |
| `METRICS_ANOMALY_SIGMA` | `2.0` | Z-score threshold for anomaly detection (1–5). |
| `METRICS_ANOMALY_METHOD` | `zscore` | Anomaly method: `zscore` or `isolation_forest`. |
| `METRICS_CONFIDENCE_LEVEL` | `0.95` | Confidence level for forecast intervals (0.5–0.99). |
| `METRICS_CORRELATION_MIN_ROWS` | `10` | Minimum rows to compute Pearson/Spearman between numeric measures (2–1000). |
| `METRICS_SMOOTHING_ALPHA` | `0.3` | Level smoothing factor for exponential smoothing (0–1). |
| `METRICS_SMOOTHING_BETA` | `0.1` | Trend smoothing factor for Holt (0–1). |

### Cohort analysis {#cohort-analysis}

Report metrics include **cohorts** when the query result has a cohort dimension. Expectation:

- **Cohort column:** A dimension column whose name contains `cohort` (case-insensitive), e.g. `cohort_month`, `signup_cohort`.
- **Period column:** A second dimension (e.g. `period_index`, `month`) or the time column. Values can be numeric (0, 1, 2…) or labels.
- **Measures:** One or more numeric measure columns. Cohorts are aggregated by (cohort, period); the first measure is used for the cohort table and optional retention % (last period / first period × 100).

Example query shape: `SELECT cohort_month, period_index, SUM(revenue) AS revenue FROM … GROUP BY cohort_month, period_index`. The report **Analytics** card and [API report payload](api/README.md#reports) will include `metrics.cohorts`.

---

## Security

| Variable | Default | Description |
|---------|---------|-------------|
| `SECURITY_AUTH_ENABLED` | `false` | When true, `/api/*` and `/web/reports/export*` require `Authorization: Bearer <SECURITY_API_KEY>`. `/health` and `/ready` are never protected. |
| `SECURITY_API_KEY` | (empty) | Bearer token for API auth. Required when `SECURITY_AUTH_ENABLED` is true. |
| `SECURITY_RATE_LIMIT_RPM` | `0` | Max requests per minute per client IP (0 = disabled). |
| `SECURITY_RATE_LIMIT_BURST` | `0` | Burst size for rate limiter (0 = 2× RPM). |

---

## Production

- Change default passwords; use secrets management.
- Use SSL for DB: `DATABASE_SSL_MODE=require`.
- Recommended: `QUERY_TIMEOUT=60s`, `DATABASE_MAX_CONNECTIONS=50`.

See [Deployment](reference/deployment.md) and [Operations](reference/operations.md).

---

## See also

- [Installation](getting-started/installation.md) · [API reference](api/README.md) · [Deployment](reference/deployment.md) · [Documentation index](README.md)
