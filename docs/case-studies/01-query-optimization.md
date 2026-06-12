# How I took a 1.1s aggregation to 145ms on 10M rows in PostgreSQL

A real before/after on the PgQueryNarrative demo dataset: a region-filtered
category rollup on `demo.sales`, optimized with a covering composite index on a
monthly range-partitioned table.

**Environment:** PostgreSQL 16.14, Docker `postgres:16-alpine`, 2 GB container
memory. Dataset: **10,008,000 rows**, **1,672 MB** heap across partitions (see
[docs/DATASET.md](../DATASET.md)). Measured **2026-06-08**.

---

## Problem

A dashboard widget asks: *“Total revenue by product category for the North
region.”* The SQL is a simple aggregation with no date predicate — every
partition that holds data must be scanned:

```sql
SELECT product_category, SUM(total_amount) AS total
FROM demo.sales
WHERE region = 'North'
GROUP BY product_category
ORDER BY total DESC;
```

On 10M rows this is slow (~1.1s) and reads ~128k buffer pages. The table already
has single-column indexes on `date`, `product_category`, and `region`, but none
match the **filter + group-by + aggregate** shape of this query.

PgQueryNarrative’s `POST /api/v1/queries/explain` flags the plan with 25+
`Sequential scan` / `Bitmap Heap Scan` findings and suggests a btree index on the
filtered columns (see [docs/api/examples.md](../api/examples.md)).

---

## Schema context

`demo.sales` is `PARTITION BY RANGE (date)` with ~49 monthly child partitions
(migration `000018`). Relevant columns:

| Column | Role in query |
|---|---|
| `region` | `WHERE` filter (`'North'`) |
| `product_category` | `GROUP BY` key |
| `total_amount` | `SUM()` target |
| `date` | Partition key (not in this query) |

**Existing indexes** (on parent, propagated to partitions):

- `idx_sales_date` — `(date)`
- `idx_sales_category` — `(product_category)`
- `idx_sales_region` — `(region)`

The `region` index alone is not enough: Postgres uses a **Bitmap Index Scan**
on `region`, then a **Bitmap Heap Scan** to fetch `product_category` and
`total_amount` from the heap — millions of heap pages across ~25 populated
partitions.

---

## Before: `EXPLAIN (ANALYZE, BUFFERS)`

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT product_category, SUM(total_amount) AS total
FROM demo.sales
WHERE region = 'North'
GROUP BY product_category
ORDER BY total DESC;
```

**Key plan fragments** (truncated):

```
Finalize GroupAggregate  (actual time=1114.0..1120.6 rows=5 loops=1)
  ->  Gather Merge  (actual time=1113.9..1120.5 rows=15 loops=1)
        Workers Launched: 2
        ->  Partial HashAggregate
              ->  Parallel Append  (actual time=109.2..994.6 rows=416385 loops=3)
                    ->  Parallel Bitmap Heap Scan on sales_2025_08
                          Recheck Cond: (region = 'North'::text)
                          Buffers: shared read=5384
                          ->  Bitmap Index Scan on sales_2025_08_region_idx
                                Index Cond: (region = 'North'::text)
                    … (one bitmap heap scan per populated partition) …
```

| Metric | Value |
|---|---|
| **Execution Time** | **1145 ms** |
| **Shared buffers** | hit=155, **read=127,971** |
| **Root plan node** | `Gather Merge` → `Parallel Append` |
| **Dominant I/O** | Bitmap heap fetches per partition |

The planner correctly parallelizes across partitions, but each worker still
touches heap pages to read columns not in `idx_sales_region`.

---

## Fix: covering composite index

Add a btree index whose key matches the filter and group-by, with `total_amount`
in the `INCLUDE` list so Postgres can satisfy the aggregation from the index
alone:

```sql
CREATE INDEX idx_sales_region_category_covering
  ON demo.sales (region, product_category)
  INCLUDE (total_amount);

ANALYZE demo.sales;
```

**Why this shape:**

- `(region, product_category)` — equality on `region`, then category values are
  colocated for cheap partial aggregation per partition.
- `INCLUDE (total_amount)` — covering index; enables **Index Only Scan** with
  `Heap Fetches: 0` when the visibility map is fresh (true after `ANALYZE`).

On a partitioned table, the index is declared on the **parent** and PostgreSQL
creates matching indexes on every child partition automatically.

**Build cost:** ~11 s on this dataset. **Index size:** **426 MB** additional
(total index footprint grows from 678 MB → **1,105 MB**).

---

## After: `EXPLAIN (ANALYZE, BUFFERS)`

Same query, after `ANALYZE`:

```
Finalize GroupAggregate  (actual time=142.4..144.6 rows=5 loops=1)
  ->  Gather Merge  (actual time=142.4..144.5 rows=15 loops=1)
        ->  Partial HashAggregate
              ->  Parallel Append  (actual time=0.06..75.6 rows=416385 loops=3)
                    ->  Parallel Index Only Scan
                          using sales_2025_08_region_product_category_total_amount_idx
                          Index Cond: (region = 'North'::text)
                          Heap Fetches: 0
                          Buffers: shared hit=307
                    … (index-only scan per populated partition) …

Execution Time: 144.963 ms
```

| Metric | Before | After | Change |
|---|---|---|---|
| **Execution Time** | 1145 ms | **145 ms** (warm cache) | **~7.9× faster** |
| **Shared buffer reads** | 127,971 | **0** (all hits after warm-up) | ~128k fewer disk reads |
| **Scan type** | Bitmap Heap Scan | **Index Only Scan** | No heap fetches |
| **Estimated root cost** | 153,487 | 48,225 | −69% planner cost |

First run after index creation (partially cold cache): **171 ms**, 7,156 buffer
reads — still a **6.7×** improvement over the baseline.

---

## Results summary

| | |
|---|---|
| **Headline** | **1.1s → 145ms** on 10M rows |
| **Query** | Region-filtered `SUM(total_amount)` by `product_category` |
| **Technique** | Covering composite index `(region, product_category) INCLUDE (total_amount)` |
| **Trade-off** | +426 MB index storage; slower writes / vacuum on `demo.sales` |

---

## Approaches considered (and rejected)

### 1. Rely on `idx_sales_region` only

Already in place. The planner uses it, but only to find matching heap tuples —
every row still needs a heap fetch for `product_category` and `total_amount`.
Measured: 1145 ms. **Rejected:** insufficient for this access pattern.

### 2. Partial index `WHERE region = 'North'`

```sql
-- NOT chosen
CREATE INDEX … ON demo.sales (product_category) INCLUDE (total_amount)
  WHERE region = 'North';
```

Smaller than a full composite index, but **only accelerates one region value**.
Any change to the filter (`'South'`, `IN (…)`, ad-hoc BI) misses the index.
**Rejected:** too narrow for a general analytics endpoint.

### 3. Add a date range to force partition pruning

```sql
-- Different question (last 2 months only)
SELECT product_category, SUM(total_amount)
FROM demo.sales
WHERE date >= date_trunc('month', CURRENT_DATE) - INTERVAL '2 months'
GROUP BY product_category;
```

On this dataset, partition pruning removes 34 of 49 subplans
(`Subplans Removed: 34` — see [docs/DATASET.md](../DATASET.md)). That is the
right tool when the **business question is time-bounded**, but it does not
answer “all-time North totals.” **Rejected for this query:** changes semantics.

### 4. Materialized view per region

Pre-aggregate `product_category` totals per region. Fast reads, but requires
refresh strategy (scheduled `REFRESH MATERIALIZED VIEW CONCURRENTLY`, storage,
staleness). **Rejected for MVP:** operational overhead outweighs benefit for a
single hot query.

---

## Reproduce

Requires the large seed ([docs/DATASET.md](../DATASET.md)):

```bash
make postgres-up
make migrate-docker
make seed-large-docker   # ~10M rows, ~3m 30s
```

Connect:

```bash
docker compose exec -T postgres psql -U postgres -d pgquerynarrative
```

**Before** (drop index if re-running):

```sql
DROP INDEX IF EXISTS demo.idx_sales_region_category_covering;

EXPLAIN (ANALYZE, BUFFERS)
SELECT product_category, SUM(total_amount) AS total
FROM demo.sales
WHERE region = 'North'
GROUP BY product_category;
```

**Apply fix:**

```sql
CREATE INDEX idx_sales_region_category_covering
  ON demo.sales (region, product_category)
  INCLUDE (total_amount);
ANALYZE demo.sales;
```

**After:** run the same `EXPLAIN (ANALYZE, BUFFERS)` again.

Via the API:

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = '\''North'\'' GROUP BY product_category","analyze":true}' \
  | jq '{execution_time_ms, seq_scans: ([.findings[] | select(.is_seq_scan)] | length)}'
```

Expect `seq_scans: 0` after the covering index (findings may still list
high-cost `Gather Merge` nodes).

---

## Takeaways

1. **Match the index to the query shape** — filter columns first, then group-by
   keys; `INCLUDE` payload columns needed for aggregates.
2. **Covering indexes buy index-only scans** — especially valuable on large,
   partitioned tables where heap fetches multiply across children.
3. **Partitioning and indexing solve different problems** — monthly range
   partitions excel at *time-bounded* queries; global dimension rollups still
   need the right btree.
4. **Measure with `EXPLAIN (ANALYZE, BUFFERS)`** — execution time and buffer
   reads tell you whether you eliminated heap I/O, not just changed the plan
   diagram.

---

**See also:** [Demo dataset](../DATASET.md) · [EXPLAIN API examples](../api/examples.md) · [Partition migration](../../app/db/migrations/000018_partition_demo_sales.up.sql)
