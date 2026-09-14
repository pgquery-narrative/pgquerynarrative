# Configuration reference

PgQueryNarrative is configured entirely by **environment variables**; there is no
config file. Every variable below is read in `app/config/config.go` (or, where
noted, outside `Load()`) — this page is checked against that file by
`make docs-contract-check` on every change. Boolean variables use Go's
`strconv.ParseBool`: `1`/`t`/`T`/`TRUE`/`true`/`True` and `0`/`f`/`F`/`FALSE`/`false`/`False`
only — not `yes`/`on`. An unparseable or empty value silently falls back to the
default for every variable type.

**Production StrictMode** (`APP_ENV=production`/`prod`, or `SECURITY_STRICT=true`)
enforces a large additional set of restrictions, listed together in
[Production configuration](../operate/production.md) rather than repeated per row
here — this page states each variable's ordinary default and behavior.

## Loading config

| Method | Usage |
|---|---|
| Env | `export PGQUERYNARRATIVE_PORT=8081` then start |
| `.env` | Create `.env` in the project root (gitignored); `export $(cat .env | xargs)` before starting. Never commit secrets |
| Docker Compose | Set `environment` under `app` in `docker-compose.yml` |

## Logging

| Variable | Default | Description |
|---|---|---|
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` (zerolog) |
| `LOG_PRETTY` | unset → pretty | Unset or truthy = human-readable colorized logs; `false`/`0` = one JSON line per log |
| `LOG_DEBUG` | empty | `1`/`true` (parsed by a separate, more permissive helper) = extra-verbose logging of query execution and report generation, independent of `LOG_LEVEL` |

## Server

| Variable | Default | Description |
|---|---|---|
| `PGQUERYNARRATIVE_HOST` | `0.0.0.0` | Bind address |
| `PGQUERYNARRATIVE_PORT` | `8080` | Server port |
| `PGQUERYNARRATIVE_READ_TIMEOUT` | `15s` | Request read timeout |
| `PGQUERYNARRATIVE_WRITE_TIMEOUT` | `300s` | Response write timeout (Ask/report chains several LLM calls) |
| `SHUTDOWN_TIMEOUT` | `10s` | Graceful shutdown timeout |
| `CORS_ORIGINS` | empty | Comma-separated allowed origins; empty = same-origin only |

## Database {#database}

| Variable | Default | Description |
|---|---|---|
| `POSTGRES_IMAGE` | `postgres:16-alpine` | Base image the root Compose file **builds** (with HypoPG) as `pgquerynarrative-postgres:hypopg`, not a pulled tag. Compose-only, not read by the app |
| `DATABASE_HOST` | `localhost` | Metadata database host |
| `DATABASE_PORT` | `5432` | Metadata database port |
| `DATABASE_NAME` | `pgquerynarrative` | Metadata database name |
| `DATABASE_USER` | `pgquerynarrative_app` | App role: metadata reads/writes (`app.*`) |
| `DATABASE_PASSWORD` | `pgquerynarrative_app` | App role password. ≥16 chars and not a placeholder in production |
| `DATABASE_READONLY_USER` | `pgquerynarrative_readonly` | Default connection's read-only role |
| `DATABASE_READONLY_PASSWORD` | `pgquerynarrative_readonly` | Read-only password. Same production rule as above; enforced per connection too |
| `DATABASE_SSL_MODE` | `disable` | `disable`/`allow`/`prefer`/`require`/`verify-ca`/`verify-full`. Production requires `require`, `verify-ca`, or `verify-full` |
| `DATABASE_MAX_CONNECTIONS` | `10` | Pool size per connection |
| `DATABASE_MIN_CONNECTIONS` | `0` | Minimum idle connections per pool |
| `DATABASE_GLOBAL_MAX_CONNECTIONS` | `0` (off) | Shared connection budget across the app pool and every read-only pool combined; requests fail once exceeded |
| `QUERY_TIMEOUT` | `30s` | Query execution timeout. Must be > 0 in production |
| `QUERY_LOCK_TIMEOUT` | `2s` | Session `lock_timeout` for query sessions. Must be > 0 in production |
| `QUERY_IDLE_IN_TX_TIMEOUT` | `10s` | Session `idle_in_transaction_session_timeout`. Must be > 0 in production |
| `QUERY_MAX_RESULT_BYTES` | `10485760` (10 MiB) | Approximate max materialized result size before `QUERY_RESULT_TOO_LARGE`. Must be > 0 in production |
| `QUERY_MAX_CELL_BYTES` | `1048576` (1 MiB) | Approximate max size for one cell. Must be > 0 in production |
| `QUERY_MAX_COLUMNS` | `100` | Max columns in a result. Must be > 0 in production |
| `DATABASE_ALLOWED_SCHEMAS` | `demo` | Comma-separated schema allowlist. Never `app`, `pg_catalog`, `information_schema`, `pg_toast*` — always rejected. Must be non-empty and exclude `public` in production |
| `DATABASE_DEFAULT_CONNECTION_ID` | `default` | Connection used when `connection_id` is omitted or blank. If this id isn't among the configured connections, the first configured connection is used instead |
| `DATABASE_CONNECTIONS_JSON` | empty | JSON array of additional read-only connections — see [below](#multiple-database-connections) |

### Migration identity (outside `Load()`) {#migration-identity}

Read directly by the container entrypoint, not by the Go config loader — unset
again before the server process starts:

| Variable | Default | Description |
|---|---|---|
| `DATABASE_MIGRATION_USER` | falls back to `DATABASE_USER` | Role used to run `migrate up` at container start. Needs `CREATE EXTENSION` and `ALTER ROLE` |
| `DATABASE_MIGRATION_PASSWORD` | falls back to `DATABASE_PASSWORD` | Password for the migration role |
| `DATABASE_MIGRATION_URL` | derived from the above | Full migration DSN; overrides the two variables above when set |
| `PGQUERYNARRATIVE_SKIP_MIGRATIONS` | `false` | Skip running migrations at container start entirely (e.g. when a separate Job does it) |
| `PGQUERYNARRATIVE_SEED` | `false` | Load `tools/db/seed.sql` after migrating |

Under production StrictMode, if `DATABASE_MIGRATION_USER` equals `DATABASE_USER` and
no `DATABASE_MIGRATION_URL` is set, the entrypoint **refuses to start** rather than
run migrations as a role that can't finish them. See
[Deployment — migration identity](../operate/deployment.md#migration-identity).

### Multiple database connections {#multiple-database-connections}

App tables (`saved_queries`, `reports`, audit, embeddings, …) always stay in the
single metadata database configured above. `DATABASE_CONNECTIONS_JSON` adds
**analytical** connections on top of the implicit `default` one.

Each array entry is matched to Go struct field names **case-insensitively**
(camelCase, e.g. `readOnlyUser`, works; there are no JSON tags, and snake_case keys
are silently dropped). Durations (`queryTimeout`, `lockTimeout`, `idleTxTimeout`)
must be given as **integer nanoseconds**, not duration strings — `"queryTimeout":
"30s"` fails to parse and, because the whole variable is parsed as one JSON value,
**the entire `DATABASE_CONNECTIONS_JSON` is ignored** (only the `default` connection
loads); check the server log for `config: ignoring DATABASE_CONNECTIONS_JSON: ...`
if a connection you configured doesn't appear. An entry missing `id`, `host`,
`database` or `readOnlyUser` is skipped individually. Unset optional fields inherit
from the corresponding `DATABASE_*`/`QUERY_*` default.

Fields: `id`, `name`, `host`, `port`, `database`, `readOnlyUser`,
`readOnlyPassword`, `sslMode`, `queryTimeout` (ns), `lockTimeout` (ns),
`idleTxTimeout` (ns), `allowedSchemas` (array of strings), `maxResultBytes`,
`maxCellBytes`, `maxColumns`.

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
    "queryTimeout": 30000000000,
    "allowedSchemas": ["analytics"]
  }
]'
```

Requests choose a connection with `connection_id` — see
[Multiple connections](../workflows/connections.md) for resolution rules
(unknown id fails with `CONNECTION_NOT_FOUND`, it does not fall back silently).

## Security {#security}

### Authentication

| Variable | Default | Description |
|---|---|---|
| `SECURITY_AUTH_ENABLED` | `false` | Requires Bearer/session/OIDC on `/api/*`, `/metrics`, and the non-shared report export routes. Must be `true` in production |
| `SECURITY_ALLOW_INSECURE_NO_AUTH` | `false` | Required (`true`) when auth is off — explicit opt-in for open local/dev access. Forbidden in production |
| `SECURITY_API_KEY` | empty | Plaintext bearer token. ≥16 chars if set. Forbidden in production — use the hash below |
| `SECURITY_API_KEY_HASH` | empty | SHA-256 hex of the bearer token, compared in constant time |
| `SECURITY_API_KEYS_JSON` | empty | JSON array of managed keys (`key_hash`, `id`, `role`, `scopes`, `expires_at`, `revoked`). Must use `key_hash`, not plaintext `key`, in production |
| `SECURITY_TRUSTED_PROXIES` | empty | Comma-separated CIDRs trusted to set forwarded-for headers for per-IP rate limiting |
| `SECURITY_MAX_REQUEST_BODY_BYTES` | `5242880` (5 MiB) | Hard cap on request body size |

At least one of `SECURITY_API_KEY`, `SECURITY_API_KEY_HASH`, `SECURITY_API_KEYS_JSON`
or `SECURITY_OIDC_ISSUER` is required whenever auth is enabled.

### Rate limiting

| Variable | Default | Description |
|---|---|---|
| `SECURITY_RATE_LIMIT_RPM` | `0` (disabled) | Max requests per minute per client IP. Must be > 0 in production |
| `SECURITY_RATE_LIMIT_BURST` | `0` (= 2× RPM) | Burst size |
| `SECURITY_RATE_LIMIT_DISTRIBUTED` | `false` | Use the PostgreSQL-backed limiter instead of in-memory. Must be `true` in production when RPM > 0 |
| `SECURITY_RATE_LIMIT_FAILURE_MODE` | `closed` if StrictMode or auth is on, else `open` | `open`/`closed`/`local_fallback`. Cannot be `open` while auth is enabled, or in production |
| `SECURITY_RATE_LIMIT_BUCKET_MAX_AGE` | `24h` | How long an idle rate-limit bucket is retained before cleanup |

### Audit

| Variable | Default | Description |
|---|---|---|
| `SECURITY_AUDIT_MODE` | `required` under StrictMode, else `best_effort` | `best_effort`/`required`/`buffered`. `best_effort` is forbidden in production. See [Data handling](../security/data-handling.md#audit) |

### Data at rest and sessions

| Variable | Default | Description |
|---|---|---|
| `SECURITY_DATA_ENCRYPTION_KEY` | empty | Seals SQL text and connection secrets at rest (AES-GCM). ≥32 chars, not a placeholder, if set. Either this or the session secret is required in production |
| `SECURITY_SESSION_SECRET` | empty | HMAC secret for session cookies; also usable as the encryption-key fallback. ≥32 chars, not a placeholder, if set. Required in production when browser OIDC is configured |
| `SECURITY_SESSION_TTL` | `8h` | Browser session lifetime |

### Sharing

| Variable | Default | Description |
|---|---|---|
| `SECURITY_SHARE_LINKS_ENABLED` | `false` | Allows unauthenticated report share links. Forbidden in production until sharing is explicitly hardened |
| `SECURITY_SHARE_LINK_DEFAULT_HOURS` | `168` (7 days) | Default share-link expiry |
| `SECURITY_SHARE_LINK_EXPOSE_SQL` | `false` | Include the underlying SQL in the shared view |

### Query execution

| Variable | Default | Description |
|---|---|---|
| `SECURITY_EXPLAIN_ANALYZE_ENABLED` | `false` | Allows EXPLAIN ANALYZE, which executes the query. Forbidden in production |
| `SECURITY_EXPLAIN_SNAPSHOT_RETENTION_DAYS` | `90` (`0` = forever) | Retention for stored EXPLAIN snapshots, cleaned up every 6h |
| `SECURITY_STAT_STATEMENTS_ENABLED` | `true` | Whether `pg_stat_statements` reads (query stats, regression polling) are attempted |
| `SECURITY_CONNECTION_ALLOWLIST_REQUIRED` | on under StrictMode, else off | When on, an organization with no assignment for a connection is denied instead of allowed by default |

### OIDC

| Variable | Default | Description |
|---|---|---|
| `SECURITY_OIDC_ISSUER` | empty | Corporate IdP issuer URL. When set in production, `SECURITY_OIDC_AUDIENCE` is required |
| `SECURITY_OIDC_AUDIENCE` | empty | Expected JWT audience |
| `SECURITY_OIDC_JWKS_URL` | empty | JWKS endpoint override, when it differs from IdP discovery |
| `SECURITY_OIDC_CLIENT_ID` | empty | OAuth2/OIDC client ID |
| `SECURITY_OIDC_CLIENT_SECRET` | empty | OIDC client secret (omit for public clients) |
| `SECURITY_OIDC_REDIRECT_URL` | `http://localhost:8080/auth/callback` | Callback URL registered at the IdP. An empty string counts as unset and falls back to the localhost default — set this explicitly for any real deployment |
| `SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG` | **`false`** | Auto-provision default-org membership on first OIDC login. **Forbidden (must stay `false`) in production** |

### Schedules and webhooks

| Variable | Default | Description |
|---|---|---|
| `SCHEDULE_RUNNER_ENABLED` | `false` | Enables the background schedule/webhook runner |
| `SCHEDULE_RUNNER_INTERVAL` | `1m` | Poll interval for due schedules |
| `SCHEDULE_DURABLE_LEASES` | `true` | Must stay `true` when the runner is enabled in production |
| `SECURITY_WEBHOOK_SIGNING_SECRET` | empty | HMAC secret for `X-PGQN-Signature`. ≥16 chars, not a placeholder, required when the runner is enabled in production |
| `SECURITY_WEBHOOK_ALLOWED_HOSTS` | empty | Comma-separated webhook destination hosts. **Empty fails closed** — no webhook fires. Required when the runner is enabled in production |

### Regression poller

| Variable | Default | Description |
|---|---|---|
| `REGRESSION_POLLER_ENABLED` | = `SECURITY_STAT_STATEMENTS_ENABLED` | Enables the background regression-detection poller |
| `REGRESSION_POLLER_INTERVAL` | `15m` | Poll interval per (organization, connection) |
| `REGRESSION_MEAN_THRESHOLD_PCT` | `50` | Mean-latency increase that triggers an alert |
| `REGRESSION_HIGH_THRESHOLD_PCT` | `100` | Impact tier: high |
| `REGRESSION_CRITICAL_THRESHOLD_PCT` | `200` | Impact tier: critical |
| `REGRESSION_SNAPSHOT_RETENTION_DAYS` | `7` | Retention for raw statement snapshots |

See [Regressions and applied fixes](../workflows/regressions.md) for how these
combine into an alert.

## LLM {#llm}

Required for Ask, chat, and plain-English SQL explanation; used with a deterministic
fallback for workbench report narratives. Not used by plan findings, candidates,
compare, verification, or investigation reports. See [LLM providers](../integrations/llm.md).

| Variable | Default | Description |
|---|---|---|
| `LLM_PROVIDER` | `ollama` | `ollama`\|`gemini`\|`claude`\|`openai`\|`groq`. Anything other than `ollama`/empty counts as a **cloud** provider |
| `LLM_MODEL` | `llama3.2` | Model name |
| `LLM_BASE_URL` | `http://localhost:11434` | LLM API base URL |
| `LLM_API_KEY` | empty | Required for cloud providers |
| `LLM_ALLOW_EXTERNAL_DATA` | `false` | Must be `true` before any cloud provider can be used at all |
| `LLM_SEND_ROW_DATA` | `false` | Include raw row samples in prompts |
| `LLM_MAX_SAMPLE_ROWS` | `5` (clamped 0–10) | Row sample cap; ≤3 for cloud providers when row data is sent; ≤5 in production regardless |
| `LLM_REDACT_PII` | `true` | Redacts PII patterns and SQL literals before prompting. Must stay `true` in production when a cloud provider is sent row data |
| `LLM_MAX_CALLS_PER_REPORT` | `12` | Auxiliary LLM calls allowed per report |
| `LLM_DAILY_TOKEN_BUDGET` / `LLM_MONTHLY_TOKEN_BUDGET` | `0` (unlimited) | Token budget ceilings |
| `LLM_DAILY_COST_BUDGET_USD` / `LLM_MONTHLY_COST_BUDGET_USD` | `0` | Cost budget ceilings |
| `LLM_PER_USER_DAILY_TOKEN_BUDGET` / `LLM_PER_USER_MONTHLY_TOKEN_BUDGET` | `0` | Per-user token ceilings |
| `LLM_PER_USER_DAILY_COST_BUDGET_USD` / `LLM_PER_USER_MONTHLY_COST_BUDGET_USD` | `0` | Per-user cost ceilings |
| `LLM_USD_PER_1K_TOKENS` | `0.002` | Rate used to convert tokens into the cost budgets above |
| `LLM_BUDGET_FAIL_CLOSED` | `true` for a cloud provider or under StrictMode, else `false` | Deny LLM calls (rather than allow) when the budget ledger is unreachable. Must be `true` for cloud providers in production |

## Embeddings {#embeddings}

Used for `GET /suggestions/similar`, `GET /reports/similar`, and RAG context in
report generation. When unset, saving still works but no vectors are stored.

| Variable | Default | Description |
|---|---|---|
| `EMBEDDING_BASE_URL` | empty | Embedding API URL. Falls back to `LLM_BASE_URL` when `LLM_PROVIDER=ollama` |
| `EMBEDDING_MODEL` | `nomic-embed-text` | Embedding model |

See [Semantic search (pgvector)](../integrations/semantic-search.md).

## MCP {#mcp}

Not part of `Load()` — the MCP server is a separate binary reading its own
environment. See [MCP server](../integrations/mcp.md#what-it-is) for the full list
(`PGQUERYNARRATIVE_URL`, `PGQUERYNARRATIVE_API_KEY`).

## Metrics

Analytics windows and thresholds; shown read-only in **Settings → Analytics**.
Out-of-range values are clamped at load, never rejected.

| Variable | Default | Range |
|---|---|---|
| `METRICS_TREND_PERIODS` | `6` | 2–24 |
| `METRICS_MOVING_AVG_WINDOW` | `3` | 2–24 |
| `METRICS_MAX_TIMESERIES_PERIODS` | `24` | 2–120 |
| `METRICS_MAX_SEASONAL_LAG` | `12` | 2–24 |
| `METRICS_MIN_PERIODS_FOR_SEASONALITY` | `12` | ≥ 2 |
| `PERIOD_TREND_THRESHOLD_PERCENT` | `0.5` | any |
| `METRICS_ANOMALY_SIGMA` | `2.0` | 1–5 |
| `METRICS_ANOMALY_METHOD` | `zscore` | `zscore`\|`isolation_forest` |
| `METRICS_CONFIDENCE_LEVEL` | `0.95` | 0.5–0.99 |
| `METRICS_CORRELATION_MIN_ROWS` | `10` | 2–1000 |
| `METRICS_SMOOTHING_ALPHA` | `0.3` | (0, 1] |
| `METRICS_SMOOTHING_BETA` | `0.1` | [0, 1] |

## Environment mode (outside `Load()`)

| Variable | Default | Description |
|---|---|---|
| `APP_ENV` | empty | `production`/`prod` (case-insensitive) enables StrictMode; `demo` enables [`DemoMode`](../trust-model.md#demo-versus-your-database) (fabricated workspace KPIs and seeded regression alerts) |
| `SECURITY_STRICT` | `false` | Alternative StrictMode switch, independent of `APP_ENV` |

## Production

See [Production configuration](../operate/production.md) for the complete list of
StrictMode requirements — it is generated from the same validation function this
page's defaults come from, so treat that page as the authoritative gate and this
page as the variable dictionary.

## See also

[Installation](../getting-started/installation.md) · [API reference](api.md) ·
[Deployment](../operate/deployment.md) · [Documentation index](../index.md)
