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

## PostgREST request capture

`POST /investigations/from-postgrest` (or **Import a PostgREST request** on the
Investigate page) imports one captured GET or HEAD request to a table, view or
RPC function. It reads the actual generated SQL and bind values from PostgreSQL
logs; it does not fetch a URL or require a running PostgREST instance in
PgQueryNarrative. The capture format is tested with PostgREST 12.2, 13.0 and
16.4 on PostgreSQL 16/18 across common request shapes (filters, embeds, JSON
paths, full-text search, computed columns, RPC, counts, CSV, singular objects,
Range pagination, large `in` lists). PostgREST versions before 13 pass RPC
scalar arguments through one JSON payload parameter the importer casts
explicitly; this is handled automatically and needs no configuration.

**Capture.** Paste stderr, csvlog or jsonlog output. Log with
`log_min_duration_statement=0` or `log_statement=all`, and
`log_parameter_max_length=-1`. A stderr `log_line_prefix` must contain the
backend PID as `[%p]` (for example `'%m [%p] %Q app=%a '`; fixed
managed-service prefixes such as `%t:%r:%u@%d:[%p]:` also work); with
`compute_query_id=on`, a `%Q` directly after `[%p]` is recorded as the query
id. Include one request's transaction. If the excerpt holds several requests,
the error names their backend PIDs so you can keep one. The excerpt is at most
200,000 bytes.

- **Failed requests**, including ones that hit `statement_timeout`, import from
  PostgreSQL's `ERROR` / `STATEMENT` records. Their values come from the bind
  record (`log_min_duration_statement=0`) or the error context
  (`log_parameter_max_length_on_error=-1`). The investigation records the error;
  replays use the product's own timeout, so the statement can still be measured.
- **Slow-query-only logs** (a threshold above the session-context statement's
  duration) import with the session context marked as not logged: method,
  endpoint and role are then unknown.
- A value ending in `...` imports only when a longer, untruncated value in the
  same excerpt proves `log_parameter_max_length` did not cut it.
- Refused: writes, NULL binds (PostgREST does not send them), and
  syslog-forwarded logs, which split and escape records so the exact SQL cannot
  be rebuilt.

The statement with its values substituted must fit `DATABASE_MAX_QUERY_LENGTH`
(default 10,000 bytes: an `in.(...)` list of roughly 2,000 four-digit ids, or
about 250 UUIDs). These source logs can contain credentials and row-filter
values: keep the capture restricted and turn off broad logging after capture.
The importer discards request headers and cookies. SQL and its captured values
are retained as investigation evidence.

```bash
jq -n --rawfile capture capture.txt \
  '{postgres_log: $capture, connection_id: "default", analyze: false}' \
  | curl --fail-with-body -sS http://localhost:8080/api/v1/investigations/from-postgrest \
      -H "Authorization: Bearer $PGQN_API_KEY" \
      -H "Content-Type: application/json" --data-binary @-
```

Use the returned investigation ID with the existing rank, candidate, and report
endpoints. Captured binds are reused automatically in comparisons; conflicting
bind overrides are rejected. Generated response wrappers receive index advice
when applicable and an explicit rewrite-decline reason. Changes to a view,
function, or request shape must be tested at that source.

**Replay context.** Every plan, ranking and comparison for the investigation
runs with the captured `search_path` (only schemas the connection allows, in
captured order), the captured settings such as `timezone` (all but
`statement_timeout`), and the request method and path. It runs as the captured
role only when that role is listed in `SECURITY_POSTGREST_REPLAY_ROLES` and
granted to the analytical role (`GRANT <role> TO <analytical role>`); role-keyed
RLS then applies as it did for the endpoint. **`request.jwt.claims` is never
replayed, for any role, under any circumstance.** The captured log is
caller-supplied text with no verification it came from a real PostgreSQL log;
trusting a claims value from it would let any principal able to import a
PostgREST capture choose the identity a claims-keyed RLS policy (for example
`auth.uid()`) evaluates for, for any row any allowlisted role could ever see.
Role impersonation does not have this problem: Postgres itself independently
checks the `GRANT`, which the capture text cannot forge. Everything the replay
does not reproduce is listed with the investigation and in its report: an
unimpersonated role, claims (always, even under an impersonated role), captured
schemas outside the allowlist (so, for example, `citext` in an `extensions`
schema can resolve differently), request headers, and the endpoint's response
body. Reports never claim endpoint equivalence.

**Plan cache.** For a named prepared statement, the investigation also plans the
parameterized statement with `EXPLAIN (GENERIC_PLAN)` (PostgreSQL 16+) and
states whether PostgreSQL is likely to switch to that generic plan after five
executions, or to keep planning per request. Unnamed statements (PostgREST
`db-prepared-statements=false`) are planned per request. HTTP duration,
PostgREST transaction time, the logged execute-phase duration, and replay plan
estimates/timings remain separate.

`make test-postgrest` verifies the workflow with disposable real servers
(PostgREST 16.4 on PostgreSQL 16 and 18, plus PostgREST 13.0 and 12.2) and
fails if Docker or the containers are unavailable.

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
