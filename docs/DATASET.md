# Demo dataset

The `demo.sales` table is the benchmark dataset for query intelligence, EXPLAIN
analysis, and the optimization case study. It is **range-partitioned by month**
and seeded reproducibly at scale.

## Docker-only workflow (no host Go / psql)

Use this when the host has **only Docker** (no `go`, no `psql`):

```bash
# 1. Start Postgres (default image: postgres:16-alpine; override with POSTGRES_IMAGE)
make postgres-up

# 2. Initialize roles/schemas if first run (safe to re-run)
make db-init

# 3. Apply migrations (includes 000018 partition migration)
make migrate-docker

# 4. Seed ~10M rows (several minutes; Postgres container has 2G memory limit)
make seed-large-docker

# Optional: smaller test load
make seed-large-docker ROWS=100000
```

Postgres image override (e.g. PG 18):

```bash
POSTGRES_IMAGE=postgres:18-alpine make postgres-up
```

### Host with Go + psql

```bash
make migrate
make seed-large              # default ROWS=10000000
make seed-large ROWS=5000000 # custom count
```

Fast dev seed (8,000 rows) is unchanged: `make seed`.

---

## Schema & partitioning

`demo.sales` is a `PARTITION BY RANGE (date)` table (migration
`000018_partition_demo_sales`). Columns are unchanged from the original demo
table, except the primary key is now `(id, date)` — a partitioned table must
include the partition key in every unique/primary key.

- **Partition granularity:** one partition per month.
- **Window:** 36 months back to 12 months ahead of `CURRENT_DATE` (49 monthly
  partitions), plus a `DEFAULT` partition as a safety net.
- **Why month range:** the dominant access pattern is time-bounded aggregation
  (e.g. monthly totals, period-over-period). Monthly range partitions let
  Postgres **prune** to just the relevant months and keep each partition's
  indexes small.
- **DEFAULT partition tradeoff:** guarantees inserts never fail for
  out-of-window dates, but rows there are not pruned efficiently. For a clean
  benchmark, keep seeded dates inside the window (the seed uses a ~24-month
  spread, well within range).

> **Note on `demo.sales_summary`:** migration `000009` created a
> `demo.sales_summary` view on `demo.sales`. The partition migration drops it
> before swapping the table and recreates it afterwards, so the view continues
> to work against the partitioned table.

### Indexes

Declared on the parent (propagate to all partitions):

| Index | Column(s) | Purpose |
|---|---|---|
| `idx_sales_date` | `date` | Intra-partition ordering / range scans |
| `idx_sales_category` | `product_category` | Category filters |
| `idx_sales_region` | `region` | Region filters |

> **Intentional optimization runway:** there is deliberately **no composite or
> covering index** for the headline aggregation (e.g. `SUM(total_amount)` grouped
> by `product_category` filtered by `region`). This leaves a real before/after to
> demonstrate in `docs/case-studies/01-query-optimization.md` (Phase 3).

## Data distribution

See `tools/db/seed-large.sql`:

- `product_category` is front-weighted via `power(random(), 2)` (Electronics most common).
- Dates spread across ~24 months so rows populate many partitions.
- `total_amount` is consistent: `unit_price * quantity`.

---

## Measurements

Run these after seeding and paste results into the table below.

**Via Docker:**

```bash
docker compose exec -T postgres psql -U pgquerynarrative_app -d pgquerynarrative
```

```sql
-- Row count
SELECT count(*) FROM demo.sales;

-- Total size (heap + indexes across all partitions).
-- The parent relfilenode is empty on partitioned tables — pg_total_relation_size('demo.sales') returns 0.
SELECT pg_size_pretty(SUM(pg_total_relation_size(c.oid))) AS total_all_partitions
FROM pg_inherits i
JOIN pg_class c   ON c.oid = i.inhrelid
JOIN pg_class par ON par.oid = i.inhparent
WHERE par.relname = 'sales'
  AND par.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'demo');

-- Per-partition heap + index sizes (top 12 by size)
SELECT c.relname AS partition,
       pg_size_pretty(pg_total_relation_size(c.oid)) AS total_size,
       (SELECT reltuples::bigint FROM pg_class p WHERE p.oid = c.oid) AS approx_rows
FROM pg_inherits i
JOIN pg_class c   ON c.oid = i.inhrelid
JOIN pg_class par ON par.oid = i.inhparent
WHERE par.relname = 'sales'
ORDER BY pg_total_relation_size(c.oid) DESC
LIMIT 12;

-- Index sizes
SELECT indexrelname, pg_size_pretty(pg_relation_size(indexrelid)) AS size
FROM pg_stat_user_indexes
WHERE schemaname = 'demo'
ORDER BY pg_relation_size(indexrelid) DESC
LIMIT 10;
```

### Verify partition pruning

```sql
EXPLAIN (ANALYZE, BUFFERS)
SELECT product_category, SUM(total_amount)
FROM demo.sales
WHERE date >= date_trunc('month', CURRENT_DATE) - INTERVAL '2 months'
GROUP BY product_category;
```

Expect `Subplans Removed: 34` (or similar) and scans of only the recent monthly
partitions with data — not all 49.

### Results (verified 2026-06-08)

| Metric | Value | Environment |
|---|---|---|
| Rows seeded | 10,008,000 | `make seed-large-docker` (`ROWS=10000000` default) |
| Seed load time | ~3m 33s total (~203s `INSERT` + ~10s `ANALYZE`) | Docker `postgres:16-alpine`, 2G memory limit |
| Total size (sum of partitions) | 1,672 MB | Parent `pg_total_relation_size('demo.sales')` = 0 bytes (expected) |
| Largest partition size | `sales_2024_12` — 71 MB | Same seed run |
| Total index size | 678 MB | Sum of `pg_indexes_size` across partitions |
| Partition pruning (2-month agg) | **Subplans Removed: 34** of 49; 3 partitions with rows (`2026_04`–`2026_06`) | `EXPLAIN (ANALYZE, BUFFERS)` query above |
| Postgres version / memory | PostgreSQL 16.14 (aarch64), container limit 2G | `docker-compose.yml` `deploy.resources.limits.memory` |
