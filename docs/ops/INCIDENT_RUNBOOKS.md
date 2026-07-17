# Incident runbooks — PgQueryNarrative

Operational playbooks for production incidents. Pair with Prometheus alerts in
`deploy/prometheus/alerts.yml` and the Grafana dashboard
`deploy/grafana/pgquerynarrative-overview.json`.

Scrape metrics from `GET /metrics?format=prometheus` (auth required when
`SECURITY_AUTH_ENABLED=true`).

---

## HTTP 5xx spike

**Symptoms:** `pgqn_http_errors_total` rising; users see 500s.

1. Check `/ready` and app logs for panics or DB errors.
2. Confirm Postgres connectivity and `schema_migrations` is not dirty.
3. If errors concentrate on `/api/v1/reports/generate` or Ask, check LLM provider health (`LLM_BASE_URL` / API key).
4. Mitigate: scale replicas only after confirming schedule runner + rate-limit distributed mode (`SECURITY_RATE_LIMIT_DISTRIBUTED=true`).
5. Rollback recent deploy if error rate started at release time.

---

## Auth failure spike

**Symptoms:** `pgqn_auth_failures_total` rising; 401 responses.

1. Confirm IdP / JWKS availability (`SECURITY_OIDC_ISSUER`, JWKS URL).
2. Check clock skew on app hosts (JWT `exp`/`nbf`).
3. Verify API keys were not rotated without updating clients.
4. For browser OIDC: confirm `/auth/login` → callback still issues session; check `SECURITY_SESSION_SECRET` unchanged.
5. If membership denials (`403 no organization membership`), inspect `app.organization_members` and `SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG`.

---

## Query timeouts

**Symptoms:** `pgqn_query_timeouts_total` rising; `TIMEOUT_ERROR` in API.

1. Identify slow SQL via `pg_stat_statements` API or Postgres.
2. Confirm `QUERY_TIMEOUT` / statement_timeout are intentional.
3. Check replica lag and disk IO on the analytics database.
4. Temporarily lower concurrency (`DATABASE_MAX_CONNECTIONS`) if pool waits amplify timeouts.
5. Advise analysts to add filters / use EXPLAIN findings before re-running heavy queries.

---

## Scheduler stuck / duplicate runs

**Symptoms:** schedules not advancing; `last_status=failed`; or duplicate reports.

1. Query:
   ```sql
   SELECT id, status, worker_id, lease_until, attempt_count, failure_code
   FROM app.schedule_runs
   WHERE status IN ('running','failed','dead_letter')
   ORDER BY created_at DESC LIMIT 50;
   ```
2. Expired leases are reclaimed by the runner; confirm `SCHEDULE_RUNNER_ENABLED=true` on exactly the intended replicas.
3. Idempotency key is `schedule_id:unix(next_run_at)` — duplicate workers should `ON CONFLICT DO NOTHING`.
4. For `dead_letter`, fix the underlying SQL/webhook, then re-enable the schedule or use Run Now.
5. Clear stuck locks if a worker crashed mid-deploy:
   ```sql
   UPDATE app.schedules SET locked_by = NULL, locked_until = NULL
   WHERE locked_until < NOW();
   ```

---

## Webhook delivery failures / dead letter

**Symptoms:** destinations not receiving payloads; `app.webhook_deliveries.status` = `failed`/`dead_letter`.

1. Inspect:
   ```sql
   SELECT id, destination_url, status, attempt_count, http_status, error_message, completed_at
   FROM app.webhook_deliveries
   WHERE status IN ('failed','dead_letter')
   ORDER BY completed_at DESC LIMIT 50;
   ```
2. Confirm SSRF blocks are expected for private IPs; use public HTTPS endpoints on 443/8443.
3. Verify `SECURITY_WEBHOOK_SIGNING_SECRET` matches receiver validation.
4. Retry worker re-attempts failed rows up to 5 times with backoff; after that status becomes `dead_letter`.
5. After fixing the receiver, insert a new schedule run or use Run Now (new idempotency key).

---

## LLM budget exhausted

**Symptoms:** `pgqn_llm_budget_denials_total` increasing; narrative/Ask returns budget errors.

1. Check org usage:
   ```sql
   SELECT * FROM app.llm_budget_usage
   WHERE usage_date = CURRENT_DATE
   ORDER BY prompt_tokens + completion_tokens DESC;
   ```
2. Raise `LLM_DAILY_TOKEN_BUDGET` / `LLM_DAILY_COST_BUDGET_USD` only with policy approval.
3. Reduce spend: keep `LLM_SEND_ROW_DATA=false`, prefer local Ollama, lower Ask/report volume.
4. Audit classes in `app.llm_audit_events` for unexpected `row_values` / cloud sends.

---

## DB pool saturation

**Symptoms:** `pgqn_pool_acquired_conns / pgqn_pool_max_conns` high; request latency up.

1. Inspect `pg_stat_activity` for long transactions and idle-in-transaction sessions.
2. Confirm `QUERY_IDLE_IN_TX_TIMEOUT` is set.
3. Increase `DATABASE_MAX_CONNECTIONS` cautiously vs Postgres `max_connections`.
4. Check for connection leaks (missing `Rows.Close` / hung LLM holding request context).
5. Scale horizontally only with distributed rate limiting and durable schedule leases enabled.

---

## Credential rotation

**When:** scheduled rotation, suspected compromise, or offboarding automation credentials.

1. **API keys:** add a new entry to `SECURITY_API_KEYS_JSON` (prefer `key_hash`), deploy, update clients, then set `"revoked": true` on the old entry or remove it.
2. **OIDC client secret:** rotate in the IdP admin console, update `SECURITY_OIDC_CLIENT_SECRET`, rolling-restart app replicas.
3. **Session cookies:** rotate `SECURITY_SESSION_SECRET` during a maintenance window — all browser sessions invalidate and users must re-login via `/auth/login`.
4. **Webhook signing:** rotate `SECURITY_WEBHOOK_SIGNING_SECRET`; update receivers before removing the old secret.
5. **Database passwords:** rotate app/readonly roles in Postgres, update secrets, rolling-restart (pools reconnect lazily).
6. Verify: `make db-security-verify-docker`, `make pilot-acceptance`, and `/auth/login` when OIDC is enabled.

---

## Cross-org IDOR suspicion

**Symptoms:** user reports seeing another team's saved query/report.

1. Confirm request principal org via logs / `/auth/session`.
2. Reproduce with two API keys having different `org_id` in `SECURITY_API_KEYS_JSON`.
3. Verify app queries filter `organization_id` and that `app.current_org_id` is set (OrgScoped pool).
4. Check RLS:
   ```sql
   SHOW app.current_org_id;
   SELECT * FROM app.saved_queries; -- should be empty without setting
   SELECT set_config('app.current_org_id', '<org-uuid>', true);
   SELECT * FROM app.saved_queries;
   ```
5. Treat as Sev-1: revoke keys, rotate session secret, preserve audit logs.
