# Phase 2 summary

Postgres depth backlog **B7–B11**. Audit date: repo state with migrations through `000021`.
See `docs/BACKLOG.md` for acceptance detail.

| Item | Status |
|------|--------|
| B7 `pg_query` validator | ☑ Shipped |
| B8 EXPLAIN (JSON) | ☑ Shipped |
| B9 `pg_stat_statements` | ☑ Shipped |
| B10 RLS multi-tenant demo | ☑ Shipped (`000021_sales_rls_demo`, `docs/ops/rls-demo.md`) |
| B11 pgvector semantic search | ☑ Shipped (pre-existing + B11 session docs/tests) |

**Phase 2:** 5/5 complete.

---

## B7 — `pg_query_go` validator

`app/queryrunner/validator.go` uses `pg_query.ParseToJSON` and an AST walk (`disallowedASTNodes`, `extractReadOnlyQuery`, `collectSchemaNames`) for single-statement read-only enforcement—no regex/substring blocklist. Adversarial cases live in `test/unit/app/queryrunner/queryrunner_test.go`.

**Verify:**

```bash
docker run --rm -v "$(pwd)":/app -w /app golang:1.25-bookworm \
  sh -c "apt-get update -qq && apt-get install -qq -y gcc libc6-dev >/dev/null && \
  go test ./test/unit/app/queryrunner/... -v -run TestValidator"
```

---

## B8 — EXPLAIN (JSON) integration

`POST /api/v1/queries/explain` runs `EXPLAIN (FORMAT JSON [, ANALYZE, BUFFERS])` on validated read-only SQL; `app/queryrunner/explain.go` parses the plan and flags seq scans / high-cost nodes with index hints.

**Verify:**

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category"}' \
  | jq '.findings[] | select(.is_seq_scan) | .message'
```

Expect at least one message containing `Sequential scan` and `btree index`.

---

## B9 — `pg_stat_statements` dashboard

Docker sets `shared_preload_libraries=pg_stat_statements`; migration `000019_pg_stat_statements` creates the extension and grants `pg_read_all_stats` to the readonly role. `GET /api/v1/queries/stats` and UI **Query Stats** at `/stats` expose top-N by total/mean time or calls.

**Verify:**

```bash
make postgres-recreate && make migrate-docker
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT region, COUNT(*) FROM demo.sales GROUP BY region","limit":10}'
curl -s 'http://localhost:8080/api/v1/queries/stats?order_by=total_time&limit=5' \
  | jq '.items[:2] | .[] | {query: .query[0:80], calls, total_time_ms}'
```

---

## B10 — RLS multi-tenant demo

Migration `000021_sales_rls_demo`: `ENABLE ROW LEVEL SECURITY` on `demo.sales`,
role `pgquerynarrative_sales_rep`, rep policy via `current_setting('app.current_rep', true)`.
API query path (`pgquerynarrative_readonly`) has permissive `USING (true)` policy — full
visibility unchanged. Walkthrough: `docs/ops/rls-demo.md`.

**Verify:**

```bash
# Session A
docker compose exec -T postgres psql -U pgquerynarrative_sales_rep -d pgquerynarrative \
  -c "SET app.current_rep = 'A. Lee'; SELECT DISTINCT sales_rep FROM demo.sales LIMIT 20;"
# Session B — disjoint rep set
docker compose exec -T postgres psql -U pgquerynarrative_sales_rep -d pgquerynarrative \
  -c "SET app.current_rep = 'B. Singh'; SELECT DISTINCT sales_rep FROM demo.sales LIMIT 20;"
docker compose exec -T postgres psql -U postgres -d pgquerynarrative -c '\d+ demo.sales'
```

Expect each session to see only its rep; `\d+` shows RLS policies enabled.

---

## B11 — pgvector semantic search

Save query → Ollama embed → `app.query_embeddings` (`embedding_vector vector(768)` + HNSW when `POSTGRES_IMAGE=pgvector/pgvector:pg16`); k-NN via `embedding_vector <=> $1` in `app/embedding/store.go`. `GET /api/v1/suggestions/similar` and Saved Queries UI semantic search; reports similar search pre-existing.

**Verify:**

```bash
POSTGRES_IMAGE=pgvector/pgvector:pg16 docker compose up -d postgres && make migrate-docker
# embeddings enabled in app env, then:
curl -s -X POST http://localhost:8080/api/v1/queries/saved \
  -H "Content-Type: application/json" \
  -d '{"name":"By region","sql":"SELECT region, SUM(total_amount) FROM demo.sales GROUP BY region"}'
curl -s 'http://localhost:8080/api/v1/suggestions/similar?text=revenue%20by%20region&limit=5' | jq .
```

See `docs/reference/semantic-search-pgvector.md` for the full flow.
