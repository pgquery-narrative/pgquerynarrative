# Postgres Credibility — Gap Analysis

What a senior Postgres-focused hiring manager wants to see in a portfolio repo,
where PgQueryNarrative stands today, and the smallest credible way to close each gap.

Legend: ✅ present · ⚠️ partial · ❌ missing

---

## 1. Scale & data modeling

| Expectation | Status | Evidence / Gap |
|---|---|---|
| Realistic dataset at scale (millions of rows) | ❌ | `demo.sales` seeds only 8,000 rows of uniform random data. |
| Partitioning (range/list/hash) with pruning shown in EXPLAIN | ❌ | Single non-partitioned table. |
| Thoughtful indexing (composite, covering, partial, GIN) | ⚠️ | Single-column btrees on `date`, `product_category`, `region`; GIN on `app.saved_queries.tags`. No composite/covering/partial on the hot path. |
| Documented table/index sizes & load times | ❌ | Nothing recorded. |

**Smallest credible fix:** Phase 1 — 10M-row month-partitioned `demo.sales`, reproducible `make seed-large`, sizes + load time in `docs/DATASET.md`.

## 2. Query performance & EXPLAIN

| Expectation | Status | Evidence / Gap |
|---|---|---|
| Comfort with `EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON)` | ❌ | Blocked by validator; never invoked anywhere. |
| Reads plan trees: seq scan vs index, joins, sorts, buffers | ❌ | No plan parsing. |
| Before/after optimization with real numbers | ❌ | No case study yet. |

**Smallest credible fix:** Phase 2 #1 (EXPLAIN JSON integration) + Phase 3 case study. **Hard blocker:** the validator must allow `EXPLAIN` (see §6).

## 3. SQL fluency (push work into the database)

| Expectation | Status | Evidence / Gap |
|---|---|---|
| Window functions, `LAG`/`LEAD`, `DATE_TRUNC`, rollups | ⚠️ | Examples exist in docs, but period-over-period is computed **in Go** (`app/metrics`), not SQL. |
| Materialized views / incremental aggregates | ❌ | None. |
| CTEs, lateral joins, set-returning functions | ⚠️ | Allowed by runner, not showcased. |

**Smallest credible fix:** Phase 1 — move period comparison to SQL window functions; Go path becomes documented fallback.

## 4. Security model (defense in depth)

| Expectation | Status | Evidence / Gap |
|---|---|---|
| Least-privilege roles | ✅ | `pgquerynarrative_readonly` (SELECT on `demo`) vs `pgquerynarrative_app`. |
| Read-only enforcement at the DB, not just the app | ⚠️ | Read-only role helps, but role could still be granted more; no `default_transaction_read_only` / `SET TRANSACTION READ ONLY` on the query path. |
| Row-Level Security for multi-tenant | ❌ | No RLS policies. |
| Statement timeout enforced server-side | ⚠️ | Only app-side `context` timeout; no `statement_timeout` set on the read-only role/session. |
| Robust SQL validation (real parser) | ❌ | Substring blocklist with false positives (`created_at`→`create`) and bypass risk. |

**Smallest credible fix:** Phase 2 #2 (`pg_query` parser) + #4 (RLS demo); add `SET TRANSACTION READ ONLY` + `statement_timeout` on the query path.

## 5. Observability & operations

| Expectation | Status | Evidence / Gap |
|---|---|---|
| `pg_stat_statements` for top-N query analysis | ❌ | Extension not enabled; no `shared_preload_libraries`. |
| Connection pooling, sane pool config | ✅ | pgx pools with max conns, lifetime, min conns, retries. |
| Backup/restore & migration strategy documented | ❌ | golang-migrate is used; no expand/contract or backup docs. |
| Monitoring/alerting outline | ❌ | None. |

**Smallest credible fix:** Phase 2 #3 (`pg_stat_statements` dashboard) + Phase 4 ops docs.

## 6. Correctness bugs found during audit (fix early)

- **Substring blocklist false positives:** `app/queryrunner/validator.go` uses `strings.Contains(lower, token)`. `SELECT created_at FROM app.reports` is rejected because `created_at` contains `create`. Same risk for any identifier containing `alter`, `analyze`, `copy`, etc.
- **EXPLAIN entirely blocked:** queries must start with `select`/`with`, so `EXPLAIN …` can never run — this blocks the entire Phase 2 #1 feature. **P0.**
- **Validation is bypassable in principle:** string matching is not a parser; comments, casing tricks, and dollar-quoting can evade or trip it. A real parser (`pg_query_go`) is the credible fix.

---

## Honest scope notes

- This is a **demo/portfolio app**, not a multi-tenant SaaS. RLS and `pg_stat_statements` should be implemented as **credible, self-contained demos** with verification steps, not half-built production systems.
- pgvector is **optional** and lowest priority — only if it tells a real story (semantic search over saved queries), otherwise skip rather than bolt on AI hype.
- Benchmarks must be **reproducible** (`make seed-large`, fixed seed where possible) and run on a documented environment (Docker memory cap matters at 10M rows).

## What "credible" looks like when done

A reviewer can clone the repo, run `make seed-large`, open a query, hit **Explain**,
see a seq scan flagged with an index suggestion, apply it, and watch the case study's
before/after numbers reproduce — all backed by a real parser-based safety layer and
least-privilege roles.
