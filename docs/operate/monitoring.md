# Health and monitoring

## Health and readiness {#health-and-readiness}

| Endpoint | Auth | Behavior |
|---|---|---|
| `GET /health` | Never protected | Always `200 OK` once the process is up. Liveness only, restart on failure |
| `GET /ready` | Never protected | `200` when the app metadata pool responds and the schema migration version is current and clean; otherwise **503** with the reason (behind schema, or dirty). Add `?format=json` for a per-pool breakdown |
| `GET /ready/connections` | Never protected | Always `200`. JSON `{"pools": [{"name", "role", "ready", "lazy", "initialized", "error"}]}` for every configured connection, a diagnostic view, not a gate |
| `GET /version` | Never protected | `{"version": "..."}` |
| `GET /metrics` | Protected when auth is on | JSON `{"version", "pool"}` by default; add `?format=prometheus` (or an `Accept` header naming `text/plain`/`openmetrics`) for Prometheus text exposition |

| Environment | Probe | Target |
|---|---|---|
| Docker Compose | healthcheck | `GET /ready` |
| Kubernetes / Helm | `livenessProbe` | `GET /health` |
| Kubernetes / Helm | `readinessProbe` | `GET /ready` |

**Limitation:** none of these probes check LLM reachability; a workbench report can
fail with the app reporting healthy. Watch report error rates separately.

## Metrics

Hand-rolled Prometheus exposition (`app/observability`), not client_golang, so metric
names are stable strings rather than generated:

| Group | Metrics |
|---|---|
| Build / latency | `pgqn_info`, `pgqn_http_request_duration_seconds` (histogram) |
| HTTP / auth / audit | `pgqn_http_requests_total`, `pgqn_http_errors_total`, `pgqn_auth_failures_total`, `pgqn_authz_denials_total`, `pgqn_audit_write_failures_total`, `pgqn_rate_limit_storage_failures_total` |
| Query / LLM | `pgqn_query_runs_total`, `pgqn_query_timeouts_total`, `pgqn_llm_calls_total`, `pgqn_llm_tokens_total`, `pgqn_llm_budget_denials_total`, `pgqn_llm_budget_fail_closed_total`, `pgqn_llm_budget_reservations_expired_total` |
| Scheduler / webhooks | `pgqn_scheduler_runs_total`, `pgqn_scheduler_failures_total`, `pgqn_schedule_dead_letters_total`, `pgqn_schedule_lease_recoveries_total`, `pgqn_webhook_deliveries_total`, `pgqn_webhook_failures_total`, `pgqn_webhook_dead_letters_total`, `pgqn_webhook_rejections_total` |
| Embeddings | `pgqn_embed_calls_total`, `pgqn_embed_denials_total`, `pgqn_embed_errors_total`, `pgqn_embed_latency_avg_ms` |
| Pools | `pgqn_pool_acquired_conns`, `pgqn_pool_idle_conns`, `pgqn_pool_total_conns`, `pgqn_pool_max_conns` |

A ready-made dashboard using all of these lives at
[`deploy/grafana/pgquerynarrative-overview.json`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/deploy/grafana/pgquerynarrative-overview.json).

## Alerts

Defined in [`deploy/prometheus/alerts.yml`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/deploy/prometheus/alerts.yml):

| Alert | Condition | For |
|---|---|---|
| `PgqnHighHTTPErrorRate` | Error ratio > 5% | 10m |
| `PgqnAuthFailureSpike` | Auth failure rate > 1/s | 5m |
| `PgqnQueryTimeoutSpike` | Timeout rate > 0.2/s | 10m |
| `PgqnLLMBudgetDenials` | > 5 denials in 15m | 5m |
| `PgqnPoolSaturation` | Acquired/max connections > 85% | 10m |
| `PgqnSchedulerFailures` | Any failure in 15m | 5m |
| `PgqnScheduleDeadLetters` | Any dead letter in 30m | 1m |
| `PgqnWebhookFailures` | > 3 failures in 15m | 5m |
| `PgqnWebhookDeadLetters` | Any dead letter in 30m | 1m |

## What to watch, in practice

| Area | Watch | How |
|---|---|---|
| Application | Process up, HTTP responding | `/health`, `/ready`; page on N consecutive failures |
| Database pools | Saturation, connection errors | `pgqn_pool_*`, `PgqnPoolSaturation`, `/ready/connections` |
| Query safety | Timeout rate, auth failures | `PgqnQueryTimeoutSpike`, `PgqnAuthFailureSpike` |
| LLM | Budget denials, report failures | `PgqnLLMBudgetDenials`, report error rate in logs (not covered by `/ready`) |
| Schedules / webhooks | Failures, dead letters | `PgqnSchedulerFailures`, `PgqnScheduleDeadLetters`, `PgqnWebhookFailures`, `PgqnWebhookDeadLetters` |
| Logs | Errors, slow requests, pool/report failures | stdout/stderr → your aggregator |
| Resources | Memory, CPU | Container/pod metrics |

## See also

[Deployment](deployment.md) · [Migrations, upgrades, backup](upgrades.md) ·
[Troubleshooting and runbooks](troubleshooting.md) · [Configuration reference](../reference/configuration.md)
