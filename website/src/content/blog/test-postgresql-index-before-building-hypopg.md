---
title: "Test a PostgreSQL Index Before You Build It with HypoPG"
description: "Compare PostgreSQL index candidates with HypoPG, then measure the real index. A reproducible 500,000-row lab shows what the estimates can tell you."
date: 2026-11-04
author: "PgQueryNarrative"
tags: ["postgresql", "indexing", "query-performance", "hypopg"]
draft: false
---

An orders query needs 31 rows. PostgreSQL reads half a million to find them.

An index on the account ID looks useful. An index on the account ID and order date looks better. Before building either, there is a question PostgreSQL can help answer: what plan would it choose if that index existed?

[HypoPG](https://hypopg.readthedocs.io/en/rel1_stable/usage.html) lets you ask that question. It registers a hypothetical index in the current connection, giving the planner an access path to consider without building the index itself.

In the experiment below, the estimated total cost fell from **19,389 to 123.97**. Then `EXPLAIN ANALYZE` went straight back to a sequential scan. That is expected: a hypothetical index has no stored entries for the executor to read. Getting a runtime measurement requires another step.

Here is the query:

```sql
SELECT order_id, total_cents
FROM index_lab.orders
WHERE account_id = 42
  AND placed_at >= DATE '2026-03-01'
  AND placed_at < DATE '2026-04-01';
```

The table contains 500,000 synthetic orders: 1,000 accounts, each with one order per day for 500 days starting on January 1, 2025. Account 42 has exactly 31 orders in March 2026. There is a primary-key index on `order_id`, but initially nothing on the two filter columns.

The [complete SQL lab](/blog/hypopg-index-lab.sql) creates the data, captures the hypothetical plans, builds one real index, and records the before-and-after measurements. Run it once in a fresh scratch database. The snippets here walk through its phases; the download runs the full sequence in one connection.

These results were recorded on November 4, 2026, using PostgreSQL 16.15 and HypoPG 1.4.3 in an ARM64 Docker container limited to two CPUs and 768 MiB of memory. PostgreSQL had 128 MB of `shared_buffers`. The table occupied 83 MB. The lab runs `ANALYZE` after loading the data and sets `max_parallel_workers_per_gather = 0` to keep the plans easy to inspect. Those are the conditions of this experiment, rather than recommended production settings.

With no supporting index, plain `EXPLAIN` produces:

```text
Seq Scan on orders  (cost=0.00..19389.00 rows=31 width=12)
  Filter: ((placed_at >= '2026-03-01'::date)
       AND (placed_at < '2026-04-01'::date)
       AND (account_id = 42))
```

The planner already estimates the result size correctly. It expects 31 rows. Its problem is reaching those rows cheaply.

That distinction is useful when investigating a slow query. Bad row estimates can send the planner toward a poor choice, but accurate estimates do not manufacture a missing access path. Here, the scan still has to inspect the table.

To try another path, the server needs the HypoPG package installed, and the database needs the extension enabled by a role with the required privileges:

```sql
CREATE EXTENSION IF NOT EXISTS hypopg;
```

`CREATE EXTENSION` does not download the package. Installation depends on your PostgreSQL distribution or hosting provider; the [HypoPG installation instructions](https://hypopg.readthedocs.io/en/rel1_stable/installation.html) cover packages and source builds.

Start with the narrower index definition:

```sql
SELECT hypopg_reset();

SELECT * FROM hypopg_create_index(
    'CREATE INDEX ON index_lab.orders (account_id)'
);

EXPLAIN
SELECT order_id, total_cents
FROM index_lab.orders
WHERE account_id = 42
  AND placed_at >= DATE '2026-03-01'
  AND placed_at < DATE '2026-04-01';
```

This time, PostgreSQL chooses a bitmap index scan followed by a bitmap heap scan. It expects the index to find about 498 rows for account 42, then applies the date filter while fetching table rows. The actual account has 500 orders; the estimate is close.

Now clear that hypothesis and try both columns:

```sql
SELECT hypopg_reset();

SELECT * FROM hypopg_create_index(
    'CREATE INDEX ON index_lab.orders (account_id, placed_at)'
);

EXPLAIN
SELECT order_id, total_cents
FROM index_lab.orders
WHERE account_id = 42
  AND placed_at >= DATE '2026-03-01'
  AND placed_at < DATE '2026-04-01';
```

Keep these statements in the **same database connection**. Hypothetical indexes belong to that session. Resetting between candidates also matters: otherwise the planner can consider both indexes, and the second experiment answers a different question. The [HypoPG usage guide](https://hypopg.readthedocs.io/en/rel1_stable/usage.html) documents this scope and the reset function.

The composite index puts the account equality first and the date range second. That fits PostgreSQL 16's [multicolumn B-tree rules](https://www.postgresql.org/docs/16/indexes-multicolumn.html): equality on the leading column, followed by a range on the next column, can narrow the portion of the index searched.

Here are the plans' estimates from the recorded run:

| Available candidate | Chosen access path | Estimated total cost | Estimated rows found by the index |
|---|---|---:|---:|
| No supporting index | Sequential scan | 19,389.00 | — |
| Hypothetical `(account_id)` | Bitmap index + heap scan | 1,655.92 | 498 |
| Hypothetical `(account_id, placed_at)` | Bitmap index + heap scan | 123.97 | 31 |

Both hypothetical plans ultimately estimate 31 output rows. The composite index gets there with less work because its `Index Cond` includes the account and both date bounds. The account-only plan retrieves a larger set and filters it afterward. That change in the plan explains the lower estimate.

The composite candidate has earned a closer look. It has not established a runtime improvement. PostgreSQL's [EXPLAIN reference](https://www.postgresql.org/docs/16/sql-explain.html) defines cost in arbitrary planner units; dividing the two costs does not give a speedup factor. Your exact estimates may also differ because `ANALYZE` samples the table.

The easiest way to see the boundary is to add `ANALYZE` while the hypothetical composite index still exists:

```sql
EXPLAIN (ANALYZE, BUFFERS, TIMING OFF)
SELECT order_id, total_cents
FROM index_lab.orders
WHERE account_id = 42
  AND placed_at >= DATE '2026-03-01'
  AND placed_at < DATE '2026-04-01';
```

The recorded result, with the filter text omitted for space:

```text
Seq Scan on orders  (cost=0.00..19389.00 rows=31 width=12)
                   (actual rows=31 loops=1)
  Rows Removed by Filter: 499969
  Buffers: shared hit=10639
Execution Time: 58.960 ms
```

The sequential scan is back. HypoPG deliberately makes its hypothetical indexes available to plain `EXPLAIN` only. [`EXPLAIN ANALYZE` executes the query](https://www.postgresql.org/docs/16/sql-explain.html), so it must use the physical structures that exist. Nothing has failed, and there is no hidden index build to wait for.

The lab then resets HypoPG and builds the composite index on the disposable table:

```sql
SELECT hypopg_reset();

CREATE INDEX orders_account_placed_idx
    ON index_lab.orders (account_id, placed_at);
```

At that point, an executed plan can use it. The measured plan contains a bitmap index scan returning 31 rows and a bitmap heap scan visiting 31 heap blocks. The new index occupies 11 MB.

To collect timings, the lab runs one warm-up and five measured executions before building the index, then repeats that sequence afterward. Every measurement uses `EXPLAIN (ANALYZE, BUFFERS, TIMING OFF)`; disabling per-node timing still leaves the statement's execution time in the output.

| Measurement | Before the real index | After the real index |
|---|---:|---:|
| Execution time, median of five runs | 25.137 ms | 0.044 ms |
| Execution time, observed range | 24.418–30.494 ms | 0.030–0.078 ms |
| Shared buffer hits per measured run | 10,639 | 34 |
| Shared buffer reads per measured run | 0 | 0 |
| Rows returned | 31 | 31 |

The buffer counts make the result easier to explain than the tiny elapsed times do. The executed query now reaches the matching rows through three index blocks and 31 heap blocks. Previously, it inspected the whole table and rejected 499,969 rows.

This is a small, warm-cache experiment with uniform data and no concurrent workload. The before and after runs were collected sequentially. The timing range describes those executions; it cannot predict your endpoint's latency. Even the real index's estimated cost, 124.34, differs slightly from HypoPG's 123.97.

For an application, repeat the experiment with the parameter values that matter. A busy account may have years of orders; another may have three. A month-wide filter and an all-history export can need different plans. If the application uses prepared statements, inspect that path too: PostgreSQL can choose between [generic and parameter-specific plans](https://www.postgresql.org/docs/16/sql-prepare.html), so a literal query pasted into a console may not reproduce the application's choice.

There is also a cost outside this SELECT. PostgreSQL must [maintain a real index as the table changes](https://www.postgresql.org/docs/16/indexes-intro.html), and this read experiment measures none of that write overhead. It also does not measure production build time or contention. A concurrent build lets normal writes continue, but still performs work and has its own constraints; PostgreSQL documents those under [Building Indexes Concurrently](https://www.postgresql.org/docs/16/sql-createindex.html#SQL-CREATEINDEX-CONCURRENTLY).

This is the distinction PgQueryNarrative preserves in **Rank candidates**. Index advice attached to plan findings supplies candidate DDL. When HypoPG is installed and the analytical role has the required access, the tool projects each candidate's cost through PostgreSQL's planner. The [projection implementation](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/internal/queryrunner/hypopg.go) holds one connection for the hypothetical index and its `EXPLAIN`, then clears the session state.

If HypoPG is unavailable or the projection fails, the current fallback assumes a cost of baseline × 0.3. That fixed multiplier contains no new planner evidence. It is labeled `heuristic` and kept **review-only**; the [ranking code](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/internal/queryrunner/candidates.go) excludes it from numeric ranking. A HypoPG candidate receives a rank only when its projected plan improves on the baseline.

The two index definitions in this article were selected for the lab. They demonstrate the projection mechanism, rather than a claim that PgQueryNarrative searches every possible index definition. The tool proposes bounded candidates for a person to inspect. Creating the real index remains a separate, manual step. If you also change the SQL, [compare its results with the original](/blog/from-explain-to-verified-rewrite); candidate ranking does not run that check.

For the orders query, the useful finding is specific: putting the account and date predicates into the same index condition reduced the rows the access path had to retrieve. HypoPG made that visible before the build. The real index supplied the runtime evidence afterward.

To try the same process on your own query, [start a local PgQueryNarrative instance](/get-started), open an investigation, and use **Rank candidates**. Read the projection label alongside the cost. A candidate worth testing should come with a plan you can explain.
