# Verifying a rewrite, not just proposing one — and a bug that surfaced along the way

A `DATE_TRUNC`-wrapped filter on a partition key forces a scan of every one
of 49 monthly partitions. PgQueryNarrative's AST rewrite engine unwraps it
into a sargable range — but the headline claim of this case study isn't the
speedup. It's that the tool then **executes both queries and verifies, over
the full result set, that they return identical rows** — not a sample, not an
eyeball check, not "trust me." It is a verification, not a mathematical proof
([Verify result equivalence](../workflows/verify-results.md) says exactly what
it checks and what it does not). It is checked twice in this write-up: once by
the tool, once independently by hand, and both agree.

Along the way, producing the "PR-ready" export artifact the tool advertises
surfaced a real bug in that export code. It's reported and fixed here too —
not edited out, because a case study about verification should survive being
skeptical of itself.

**Environment:** PostgreSQL 16.14, Docker `postgres:16-alpine`, 2 GB
container memory, 128 MB `shared_buffers`. Dataset: `demo.sales`,
**10,600,000 rows** across **49 monthly partitions** (see
[the demo dataset](dataset.md)). Measured **2026-09-14** through the
running app's own API — `/investigations`, `/suggest-rewrite`,
`/candidate`, `/report` — plus raw `EXPLAIN (ANALYZE, BUFFERS)` and a
hand-run independent checksum for cross-verification.

---

## Problem

A dashboard widget asks: *"Revenue by product category, this month."* The
obvious SQL:

```sql
SELECT product_category, SUM(total_amount) AS revenue
FROM demo.sales
WHERE DATE_TRUNC('month', date) = DATE '2026-03-01'
GROUP BY product_category
ORDER BY revenue DESC;
```

`demo.sales` is `PARTITION BY RANGE (date)` with one partition per month
(migration `000018_partition_demo_sales`). `date` is the partition key — but
it's wrapped in `DATE_TRUNC()` here, and Postgres's partition pruning can't
evaluate a function call against partition bounds at plan time. The planner
has no choice but to build an `Append` over **all 49 partitions** and filter
row-by-row inside each one.

---

## Before: raw `EXPLAIN (ANALYZE, BUFFERS)`

```
Sort  (actual time=1517.843..1525.504 rows=5 loops=1)
  Buffers: shared hit=84 read=134569, temp read=1679 written=1687
  ->  Finalize GroupAggregate
        ->  Gather Merge
              Workers Launched: 2
              ->  Partial GroupAggregate
                    ->  Sort (external merge, disk)
                          ->  Parallel Append  (rows=158375 loops=3)
                                ->  Seq Scan on sales_2023_09 … 0 rows
                                ->  Seq Scan on sales_2023_10 … 0 rows
                                … (49 partitions total, one Seq Scan each) …
                                ->  Parallel Seq Scan on sales_2026_03 … 475124 rows
                                      Rows Removed by Filter: 0
                                ->  Parallel Seq Scan on sales_2026_07 … 0 rows
                                      Rows Removed by Filter: 477065
                                … (every other populated partition, all filtered out) …
Execution Time: 1554.831 ms
```

| Metric | Value |
|---|---|
| **Execution time** | **1555 ms** (first run); **990–999 ms** on repeat runs |
| **Shared buffer reads** | 123,000–134,569, every run |
| **Partitions scanned** | **49 of 49** (`Append` over the full partition set) |
| **Temp disk** | 1,679–1,687 blocks written (external sort spill) |
| **Root cause** | `date_trunc('month', (date)::timestamp) = '2026-03-01'` — not sargable |

Every partition gets its own `Seq Scan`, even the 24 empty ones (2023-09
through 2024-08, 2026-10 onward) — the planner can't rule them out without
evaluating the function per-row.

---

## The rewrite: driven by the real API, not by me

```bash
curl -s -X POST http://localhost:8080/api/v1/investigations \
  -d '{"title":"...", "sql":"SELECT product_category, SUM(total_amount) ... WHERE DATE_TRUNC(...) = DATE ...", "analyze": true}'

curl -s -X POST http://localhost:8080/api/v1/investigations/{id}/suggest-rewrite
```

Response:

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

This is an **AST transform**, not an LLM guess — `pg_query_go` parses the
real Postgres grammar, and the rewrite is only offered when calendar math on
the literal can prove the range is exact (see
[`rewriter.go`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/app/queryrunner/rewriter.go)).
The refusal case below shows what happens when it *can't* prove that.

---

## The verification: full-result, order-independent, in-database

```bash
curl -s -X POST http://localhost:8080/api/v1/investigations/{id}/candidate \
  -d '{"candidate_sql": "<rewrite above>", "analyze": true, "verify_results": true}'
```

The relevant part of the response:

```json
{
  "result_checksum_equal": true,
  "result_equivalence_status": "VerifiedEqual",
  "result_equivalence_notes": "Full result compared: 5 rows, matched by an order-independent checksum over every row (one aggregate pass per side, no sort).",
  "result_before_row_count": 5,
  "result_after_row_count": 5
}
```

`VerifiedEqual` means something specific: the tool ran both the original and
rewritten query against live Postgres and compared

```sql
SELECT count(*)::bigint,
       coalesce(sum(hashtextextended(t::text, 0)::numeric), 0)::text,
       coalesce(bit_xor(hashtextextended(t::text, 0)), 0)::bigint
FROM (<query>) AS t
```

— a **single aggregate pass per side**, no sort, no row transfer beyond
three scalars, order-independent by construction (`count`/`sum`/`bit_xor`
are all commutative). Two result sets that differ in even one row's value
produce different `sum`/`xor`; a transposition that happens to preserve the
sum still trips the `xor`. This is what "verified" means in the product —
not "the row count matched."

**I ran the same checksum myself, independently, via raw psql:**

```sql
-- original
 n |          s           |          x
---+----------------------+----------------------
 5 | -1291831055969178452 | -5989091917971413090

-- rewrite
 n |          s           |          x
---+----------------------+----------------------
 5 | -1291831055969178452 | -5989091917971413090
```

Identical. And because this result set is tiny (5 rows — one per product
category), I also just diffed the actual output by eye:

```
 product_category |   revenue
------------------+--------------
 Electronics      | 526484289.65
 Furniture        | 246936094.09
 Office Supplies  | 196409877.13
 Clothing         | 172799418.45
 Accessories      | 137640838.28
```
Identical on both sides, to the cent, across 10.6M source rows.

---

## After: raw `EXPLAIN (ANALYZE, BUFFERS)`

```sql
SELECT product_category, sum(total_amount) AS revenue
FROM demo.sales
WHERE date >= '2026-03-01'::date AND date < '2026-04-01'::date
GROUP BY product_category ORDER BY revenue DESC;
```

```
->  Parallel Seq Scan on sales_2026_03 sales  (actual time=0.048..30.529 rows=158375 loops=3)
      Buffers: shared hit=576 read=5455
Execution Time: 62.523 ms
```

No `Append`, no other partitions touched, no temp disk spill.

| Metric | Before | After | Change |
|---|---|---|---|
| **Execution time** | 990–1555 ms | **44–63 ms** | **~18–28× faster** |
| **Shared buffer reads** | 123,000–134,569 | 5,263–5,455 | **~96% fewer** |
| **Partitions scanned** | 49 | **1** | pruned to exactly the target month |
| **Temp disk** | 1,679–1,687 blocks | 0 | eliminated |
| **Planner cost (estimate)** | 215,166 | 10,992 | −94.9% (not a speed multiple — see caveat below) |

The API's own comparison output carries an honest caveat on nearly every
row, unprompted:

> *"Planner estimate in arbitrary units — not a time, and not a speed
> multiple. Use execution time (with ANALYZE) to claim a speedup."*
>
> *"One execution each, on the cache state at the time. Re-run to see the
> spread before quoting a speedup."*

That's why this write-up reports a range from repeated runs rather than one
number.

---

## The refusal case: when it declines instead of guessing

Not every `DATE_TRUNC` equality can be safely unwrapped. If the literal
isn't aligned to a month boundary, `DATE_TRUNC('month', date)` can never
equal it — the correct answer is always zero rows:

```sql
SELECT count(*) FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2026-03-15';
--  count
-- -------
--      0
```

A naive "just widen it to a range" rewrite would get this **badly wrong**:

```sql
-- what a careless auto-rewrite might guess — WRONG
SELECT count(*) FROM demo.sales WHERE date >= DATE '2026-03-15' AND date < DATE '2026-04-15';
--  count
-- --------
--  475358
```

Zero correct rows vs. 475,358 silently-wrong rows. I asked the tool for a
rewrite on this exact query:

```bash
curl -s -X POST http://localhost:8080/api/v1/investigations/{id}/suggest-rewrite
# {"candidates": []}
```

Empty. It declines rather than guess, because it can't prove the boundary
alignment the transform depends on
([`rewriter.go:371-380`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/app/queryrunner/rewriter.go)).
This is the same engineering judgment seen in the earlier case study's
"correctly says no index helps" moment — restraint as a feature, not a gap.

---

## The bug this exercise found — and fixed

Generating the PR-ready `.sql` export (the artifact meant to be pasted
straight into a migration or PR) turned up a real defect. The report's
`candidate_improvements` list mixes two unrelated things: the one real,
tested rewrite, and a plain-English pointer attached to *every* `seq_scan`
finding (`"Investigate index or predicate shape for Seq Scan"` — one per
partition, 49 of them). The export code treated all of them as SQL:

```sql
-- AFTER (candidate rewrite 2)
Investigate index or predicate shape for Seq Scan;

-- AFTER (candidate rewrite 3)
Investigate index or predicate shape for Seq Scan;
… (49 more of these) …
```

51 "candidate rewrite" entries, 50 of which are a sentence fragment with a
semicolon stuck on the end — not SQL, and not something you'd want to paste
into a PR. The Markdown export had the same root bug: it fenced the same
prose as ` ```sql ` blocks.

A follow-up review pass found the same root bug in **two more places** that
render the identical `proposed_change` field: the PDF export (a shaded
monospace code box around plain prose) and the HTML export / live
investigation-report page in the product UI itself (`<pre>` around prose) —
four render paths sharing one field, only two of which got checked in the
first pass.

**Root cause:** [`app/story/investigation_report.go`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/app/story/investigation_report.go)'s
`buildCandidates` builds both kinds of entry into the same list with no
machine-readable way to tell them apart; every renderer that consumed
`proposed_change` — Markdown, SQL, PDF, HTML, and the React UI — assumed it
was always SQL.

**Fix:** added an explicit `Kind` field (`sql_rewrite` / `investigate_hint`)
to `CandidateImprovement`, set correctly at the point each kind is built,
and updated **all five** consumers to check it: the Markdown and HTML
exports now render hints as plain text instead of fenced/`<pre>` code, the
SQL export skips them entirely, the PDF export drops the shaded code box for
hints, and the React investigation-report page (plus its TypeScript type,
which didn't even carry `kind` before) branches the same way. Verified live
against the running app — rebuilt the app container and pulled the actual
`GET /web/reports/export` HTML output for this investigation's report:

```html
<li><p>Investigate index or predicate shape for Seq Scan</p>
<p>Sequential scan on sales_2023_09 (estimated cost 0.00) — filter: …</p></li>
```

— plain `<p>`, not `<pre>`. And the SQL export, regenerated after the fix:

```sql
-- PgQueryNarrative report 389997fe
-- BEFORE (original)
SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2026-03-01' GROUP BY product_category ORDER BY revenue DESC;

-- AFTER (candidate rewrite 1)
SELECT product_category, sum(total_amount) AS revenue FROM demo.sales WHERE date >= '2026-03-01'::date AND date < '2026-04-01'::date GROUP BY product_category ORDER BY revenue DESC;

-- Result equivalence: VerifiedEqual
```

One real candidate, correctly labeled, nothing else. (Reports generated
*before* the fix keep the old broken export — the fix applies going
forward, not retroactively to already-persisted report JSON, which is the
right scope for this change.)

**What's verified and what isn't, honestly:** the Markdown, SQL, and HTML
paths are confirmed against real output from the running app, quoted above.
The PDF export was confirmed to still generate a valid PDF (no error, 12
pages) with the new conditional code in place, but not visually inspected —
no PDF-rendering tool was available in this environment to confirm the
shaded-box-vs-plain-text distinction on the page itself. The React
component's fix was confirmed by TypeScript type-checking (`tsc --noEmit`,
clean) and a golangci-lint-equivalent code read, not by opening it in a
browser. Both are logically identical to the already-verified paths — same
field, same condition — but "identical logic" is a weaker claim than
"watched it happen," and this write-up says which is which.

---

## Results summary

| | |
|---|---|
| **Headline** | Full-result, order-independent result verification — not just a speedup |
| **Query** | `DATE_TRUNC('month', date) = X` → `date >= X AND date < X+1month` |
| **Speed** | 990–1555 ms → 44–63 ms (**~18–28×**), 49 → 1 partitions scanned |
| **Verification** | `VerifiedEqual`, cross-checked independently: identical checksum, identical rows |
| **Judgment** | Declines to rewrite a misaligned literal rather than risk 475,358 wrong rows |
| **Bug found & fixed** | 5 render paths (SQL/Markdown/PDF/HTML export + the live React UI) conflated prose findings with real SQL candidates — fixed everywhere with one explicit `Kind` field |

---

## Reproduce

```bash
make postgres-up
make migrate-docker
make seed-large-docker   # ~10M rows, ~3m 30s
docker compose up -d app
```

```bash
# Before
docker compose exec -T postgres psql -U postgres -d pgquerynarrative -c "
EXPLAIN (ANALYZE, BUFFERS)
SELECT product_category, SUM(total_amount) AS revenue
FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2026-03-01'
GROUP BY product_category ORDER BY revenue DESC;"

# Full pipeline via the API
curl -s -X POST http://localhost:8080/api/v1/investigations \
  -d '{"title":"...", "sql":"SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('\''month'\'', date) = DATE '\''2026-03-01'\'' GROUP BY product_category ORDER BY revenue DESC", "analyze": true}'

curl -s -X POST http://localhost:8080/api/v1/investigations/<id>/suggest-rewrite

curl -s -X POST http://localhost:8080/api/v1/investigations/<id>/candidate \
  -d '{"candidate_sql": "<rewrite>", "analyze": true, "verify_results": true}'

curl -s -X POST http://localhost:8080/api/v1/investigations/<id>/report
curl -s "http://localhost:8080/web/reports/export/sql?id=<report_id>"
```

Refusal case:

```bash
curl -s -X POST http://localhost:8080/api/v1/investigations \
  -d '{"title":"...", "sql":"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE DATE_TRUNC('\''month'\'', date) = DATE '\''2026-03-15'\'' GROUP BY product_category", "analyze": false}'
curl -s -X POST http://localhost:8080/api/v1/investigations/<id>/suggest-rewrite
# expect {"candidates": []}
```

---

## Takeaways

1. **The speedup isn't the hard part — verifying equivalence is.** Anyone can
   guess the rewrite for `DATE_TRUNC('month', date) = X`. Nobody checks it
   row-for-row over 10.6M rows by hand in under a second; the tool does,
   with a single aggregate pass per side, and the result is checkable
   independently (I did, and it matched exactly).
2. **Restraint is verifiable, not just claimed.** Feed it a predicate where
   the "obvious" rewrite would silently return 475,358 wrong rows instead of
   the correct 0, and it returns an empty candidate list rather than a wrong
   answer.
3. **Testing the artifact surfaced a real bug, and that's the point of
   testing it end-to-end.** The "PR-ready" export wasn't, until this
   exercise ran it against a report with 49 partition-level findings and
   actually read the output instead of trusting the label.
4. **`VerifiedEqual` is a specific, falsifiable claim** — a full-result,
   order-independent checksum computed inside Postgres — not a vague
   confidence label. That specificity is what makes it worth verifying
   independently, and worth trusting once it checks out.
5. **The first fix was incomplete, and a second review pass is what caught
   that.** Fixing the bug in the export I happened to be using (SQL, then
   Markdown) left the identical bug live in three other places that render
   the same field — PDF export, HTML export, and the product's own live
   report page. A fix scoped to "the artifact I was looking at" instead of
   "every consumer of this field" would have shipped half-fixed.

---

**See also:** [Demo dataset](dataset.md) · [Query optimization case study](query-optimization.md) · [equivalence.go](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/app/service/equivalence.go) · [rewriter.go](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/app/queryrunner/rewriter.go) · [investigation_report.go](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/app/story/investigation_report.go) · [report_export.go](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/web/report_export.go) · [pdf.go](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/web/pdf.go) · [handlers.go](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/web/handlers.go)
