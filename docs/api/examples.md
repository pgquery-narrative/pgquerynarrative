# API examples

Base URL: `http://localhost:8080` (or set port via [Configuration](../configuration.md)). Full endpoint list: [API reference](README.md).

**Example — run query:**

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category","limit":10,"connection_id":"default"}'
```

Response: `columns`, `rows`, `row_count`, `execution_time_ms`, optional `chart_suggestions`, `period_comparison` (when result has a time column and measures).

**Example — explain query plan (seq scan detection):**

Requires a running stack (`make start-docker` or `make postgres-up` + app on port 8080). On the
10M-row partitioned `demo.sales` dataset, filtering on `region` (no btree index on that column)
produces sequential scans across partitions — one finding per scanned partition.

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category","connection_id":"default"}'
```

Expected response (truncated; `plan` is the full `EXPLAIN (FORMAT JSON)` array; costs vary with row count):

```json
{
  "sql": "SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = 'North' GROUP BY product_category",
  "total_cost": 153486.8,
  "execution_time_ms": 81,
  "findings": [
    {
      "node_type": "Seq Scan",
      "relation": "sales_2023_06",
      "estimated_cost": 0,
      "is_seq_scan": true,
      "message": "Sequential scan on sales_2023_06 (estimated cost 0.00) — filter: (region = 'North'::text) — consider a btree index on filtered or joined columns"
    },
    {
      "node_type": "Seq Scan",
      "relation": "sales_2023_07",
      "estimated_cost": 0,
      "is_seq_scan": true,
      "message": "Sequential scan on sales_2023_07 (estimated cost 0.00) — filter: (region = 'North'::text) — consider a btree index on filtered or joined columns"
    },
    {
      "node_type": "Gather Merge",
      "estimated_cost": 153486.66,
      "is_seq_scan": false,
      "message": "High-cost Gather Merge on unknown relation (estimated cost 153486.66, ≥50% of plan total)"
    }
  ],
  "plan": [ { "Plan": { "Node Type": "Aggregate", "Total Cost": 153486.8, "…": "…" } } ]
}
```

Findings order follows plan-tree traversal (high-cost parents like `Gather Merge` may appear
before partition seq scans). Check seq-scan count:

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category"}' \
  | jq '[.findings[] | select(.is_seq_scan)] | length'
```

Expect **25** on the 10M-row partitioned dataset (`make seed-large-docker`); **1** on the small
`make seed` dataset (single `demo.sales` seq scan).

Set `"analyze": true` to run `EXPLAIN (ANALYZE, FORMAT JSON)` (executes the query; timeout-guarded).
On the same query, `execution_time_ms` is higher (~1s on 10M rows) but `total_cost` and findings
shape match the estimate-only path.

**Example — semantic search over saved queries (pgvector):**

Requires embeddings enabled (`EMBEDDING_BASE_URL` + `EMBEDDING_MODEL` in `.env`) and at least
one saved query. With `POSTGRES_IMAGE=pgvector/pgvector:pg16`, vectors are stored in
`app.query_embeddings.embedding_vector` and ranked in Postgres via HNSW.

```bash
# Save a query (triggers embed + upsert)
curl -s -X POST http://localhost:8080/api/v1/queries/saved \
  -H "Content-Type: application/json" \
  -d '{"name":"North rollup","sql":"SELECT region, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY region"}'

# Find similar saved queries
curl -s 'http://localhost:8080/api/v1/suggestions/similar?text=regional%20revenue%20breakdown&limit=5' | jq .
```

Response shape: `{ "suggestions": [ { "sql", "title", "source": "similar" }, ... ] }`.
See [Semantic search (pgvector)](../reference/semantic-search-pgvector.md) for the full flow.

**Example — list connections:**

```bash
curl -s http://localhost:8080/api/v1/connections
```

Response:

```json
{"items":[{"id":"default","name":"Default"},{"id":"prod","name":"Production"}]}
```

**See also:** [API reference](README.md) · [Configuration](../configuration.md) · [Documentation index](../README.md)
