# Schedules and webhooks

Run a saved query (or ad hoc SQL) on a recurring interval and deliver the result.

## Enabling the runner

```bash
SCHEDULE_RUNNER_ENABLED=true
SCHEDULE_RUNNER_INTERVAL=1m          # poll interval, default 1m
SCHEDULE_DURABLE_LEASES=true         # default; required in production when the runner is on
SECURITY_WEBHOOK_ALLOWED_HOSTS=hooks.example.com   # required for any webhook destination
SECURITY_WEBHOOK_SIGNING_SECRET=...  # required for webhook destinations in production
```

Off by default. In production StrictMode, enabling it also requires the webhook
allowlist and signing secret above — see [Production configuration](../operate/production.md).

## Creating a schedule

```bash
curl -s -X POST http://localhost:8080/api/v1/schedules \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Daily revenue",
    "saved_query_id": "...",
    "interval_expr": "@every 24h",
    "destination_type": "webhook",
    "destination_target": "https://hooks.example.com/pgqn",
    "enabled": true
  }'
```

- `interval_expr` uses the `@every <duration>` format (e.g. `@every 6h`) — not cron.
- `destination_type` is `webhook` or `log` (`log` writes the run result to the
  application log instead of delivering anywhere; useful for testing a schedule
  without exposing a URL).

`GET /schedules/{id}/runs` lists past runs; `POST /schedules/{id}/run` triggers one
immediately; `POST /schedule-runs/{run_id}/retry` retries a failed run;
`GET /webhook-deliveries` lists delivery attempts across schedules.

## Multi-replica safety

The runner claims due schedules with `FOR UPDATE SKIP LOCKED` and a 5-minute lease,
renewed by a heartbeat at a third of the lease length. A lease that expires without
a heartbeat (a crashed replica) is detected and recovered by another replica on its
next poll, so running more than one server instance does not double-fire a schedule.

## Webhook delivery {#webhook-delivery}

Deliveries are queued in an outbox table and claimed the same `SKIP LOCKED` way.
Each attempt:

- Requires the destination host to be in `SECURITY_WEBHOOK_ALLOWED_HOSTS` — an empty
  allowlist fails closed, so no webhook fires until you set one.
- Is sent over **HTTPS only**, to port 443 or 8443, with the resolved IP re-checked
  at dial time and private/reserved ranges blocked (SSRF protection). No redirects
  are followed and no proxy is used.
- Carries `X-PGQN-Delivery-ID`, `X-PGQN-Timestamp`, and — when
  `SECURITY_WEBHOOK_SIGNING_SECRET` is set — `X-PGQN-Signature`, an HMAC-SHA256 over
  the timestamp, delivery ID and body. Verify all three on the receiving end.
- The delivery ID is derived only from the schedule run's ID, so regenerating the
  same run's report never produces a second, differently-IDed delivery — safe to
  retry deduplication on it.

Failed deliveries retry with backoff starting at 30 seconds, doubling up to a 30
minute cap, for up to 5 attempts, then move to a dead letter for manual triage
(`GET /webhook-deliveries`). Metrics: `pgqn_webhook_deliveries_total`,
`pgqn_webhook_failures_total`, `pgqn_webhook_dead_letters_total`,
`pgqn_webhook_rejections_total` (an allowlist or SSRF rejection). Alerts:
`PgqnWebhookFailures`, `PgqnWebhookDeadLetters` — see [Health and monitoring](../operate/monitoring.md).

## See also

[Production configuration](../operate/production.md) ·
[Health and monitoring](../operate/monitoring.md) · [REST API](../integrations/rest-api.md)
