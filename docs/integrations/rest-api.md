# REST API

Full endpoint-by-endpoint contract: [API reference](../reference/api.md) and
[API errors](../reference/api-errors.md), generated from, and checked against, the
Goa design and the running routes. This page covers auth, the flagship flow with
runnable curl, and the OpenAPI spec.

Base URL: `http://localhost:8080/api/v1` (port from [Configuration](../reference/configuration.md#server)).
All bodies are JSON.

## Authentication

When `SECURITY_AUTH_ENABLED=true`, most `/api/*` routes (and `/metrics` and the
non-shared report export routes) require one of:

- `Authorization: Bearer <SECURITY_API_KEY>` (dev; rejected in production, use a hash)
- `Authorization: Bearer <key>` checked against `SECURITY_API_KEY_HASH` or
  `SECURITY_API_KEYS_JSON` (managed keys, with role and scope)
- A session cookie (browser OIDC login)
- An OIDC bearer JWT

`/health`, `/ready`, `/ready/connections`, `/version` and
`GET /reports/shared/{token}` are never protected. Roles (`admin`, `analyst`,
`viewer`) gate which write paths a caller may use, see
[Authentication and roles](../security/authentication.md). Rate limiting
(`SECURITY_RATE_LIMIT_RPM`) returns 429.

## OpenAPI 3

The canonical machine-readable contract is generated from the Goa design:
[`api/gen/http/openapi3.json`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/api/gen/http/openapi3.json) /
`.yaml`. The frontend's TypeScript types
(`frontend/src/api/schema.gen.ts`) are generated from that same spec by
`tools/openapi-ts`, so the UI, the spec and this documentation trace back to one
source. See [Repository architecture](../development/repository.md#code-generation).

## Query Investigation (flagship flow)

`demo.sales` is a **rolling window ending today**, so a hard-coded calendar date
returns zero rows once the window moves past it. Take SQL from the demo scenarios
endpoint, which injects date literals from the live data range:

```bash
# 0) Problem SQL with a date that is actually in the seeded range
SQL=$(curl -s http://localhost:8080/api/v1/demo/scenarios | jq -r '.items[0].sql')

# 1) Create: returns id, plan evidence, findings
INV=$(curl -s -X POST http://localhost:8080/api/v1/investigations \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg sql "$SQL" '{title: "Slow dashboard query", sql: $sql}')")
printf '%s' "$INV" | jq '{id, status, findings: [.explain.findings[]?.message][:3]}'
ID=$(printf '%s' "$INV" | jq -r .id)

# 2) System-proposed rewrite (AST engine; no SQL executes)
SUGGEST=$(curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/suggest-rewrite" \
  -H "Content-Type: application/json" -d "{}")
printf '%s' "$SUGGEST" | jq '{candidates: [.candidates[]? | {sql, rationale, category}]}'
REWRITE=$(printf '%s' "$SUGGEST" | jq -r '.candidates[0].sql')

# 3) Compare plans + verify results (executes both queries; needs the `query` permission)
curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/candidate" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg sql "$REWRITE" '{candidate_sql: $sql, analyze: true, verify_results: true}')" \
  | jq '{status, equivalence: .comparison.result_equivalence_status, metrics: .comparison.metrics}'

# 4) Engineering report: requires VerifiedEqual, or SampleMatch + accept_sample_match=true
curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/report" \
  -H "Content-Type: application/json" -d "{}" | jq '{status, report_id}'
```

See [Verify result equivalence](../workflows/verify-results.md) for what
`result_equivalence_status` values mean and when the report call above needs
`?accept_sample_match=true` instead.

Optional: rank rewrite + index-DDL candidates with dry EXPLAIN. `analyze` is a query
parameter, not a body field; setting it in the JSON body has no effect and the
default (`false`) applies:

```bash
curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/rank-candidates?analyze=true" \
  | jq '{candidates: [.candidates[]? | {sql, rationale, total_cost, partitions_scanned}], recommendation}'
```

List / fetch, and the workspace helpers (landing summary, regression inbox, guided
scenarios, trust snapshot):

```bash
curl -s 'http://localhost:8080/api/v1/investigations?limit=5' | jq .
curl -s "http://localhost:8080/api/v1/investigations/${ID}" | jq '{id, status, candidate_sql, report_id}'
curl -s http://localhost:8080/api/v1/workspace/overview | jq .
curl -s http://localhost:8080/api/v1/workspace/regressions | jq .
curl -s http://localhost:8080/api/v1/trust | jq .
```

## Compare plans standalone (no investigation record)

```bash
BEFORE='SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('\''month'\'', date) = '"'"'2026-08-01'"'"' GROUP BY product_category ORDER BY revenue DESC'
AFTER='SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE date >= '"'"'2026-08-01'"'"' AND date < '"'"'2026-09-01'"'"' GROUP BY product_category ORDER BY revenue DESC'

curl -s -X POST http://localhost:8080/api/v1/queries/explain/compare \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg b "$BEFORE" --arg a "$AFTER" '{before_sql: $b, after_sql: $a, analyze: true, connection_id: "default"}')" | jq .
```

Replace the literal month above with one from `GET /api/v1/demo/scenarios` if you're
running this against a seed older than the example. On the 10M-row seed
(`make demo-bootstrap`), expect **Partitions scanned** to move from many toward 1.

## Run query, EXPLAIN

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category","limit":10}'
```

Response includes `columns`, `rows`, `row_count`, `execution_time_ms` (this field
**does** exist on run/candidate results; it's only the EXPLAIN response that
doesn't have it), optional `chart_suggestions` and `period_comparison`.

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category"}' \
  | jq '{evidence_mode, planning_time_ms, seq_scans: ([.findings[] | select(.is_seq_scan)] | length)}'
```

Set `"analyze": true` for `EXPLAIN (ANALYZE, FORMAT JSON)`, only when the server
allows it (`SECURITY_EXPLAIN_ANALYZE_ENABLED`). Field meanings:
[Evidence and status vocabulary](../reference/evidence.md#timing-fields).

## Semantic search over saved queries

Requires [embeddings](semantic-search.md) and at least one saved query:

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/saved \
  -H "Content-Type: application/json" \
  -d '{"name":"North rollup","sql":"SELECT region, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY region"}'

curl -s 'http://localhost:8080/api/v1/suggestions/similar?text=regional%20revenue%20breakdown&limit=5' | jq .
```

## List connections

```bash
curl -s http://localhost:8080/api/v1/connections
# {"items":[{"id":"default","name":"Default"}]}
```

## See also

- [API reference](../reference/api.md): every route, request and response shape
- [API errors](../reference/api-errors.md)
- [Connect your PostgreSQL](../getting-started/connect-postgres.md)
- [Docs overview](../index.md)
