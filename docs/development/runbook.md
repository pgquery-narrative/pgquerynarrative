# Dev runbook

Daily developer workflow for running, testing, and shipping PgQueryNarrative. This is **not** a system architecture overview — for product vocabulary see [Concepts](../concepts.md); for structure see the [root README](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/README.md#project-structure).

Also: [Setup](setup.md) · [Testing](testing.md).

---

## Daily dev

| Step | Command |
|------|---------|
| Start DB (Docker) | `make start-docker` then open http://localhost:8080 |
| Or local Postgres | `make start-local` (Postgres must be running) |
| Run app only | `make run` (defaults: port 8080, `demo` schema) |
| Stop | `make stop` (Docker) or Ctrl+C (local) |

---

## Before commit

```bash
make fmt
make lint
make test
```

After editing `api/design/*.go`: `make generate`.

---

## Tests

| What | Command |
|------|---------|
| Unit only | `make test-unit` or `go test ./test/unit/... -v` |
| One package | `go test ./test/unit/app/metrics/... -v` |
| Integration (Docker) | `make test-integration` or `DOCKER_API_VERSION=1.44 go test ./test/integration/... -v` |
| E2E | `make test-e2e` |
| Full suite | `make test` |

---

## Build & release

| What | Command |
|------|---------|
| Binary + frontend | `make build` → `bin/server`, `frontend/dist/` |
| Frontend only | `make build-frontend` |
| MCP server | `make build-mcp` → `bin/mcp-server` |

---

## DB (when needed)

| What | Command |
|------|---------|
| Init DB (Docker) | `make db-init` |
| Migrate | `make migrate` (uses `DB_URL`) |
| Seed demo | `make seed` |
| Example queries | `tools/db/testing-queries.sql` |

---

## Quick API checks

```bash
curl -s http://localhost:8080/health
curl -s http://localhost:8080/api/v1/settings | jq .analytics
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT 1 AS n","limit":10}'
```

---

## Analytics / metrics sanity check

1. Run a time-series query (e.g. monthly sales from `tools/db/testing-queries.sql` query 8).
2. In run response, confirm `metrics.time_series` has `next_period_forecast`, `forecast_ci_lower`, `forecast_ci_upper`.
3. For correlations: use a query with ≥2 numeric measures and ≥10 rows; check `metrics.correlations`.
4. Settings → Analytics in UI shows confidence, smoothing, anomaly method, etc.

Full steps: [Runbook: Testing analytics features](testing.md#runbook-testing-analytics-features).

---

## SQL queries to test performance analytics

Use these in the Query Runner or `POST /api/v1/queries/run`. All use the `demo` schema.

**1. Time-series + forecast + CI + moving average** (one measure, many periods)

```sql
SELECT date_trunc('month', date)::date AS month,
       SUM(total_amount) AS monthly_total
FROM demo.sales
GROUP BY date_trunc('month', date)
ORDER BY month;
```

**2. Time-series + correlations** (two numeric measures, ≥10 rows → Pearson/Spearman)

```sql
SELECT date_trunc('month', date)::date AS month,
       SUM(total_amount) AS monthly_total,
       SUM(quantity) AS units_sold
FROM demo.sales
GROUP BY date_trunc('month', date)
ORDER BY month;
```

**3. Correlations only** (no date column; two measures, ≥10 rows)

```sql
SELECT product_category,
       SUM(total_amount) AS total,
       COUNT(*) AS orders,
       ROUND(AVG(total_amount), 2) AS avg_order
FROM demo.sales
GROUP BY product_category
ORDER BY total DESC;
```

**4. Anomaly detection** (time-series with one obvious outlier — e.g. one period much higher)

```sql
SELECT date_trunc('month', date)::date AS month,
       SUM(total_amount) AS monthly_total
FROM demo.sales
GROUP BY date_trunc('month', date)
ORDER BY month;
```

If your seed data is smooth, temporarily add a spike for testing, e.g.:

```sql
SELECT * FROM (
  SELECT date_trunc('month', date)::date AS month, SUM(total_amount) AS monthly_total
  FROM demo.sales GROUP BY 1
  UNION ALL
  SELECT '2099-01-01'::date, 999999.0
) t ORDER BY 1;
```

**5. Daily time-series** (more points → better chance of seasonal period / smoothing)

```sql
SELECT date::date AS day,
       SUM(total_amount) AS daily_total,
       SUM(quantity) AS daily_units
FROM demo.sales
GROUP BY date::date
ORDER BY day;
```

---

## Example: Test performance analytics

**Prereq:** App running (`make run`), demo DB with data (`make migrate` and `make seed` if needed). LLM configured if you use report generation (for narrative).

**Quick check with Run (period comparison only)**  
`POST /api/v1/queries/run` returns `period_comparison` (current vs previous, trend), not full metrics. To verify time-series is detected:

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT date_trunc('\''month'\'', date)::date AS month, SUM(total_amount) AS monthly_total, SUM(quantity) AS units_sold FROM demo.sales GROUP BY 1 ORDER BY 1","limit":100}' \
  | jq '{period_comparison, period_current_label, period_previous_label}'
```

You should see `period_comparison` (array with current/previous/trend per measure), and the two period labels.

**Full metrics (forecast, CI, correlations, anomalies) — use Reports**  
Full analytics live in **reports** only. Generate a report, then inspect `metrics`:

```bash
# 1. Generate report (requires LLM; may take a few seconds)
RESP=$(curl -s -X POST http://localhost:8080/api/v1/reports/generate \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT date_trunc('\''month'\'', date)::date AS month, SUM(total_amount) AS monthly_total, SUM(quantity) AS units_sold FROM demo.sales GROUP BY 1 ORDER BY 1"}')
ID=$(printf \'%s\' "$RESP" | jq -r '.id')
# 2. Get report and spot-check metrics
curl -s "http://localhost:8080/api/v1/reports/${ID}" | jq '{
  forecast: .metrics.time_series.monthly_total.next_period_forecast,
  ci: [.metrics.time_series.monthly_total.forecast_ci_lower, .metrics.time_series.monthly_total.forecast_ci_upper],
  correlations: (.metrics.correlations | length),
  anomalies: (.metrics.time_series.monthly_total.anomalies | length)
}'
```

You should see `forecast`, `ci` (two numbers), `correlations` ≥ 1 (one pair for monthly_total vs units_sold), and `anomalies` (0 or more). If report generation is not set up, use the UI: run the same SQL in Query Runner, then **Generate report**, and open the report to see metrics and export.

---

## Troubleshooting

| Issue | Try |
|-------|-----|
| Port in use | Change `PORT` or `PGQUERYNARRATIVE_PORT` (e.g. `PORT=8081 make run`) |
| DB connection failed | Check `DB_URL` / `DATABASE_*` env; ensure Postgres is up and migrations run |
| Integration tests skip | Docker required; `DOCKER_API_VERSION=1.44 make test-integration` |
| Lint fails (G203, etc.) | See [Testing](testing.md); known suppressions documented in code |
| Goa / gen out of sync | `make generate` after any `api/design/*.go` change |

---

## See also

- [Setup](setup.md) · [Testing](testing.md) · [Configuration](../configuration.md) · [Documentation index](../index.md)
