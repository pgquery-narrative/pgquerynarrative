# API examples

Base URL: `http://localhost:8080` (see [Configuration](../configuration.md)). Full endpoint list: [API reference](README.md).

Product vocabulary: [Concepts](../concepts.md).

---

## Query Investigation (flagship)

Create an investigation from SQL, get a **system-proposed rewrite**, compare plans, then generate a report.

`demo.sales` is seeded as a **rolling window** ending today (365 days for the
default seed, 730 for `make seed-large-docker`), so a hardcoded calendar date
returns zero rows once the window moves past it. Take the SQL from the demo
scenarios endpoint, which injects date literals from the live data range:

```bash
# 0) Problem SQL with a date that is actually in the seeded range
SQL=$(curl -s http://localhost:8080/api/v1/demo/scenarios | jq -r '.items[0].sql')

# 1) Create — returns id, explain findings, status
INV=$(curl -s -X POST http://localhost:8080/api/v1/investigations \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg sql "$SQL" '{title: "Slow dashboard query", sql: $sql}')")
printf \'%s\' "$INV" | jq '{id, status, findings: [.explain.findings[]?.message][:3]}'
ID=$(printf \'%s\' "$INV" | jq -r .id)

# 2) System-proposed rewrite (AST engine)
SUGGEST=$(curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/suggest-rewrite" \
  -H "Content-Type: application/json" \
  -d "{}")
printf \'%s\' "$SUGGEST" | jq '{candidates: [.candidates[]? | {sql, rationale, category}]}'
REWRITE=$(printf \'%s\' "$SUGGEST" | jq -r '.candidates[0].sql')

# 3) Compare plans + equivalence (set "analyze": true when server allows EXPLAIN ANALYZE)
curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/candidate" \
  -H "Content-Type: application/json" \
  -d "$(jq -n --arg sql "$REWRITE" '{candidate_sql: $sql, analyze: true}')" \
  | jq '{status, equivalence: .comparison.equivalence, comparison: .comparison.metrics}'

# 4) Engineering report (requires equivalence Equal)
curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/report" \
  -H "Content-Type: application/json" \
  -d "{}" | jq '{status, report_id}'
```

Optional: rank rewrite + index DDL candidates with dry EXPLAIN:

```bash
curl -s -X POST "http://localhost:8080/api/v1/investigations/${ID}/rank-candidates" \
  -H "Content-Type: application/json" \
  -d '{"analyze": false}' \
  | jq '{ranked: [.ranked[]? | {sql, rationale, total_cost, partitions_scanned}]}'
```

List / fetch:

```bash
curl -s 'http://localhost:8080/api/v1/investigations?limit=5' | jq .
curl -s "http://localhost:8080/api/v1/investigations/${ID}" | jq '{id, status, candidate_sql, report_id}'
```

Workspace helpers (landing / inbox / guided scenarios / trust):

```bash
curl -s http://localhost:8080/api/v1/workspace/overview | jq .
curl -s http://localhost:8080/api/v1/demo/scenarios | jq .
curl -s http://localhost:8080/api/v1/trust | jq .
```

---

## Compare plans (standalone)

Same before/after proof without an investigation record:

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain/compare \
  -H "Content-Type: application/json" \
  -d '{
    "before_sql": "SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('\''month'\'', date) = DATE '\''2026-08-01'\'' GROUP BY product_category ORDER BY revenue DESC",
    "after_sql": "SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE date >= '\''2026-08-01'\'' AND date < '\''2026-09-01'\'' GROUP BY product_category ORDER BY revenue DESC",
    "analyze": true,
    "connection_id": "default"
  }' | jq .
```

On the 10M-row seed, expect a **Partitions scanned** style metric moving from many partitions toward **1**.

---

## Run query

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category","limit":10,"connection_id":"default"}'
```

Response: `columns`, `rows`, `row_count`, `execution_time_ms`, optional `chart_suggestions`, `period_comparison` (when result has a time column and measures).

---

## Explain query plan

Requires a running stack (`make demo` / `make start-docker`). On the 10M-row partitioned `demo.sales` dataset, filtering on `region` (no btree index) produces sequential scans across partitions.

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category","connection_id":"default"}'
```

Set `"analyze": true` to run `EXPLAIN (ANALYZE, FORMAT JSON)` when enabled server-side (executes the query; timeout-guarded).

Seq-scan count check:

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category"}' \
  | jq '[.findings[] | select(.is_seq_scan)] | length'
```

Expect many partition seq scans on `make seed-large-docker`; fewer on the small `make seed` / `make demo` dataset.

---

## Semantic search over saved queries (pgvector)

Requires embeddings (`EMBEDDING_BASE_URL` + `EMBEDDING_MODEL`) and at least one saved query. See [Semantic search (pgvector)](../reference/semantic-search-pgvector.md).

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/saved \
  -H "Content-Type: application/json" \
  -d '{"name":"North rollup","sql":"SELECT region, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY region"}'

curl -s 'http://localhost:8080/api/v1/suggestions/similar?text=regional%20revenue%20breakdown&limit=5' | jq .
```

---

## List connections

```bash
curl -s http://localhost:8080/api/v1/connections
```

```json
{"items":[{"id":"default","name":"Default"},{"id":"prod","name":"Production"}]}
```

**See also:** [API reference](README.md) · [Connect your PostgreSQL](../getting-started/connect-postgres.md) · [Docs overview](../index.md)
