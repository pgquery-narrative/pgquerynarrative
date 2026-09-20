# Troubleshooting and runbooks

Common issues, then incident runbooks for production. See also
[Deployment](deployment.md), [Health and monitoring](monitoring.md), and
[Migrations, upgrades, backup](upgrades.md).

---

## Environment and dependencies

| Issue | Solution |
|---|---|
| Docker not found | Install Docker Desktop; verify with `docker info` |
| `make: command not found` | Install Make (`brew install make` on macOS) |
| Port 8080 in use | `PGQUERYNARRATIVE_PORT=8081 make start-docker` (or `start-local`) |
| `go mod tidy` / `make lint`: permission denied | The Makefile sets `GOMODCACHE=$(HOME)/.gomodcache`; outside Make, `export GOMODCACHE=$HOME/.gomodcache` |

## Database {#database}

| Issue | Solution |
|---|---|
| PostgreSQL connection refused | Docker: `make start-docker`. Local: start Postgres, then `make start-local` |
| Role does not exist / permission denied | `make db-init` then `make migrate`; if `demo.sales` denied, grant `SELECT` to the readonly role |
| `/ready` returns 503 "schema migration version N < required" | The database is behind — run migrations. See [Migrations, upgrades, backup](upgrades.md) |
| `/ready` returns 503 "dirty at version N" | A prior migration failed partway — inspect it, fix by hand, then `migrate force <version>` before `up` |
| Fresh database: "permission denied to create extension" at migration 000019 | No `DATABASE_MIGRATION_USER` set — see [Database roles](../security/database-roles.md) |
| `CONNECTION_NOT_FOUND` on a `connection_id` you expected to work | The id isn't configured. It fails closed rather than silently falling back — see [Multiple connections](../workflows/connections.md#resolving-connection_id) |

## Reports and LLM {#reports-and-llm}

| Issue | Solution |
|---|---|
| Failed to parse narrative JSON | LLM output may be truncated — ensure Ollama is running and the model is pulled |
| Report generation fails or times out | Check `LLM_BASE_URL`/`LLM_PROVIDER`/`LLM_MODEL`/`LLM_API_KEY`; Docker + host Ollama needs `LLM_BASE_URL=http://host.docker.internal:11434` |
| Cloud provider call rejected before any request | `LLM_ALLOW_EXTERNAL_DATA` is not `true` |

## Extension (PostgreSQL)

| Issue | Solution |
|---|---|
| `CREATE EXTENSION pgquerynarrative` fails | Copy the files first — `make install-extension` (local) or `make install-extension-docker` (Docker). See [PostgreSQL extension](../integrations/postgres-extension.md) |
| Functions return `{"status":"pending",...}` | The `http` extension wasn't installed **before** `pgquerynarrative` — install it, then re-run the extension's SQL |
| Functions raise `PgQueryNarrative API error: 401` | The server has auth enabled; the extension sends no API key at all |

---

## Alert runbooks

Anchors below are linked directly from `deploy/prometheus/alerts.yml`'s
`runbook` annotation — keep the `{#id}` on each heading stable even if the
heading text changes.

### HTTP 5xx spike {#http-5xx-spike}

`PgqnHighHTTPErrorRate`: `pgqn_http_errors_total` / `pgqn_http_requests_total` > 5%
for 10 minutes. Check recent deploys first (a bad config or image — see
[Rollback](upgrades.md#rollback)), then application logs for the specific failing
route and its error.

### Auth failure spike {#auth-failure-spike}

`PgqnAuthFailureSpike`: `pgqn_auth_failures_total` rate > 1/s for 5 minutes. Usually
a client using an expired/rotated key, or a credential-stuffing attempt. Check
`app.audit_logs` for the source IPs and affected principals; rotate the key if it
was legitimately compromised.

### Query timeouts {#query-timeouts}

`PgqnQueryTimeoutSpike`: `pgqn_query_timeouts_total` rate > 0.2/s for 10 minutes.
Look for a new slow query pattern (a dashboard change, a new report) or a database
under load; `QUERY_TIMEOUT` can be raised temporarily, but the [regression
inbox](../workflows/regressions.md) is the better long-term signal.

### LLM budget exhausted {#llm-budget-exhausted}

`PgqnLLMBudgetDenials`: more than 5 denials in 15 minutes. A daily/monthly/per-user
budget in [Configuration – LLM](../reference/configuration.md#llm) has been hit.
Raise the relevant `LLM_*_BUDGET*` variable, or wait for the window to roll over.

### DB pool saturation {#db-pool-saturation}

`PgqnPoolSaturation`: acquired/max connections > 85% for 10 minutes on any pool. See
[Pool exhaustion](#pool-exhaustion) below.

### Scheduler stuck / duplicate runs {#scheduler-stuck--duplicate-runs}

`PgqnSchedulerFailures` (any failure in 15m) or `PgqnScheduleDeadLetters` (any dead
letter in 30m). See [Schedule runner problems](#schedule-runner-problems) below.

### Webhook delivery failures / dead letter {#webhook-delivery-failures--dead-letter}

`PgqnWebhookFailures` (> 3 in 15m) or `PgqnWebhookDeadLetters` (any in 30m). See
[Webhook delivery problems](#webhook-delivery-problems) below.

---

## Incident runbooks

### Database (metadata pool) unreachable

**Symptoms:** `/ready` 503, "connection refused" or pool errors in logs, 5xx on
`/api/*`.

**Actions:** confirm Postgres is running and reachable (network, firewall,
credentials). Compose: check the `postgres` service and its logs, restart if
needed. External Postgres: check the instance, connectivity and credentials. Once
it's back, `/ready` should return 200 without a restart.

### Analytical connection unreachable

**Symptoms:** one entry in `GET /ready/connections` shows `"ready": false`; queries
against that `connection_id` fail while others succeed.

**Actions:** `/ready` itself stays 200 — only the app metadata pool gates it — so
this can go unnoticed without checking `/ready/connections` directly. Confirm the
target database, credentials, and network path for that specific connection.

### Schema-version mismatch after a deploy

**Symptoms:** `/ready` 503 citing a version below `RequiredMigrationVersion`.

**Actions:** run migrations for the deployed version (`make migrate` /
`make migrate-docker`, or your migration Job) — see
[Migrations, upgrades, backup](upgrades.md).

### Dirty migration

**Symptoms:** `/ready` 503 citing "dirty at version N".

**Actions:** inspect what the failed migration actually applied, fix the schema by
hand to match, then `migrate force N` to clear the dirty flag, then `migrate up`
again. Never `force` past a migration whose effects you haven't verified.

### OIDC / JWKS problems

**Symptoms:** browser login fails at `/auth/callback`; API bearer JWTs are rejected.

**Actions:** confirm `SECURITY_OIDC_ISSUER`, `SECURITY_OIDC_AUDIENCE`,
`SECURITY_OIDC_JWKS_URL` (if set) and `SECURITY_OIDC_REDIRECT_URL` match the IdP's
registration exactly — an empty `SECURITY_OIDC_REDIRECT_URL` is treated as unset and
defaults to `http://localhost:8080/auth/callback`, which is never right for a real
deployment. See [Authentication and roles](../security/authentication.md).

### Session problems

**Symptoms:** users are logged out unexpectedly; login succeeds but `/auth/session`
reports unauthenticated.

**Actions:** confirm `SECURITY_SESSION_SECRET` hasn't changed (rotating it
invalidates every existing session) and that `SECURITY_SESSION_TTL` matches
expectations. Under StrictMode, session cookies are `Secure` — a login over plain
HTTP behind a misconfigured proxy will silently fail to persist.

### Encryption-key / configuration problems

**Symptoms:** the process refuses to start with a `SECURITY_DATA_ENCRYPTION_KEY` or
`SECURITY_SESSION_SECRET` error; a config error at boot in general.

**Actions:** the message names the exact requirement — see
[Production configuration](production.md) for the full checklist `config.Validate()`
enforces, so you can fix the actual variable rather than trial-and-error.

**`invalid configuration: SECURITY_API_KEYS_JSON: …`:** the key list is checked strictly
and any mistake stops startup (unknown field such as `keyhash`, no `key` or `key_hash`,
a `key_hash` that is not 64 hex characters, a missing or unrecognised `role`,
`expires_at` not in RFC 3339 form, an unknown scope, or an array with no key when no
other credential is set). Fix the entry the message names; the server never starts with
a key list it could only half read. See
[Authentication and roles](../security/authentication.md).

### Audit fail-closed behavior

**Symptoms:** `queries.run`, `reports.generate` or an admin change (API keys, memberships,
connection permissions) start failing with an audit error, with the metadata database
otherwise healthy. If they fail right after an upgrade, confirm migration `000058` has run
(`/ready` reports the schema version): without it the database rejects those event types.

**Actions:** this is `SECURITY_AUDIT_MODE=required` doing its job — these
high-risk actions are refused rather than left unaudited when the audit write
itself fails. Fix the underlying write failure (metadata pool health, disk space);
do not switch to `best_effort` in production to make the symptom go away. See
[Data handling](../security/data-handling.md#audit).

### Distributed rate-limit backend failure

**Symptoms:** a spike in `pgqn_rate_limit_storage_failures_total`; requests start
failing (mode `closed`) or start bypassing limits (mode `local_fallback`).

**Actions:** check the metadata database — the distributed limiter is
PostgreSQL-backed. `SECURITY_RATE_LIMIT_FAILURE_MODE=closed` (required whenever auth
is enabled) means a backend outage here also blocks legitimate requests; that is the
intended trade-off over failing open. Restore the database, or switch to
`local_fallback` deliberately if you accept per-instance limiting during an outage.

### Schedule runner problems

**Symptoms:** `PgqnSchedulerFailures` or `PgqnScheduleDeadLetters` firing; a
schedule stops running.

**Actions:** `GET /schedules/{id}/runs` shows recent run status and errors. A lease
that a crashed replica held is recovered automatically on the next poll — a
schedule stuck for longer than a few lease intervals (5 minutes each) usually means
the query itself is failing, not the runner. Retry a specific run with
`POST /schedule-runs/{run_id}/retry`.

### Webhook delivery problems

**Symptoms:** `PgqnWebhookFailures` or `PgqnWebhookDeadLetters` firing;
`pgqn_webhook_rejections_total` rising.

**Actions:** `GET /webhook-deliveries` lists attempts and failure reasons. A
rejection (as opposed to a delivery failure) usually means the destination host
isn't in `SECURITY_WEBHOOK_ALLOWED_HOSTS`, or resolves to a private/loopback address
the SSRF guard blocks — see [Schedules and webhooks](../workbench/schedules.md#webhook-delivery).
A delivery failure after 5 retries dead-letters; investigate the receiving endpoint.

### Regression poller problems

**Symptoms:** the regression inbox stays empty despite known slow queries; or an
alert never resolves.

**Actions:** confirm `REGRESSION_POLLER_ENABLED` and
`SECURITY_STAT_STATEMENTS_ENABLED` are both true, and that `pg_stat_statements` is
actually installed and tracking on the analytical database. A query needs at least
3 baseline polling intervals before it's eligible to alert at all — a poller
restarted recently will look quiet for a while by design. On a connection whose read-only
role is shared by several organizations the poller deliberately does nothing and logs
`skipping connection`: give each organization its own read-only credentials. See
[Regressions and applied fixes](../workflows/regressions.md).

### Pool exhaustion

**Symptoms:** `PgqnPoolSaturation`; requests queueing or timing out acquiring a
connection.

**Actions:** check `pgqn_pool_acquired_conns` vs `_max_conns` per pool (`GET
/metrics`). Raise `DATABASE_MAX_CONNECTIONS` per connection, or
`DATABASE_GLOBAL_MAX_CONNECTIONS` for the shared budget across all pools, within
what PostgreSQL's own `max_connections` allows across every client of that database.

---

## See also

[Health and monitoring](monitoring.md) · [Migrations, upgrades, backup](upgrades.md) ·
[Configuration reference](../reference/configuration.md) · [Deployment](deployment.md)
