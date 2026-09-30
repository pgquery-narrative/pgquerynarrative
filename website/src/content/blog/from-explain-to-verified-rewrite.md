---
title: "From EXPLAIN to a Verified Rewrite: Introducing PgQueryNarrative"
description: "A DATE_TRUNC predicate that forces a 49-partition scan, the sargable rewrite PgQueryNarrative proposes from the query's own parse tree, and how the tool checks the rewrite returns the same rows before calling it safe."
date: 2026-10-01
author: "PgQueryNarrative"
tags: ["postgresql", "query-performance", "partitioning"]
draft: true
---

A dashboard widget asks a common question: revenue by product category, this month. The SQL a backend developer writes for that is unremarkable:

```sql
SELECT product_category, SUM(total_amount) AS revenue
FROM demo.sales
WHERE DATE_TRUNC('month', date) = DATE '2026-03-01'
GROUP BY product_category
ORDER BY revenue DESC;
```

It reads clearly. It also runs fine in development and then takes a second and a half against the real table, and nothing in the SQL itself explains why. That only shows up in the plan. The fix for this one turns out to be small and fairly well-known: stop wrapping the partition key in a function call. The part worth writing about is what happens after that fix is proposed. PgQueryNarrative doesn't stop at the rewrite. It runs both queries against live Postgres and checks that every row of both results agrees, before anything gets called safe.

PgQueryNarrative investigates a query roughly the way you'd do it by hand, just automated: `EXPLAIN` first, optionally `ANALYZE`. When a rewrite applies (from a fixed set of rules walking the SQL's own [parse tree](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/internal/queryrunner/rewriter.go), or AST, not a model guessing at syntax), it gets suggested. The candidate is compared against the original with a real `EXPLAIN (ANALYZE)` run on both sides, and if asked, both queries run again so their results can be checked against each other. A person reads what comes out of that and decides what to apply. Nothing here runs `CREATE INDEX` or swaps a query in production on its own.

Most queries run through this get plan findings and no candidate at all. That's the expected outcome: the rule set stays narrow on purpose, a small set of provable transforms rather than a general optimizer.

## The case study: a DATE_TRUNC filter on a partition key

The query above, and everything that follows, comes from a recorded investigation against `demo.sales`, PgQueryNarrative's benchmark dataset: PostgreSQL 16.14 running in Docker (`postgres:16-alpine`, 2 GB container memory, 128 MB `shared_buffers`), 10.6 million rows across 49 monthly range partitions, measured 2026-09-14.

`demo.sales` is declared `PARTITION BY RANGE (date)`, one partition per calendar month, with `date` (a plain `DATE` column, not a timestamp) as the partition key. That keeps this example's semantics simple: a `DATE` literal like `'2026-03-01'` has no time-of-day or time zone component to reason about. The rewrite rule itself isn't limited to `DATE` columns. It also has logic for `timestamptz`, but it specifically declines when the literal carries an explicit UTC offset, because `DATE_TRUNC` on a `timestamptz` column depends on the session's `TimeZone` setting, while the range bounds the rule emits are zoneless. That's a real case the rule accounts for, but a different one from the column here, so this write-up sticks to plain `DATE`.

### The plan evidence

Running `EXPLAIN (ANALYZE, BUFFERS)` on the original query shows the planner building an `Append` over every partition:

```
Sort  (actual time=1517.843..1525.504 rows=5 loops=1)
  Buffers: shared hit=84 read=134569, temp read=1679 written=1687
  ->  Finalize GroupAggregate
        ->  Gather Merge
              ->  Partial GroupAggregate
                    ->  Sort (external merge, disk)
                          ->  Parallel Append  (rows=158375 loops=3)
                                ->  Seq Scan on sales_2023_09 … 0 rows
                                ->  Seq Scan on sales_2023_10 … 0 rows
                                … (49 partitions total, one Seq Scan each) …
                                ->  Parallel Seq Scan on sales_2026_03 … 475124 rows
Execution Time: 1554.831 ms
```

The cause is in the `WHERE` clause, not the table's size. PostgreSQL's own guidance on partition pruning is specific: keep partitioning constraints to "simple equality conditions for list partitioning, or simple range tests for range partitioning," built from direct "comparisons of the partitioning column(s) to constants" ([PostgreSQL 16: Partition Pruning](https://www.postgresql.org/docs/16/ddl-partitioning.html)). `DATE_TRUNC('month', date) = DATE '2026-03-01'` doesn't fit that shape: it compares a function's *output* to a constant, not the column itself, so there's nothing for the planner to match against each partition's declared bounds. The planner has no way to rule out any of the 49 partitions, so it scans all of them, including 24 that hold no matching rows at all, and the aggregate's intermediate rows spill to disk (`temp read=1679 written=1687`), because the parallel workers have to merge-sort results from every partition before grouping.

| Metric | Value |
|---|---|
| Execution time | 1555 ms (first run); 990–999 ms on repeat runs |
| Shared buffer reads | 123,000–134,569, every run |
| Partitions scanned | 49 of 49 |
| Temp disk | 1,679–1,687 blocks written |

No index on `date` would have fixed this on its own. The planner can't evaluate the predicate against partition bounds at all, index or not. This is a sargability problem, and it has to be fixed in the predicate.

## The rewrite: a half-open range instead of a wrapped column

Given this query, PgQueryNarrative's `suggest-rewrite` step parses the SQL's parse tree and returns:

```json
{
  "candidates": [{
    "sql": "SELECT product_category, sum(total_amount) AS revenue FROM demo.sales WHERE date >= '2026-03-01'::date AND date < '2026-04-01'::date GROUP BY product_category ORDER BY revenue DESC",
    "rationale": "unwrap DATE_TRUNC('month') equality to a sargable range predicate so PostgreSQL can prune partitions and use indexes on the column",
    "category": "function_wrap",
    "confidence": "high"
  }]
}
```

No SQL executes for this step; it's a pure AST transform. The rewrite replaces the equality against a function call with a half-open range directly on the column: `date >= '2026-03-01' AND date < '2026-04-01'`. The lower bound is inclusive, the upper bound is exclusive: March 1st through March 31st, stopping before April 1st brings in any rows. A closed range (`<= '2026-03-31'`) would happen to work for a `DATE` column, but it breaks the moment the column carries a time-of-day component: `2026-03-31 08:00:00` would fall outside it. The half-open form works either way, which is why the rule always emits that shape instead of a closed bound tuned to this one column type.

Both sides of the comparison are now a raw column against constants, so the planner can prune at plan time and an index on `date` becomes usable within the surviving partition.

### Measured, not assumed

Comparing the original and rewritten query with `EXPLAIN (ANALYZE, BUFFERS)` on both sides:

```
->  Parallel Seq Scan on sales_2026_03 sales  (actual time=0.048..30.529 rows=158375 loops=3)
      Buffers: shared hit=576 read=5455
Execution Time: 62.523 ms
```

| Metric | Before | After | Change |
|---|---|---|---|
| Execution time | 990–1555 ms | 44–63 ms | ~18–28× faster |
| Shared buffer reads | 123,000–134,569 | 5,263–5,455 | ~96% fewer |
| Partitions scanned | 49 | 1 | pruned to the target month |
| Temp disk | 1,679–1,687 blocks | 0 | eliminated |
| Planner cost (estimate) | 215,166 | 10,992 | −94.9% |

The first four rows come from `ANALYZE`, meaning both queries were actually executed and timed. The last row didn't. Planner cost is measured in arbitrary units, "conventionally mean disk page fetches" per the PostgreSQL documentation ([EXPLAIN](https://www.postgresql.org/docs/16/sql-explain.html)), and a 95% drop in that number is not a 95% drop in latency; it's evidence the planner expects less work, which is a different claim than a stopwatch result. PgQueryNarrative's own comparison output attaches that caveat directly to the cost row rather than leaving it implicit, and it reports execution time as a range across repeated runs instead of a single sample, because one run reflects whatever happened to be cached at that moment.

## What gets verified

A candidate plan that looks better is not proof the query still returns the same answer, and PgQueryNarrative doesn't treat it as one. Result verification is a separate, optional, explicitly-executed check, with five possible outcomes:

| State | Meaning |
|---|---|
| `VerifiedEqual` | Both results were compared in full via an order-independent fingerprint (or, on a fallback path, a full ≤1,000-row comparison), and they matched |
| `SampleMatch` | Row counts matched and a bounded 1,000-row sample matched, but the full result wasn't fingerprinted |
| `Different` | Counts, fingerprints, or the sample disagreed |
| `Unverified` | The check couldn't complete, a query error or a timeout, and is never reported as a mismatch |
| `NotRequested` | Verification wasn't asked for |

For this query, requesting `verify_results: true` runs both statements and computes a single aggregate per side:

```sql
SELECT count(*)::bigint AS pgqn_n,
       coalesce(sum(hashtextextended(pgqn_eq::text, 0)::numeric), 0)::text AS pgqn_s,
       coalesce(bit_xor(hashtextextended(pgqn_eq::text, 0)), 0)::bigint AS pgqn_x,
       coalesce(sum(hashtextextended(pgqn_eq::text, 1)::numeric), 0)::text AS pgqn_s2,
       coalesce(bit_xor(hashtextextended(pgqn_eq::text, 1)), 0)::bigint AS pgqn_x2
FROM (<query>) AS pgqn_eq
```

`count`, `sum`, and `bit_xor` are all commutative, so the comparison is order-independent by construction: no sort, one aggregate pass per side. Each row's text form is hashed twice, with two different seeds, producing two independent 64-bit sum/xor pairs rather than one: 128 bits of fingerprint, so a coincidental match in one pass essentially never survives the other. The recorded run returned `VerifiedEqual`, with both sides producing 5 rows and a matching primary hash pair: `5 | -1291831055969178452 | -5989091917971413090` on both the original and rewritten query.

A matching fingerprint over an executed result is strong evidence for that specific run. It is not a mathematical proof that holds for every possible dataset; a different data distribution could in principle produce a collision, however unlikely. It also doesn't check row order, or confirm the two result sets share the same column names or types: the fingerprint hashes each row's rendered text, not its schema. A query whose contract depends on `ORDER BY`, or on a column keeping a specific type, needs that checked separately (see [Verify result equivalence](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/docs/workflows/verify-results.md)).

## Where the rule declines

Not every `DATE_TRUNC` equality is safe to unwrap. Try the same pattern with a literal that isn't aligned to a month boundary:

```sql
SELECT count(*) FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2026-03-15';
--  count
-- -------
--      0
```

`DATE_TRUNC('month', date)` only ever produces the first of a month, so an equality against the 15th matches nothing: the correct answer is zero rows, always. A rewrite rule that blindly widened this to a range would get it badly wrong:

```sql
-- what a naive rewrite would produce (incorrect)
SELECT count(*) FROM demo.sales WHERE date >= DATE '2026-03-15' AND date < DATE '2026-04-15';
--  count
-- --------
--  475358
```

Zero correct rows versus 475,358 silently wrong ones. Asking PgQueryNarrative for a rewrite on the misaligned query returns an empty candidate list. The rule checks whether truncating the literal to the unit boundary reproduces the literal exactly before it will touch the predicate at all; if it doesn't, the transform is provably unsound, and the rewriter declines rather than guess. Getting no candidate is the correct outcome here, not a gap in the rule set.

## What the report keeps for review

The equivalence result decides whether a report can even be generated: `VerifiedEqual` allows it, `SampleMatch` requires someone to explicitly acknowledge it's sampled evidence rather than a full check, and `Different` or `Unverified` block it outright. An investigation with no comparison at all can still produce a report; it just carries no equivalence claim, because none was made. The report itself is a deterministic template built from the stored plan findings, the candidate SQL, the before/after metrics, and the equivalence status and notes. No LLM writes it, and nothing in the loop applies the change on its own; the report is what a reviewer reads before deciding to run the DDL or ship the rewritten query.

That's the shape of the whole investigation: a rewrite is a guess until it's measured, and a measurement is not the same claim as a checked result. The 18–28× number in this case study is real, but it isn't what got this query cleared to ship. `VerifiedEqual` is what did.

If you want to see this against your own schema rather than the demo dataset, the [quickstart](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/docs/getting-started/quickstart.md) brings up a local instance with Docker in a couple of minutes; the [repository](https://github.com/pgquery-narrative/pgquerynarrative) has the full rule set and the rest of the workflow docs.
