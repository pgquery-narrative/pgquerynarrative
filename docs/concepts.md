# Concepts

The vocabulary behind the UI and the API. Exact field values are listed in
[Evidence and status vocabulary](reference/evidence.md); the system map is in
[Architecture](architecture.md).

## What problem this solves

Teams usually know a query is slow — a dashboard, `pg_stat_statements`, a complaint —
but lack a repeatable path from **symptom → plan evidence → candidate fix → checked
result → shareable write-up**. Pasting SQL into a chatbot skips the database's own
evidence. PgQueryNarrative keeps every step grounded in what PostgreSQL reports.

## Investigation

An **investigation** is the unit of work. It is stored in the application's metadata
database and holds:

| Piece | Meaning |
|---|---|
| Source SQL | The expensive or suspicious query |
| Plan evidence | The parsed `EXPLAIN` (or `EXPLAIN ANALYZE`) tree and its findings |
| Candidate SQL | A rewrite proposed by the rewrite engine, or one you typed |
| Comparison | Before/after plan metrics and structural diff |
| Result equivalence | Whether both queries returned the same rows, and how thoroughly that was checked |
| Fix status | Where a fix is in its life: proposed → verified → applied → confirmed/regressed |
| Report | A deterministic engineering report built from the evidence (no LLM) |

Investigations are **organization-wide**: every member of the organization can see and
act on any investigation in it, deliberately, so a teammate can pick up the work.
`created_by` records who opened it; row-level security confines each investigation, its
candidates and linked regression alerts to its own organization. There is no per-user
private mode. Lifecycle: [Investigate a slow query](workflows/investigate.md).

## Evidence: estimated versus observed

Evidence is **what PostgreSQL reported**, never model opinion. The distinction that
matters most is whether anything ran:

| | Plain `EXPLAIN` | `EXPLAIN ANALYZE` |
|---|---|---|
| Executes the query | No | **Yes** |
| `evidence_mode` | `estimated` | `observed` (when PostgreSQL reported an execution time) |
| Costs | Planner estimates, arbitrary units | Planner estimates, arbitrary units |
| Times and actual rows | None | Measured, for this one run |
| Allowed by default | Yes | No — `SECURITY_EXPLAIN_ANALYZE_ENABLED=true` and the connection's `analyze` permission |

**Planner cost is not time.** It is an estimate in arbitrary units and is not
proportional to runtime, so it is never reported as a speed multiple. Only ANALYZE
produces a duration, and a single run is reported as a single sample; `timing_runs`
(1–5) reports a median and the observed spread. Details:
[Understand plan findings](workflows/plan-findings.md) and [Compare plans](workflows/compare.md).

## Rewrite engine and candidates

**Suggest rewrite** walks the query's PostgreSQL parse tree (via `pg_query`) and
proposes rewrites for a small, deliberately conservative set of shapes: function-wrapped
date filters (`DATE_TRUNC`, `EXTRACT(YEAR …)`, `to_char`, `::date`, `COALESCE`) turned
into sargable ranges, `OR` across columns → `UNION ALL`, `IN`/`NOT IN (SELECT …)` →
`EXISTS`/`NOT EXISTS`, and `LEFT JOIN … IS NULL` → `NOT EXISTS`. It declines whenever it
cannot show a transform is safe; getting findings and no rewrite is a normal outcome.

**Rank candidates** dry-EXPLAINs the rewrites and projects index DDL with HypoPG when it
is installed (a labelled heuristic otherwise, which is never ranked). Ranking compares
plans; it does not check results. Details: [Suggest and rank candidates](workflows/candidates.md).

## Compare and result verification

**Compare** plans the source and candidate SQL side by side (and executes them under
ANALYZE when allowed) and reports metric deltas and structural plan changes.
"Better plan" means PostgreSQL's plan changed in the expected way — not that anything
preferred the new SQL.

**Result verification** is separate and opt-in (`verify_results`). It executes both
queries and reports one of five states:

| State | Meaning |
|---|---|
| `VerifiedEqual` | Every row of both results contributed to an order-independent fingerprint, and the fingerprints matched (or, on the fallback path, the whole result was ≤ 1,000 rows and matched) |
| `SampleMatch` | Full fingerprinting could not run; row counts matched and a bounded deterministic sample matched. Supporting evidence, not verification |
| `Different` | Row counts, fingerprints or samples differ |
| `Unverified` | The check could not complete. Never reported as a mismatch |
| `NotRequested` | Verification was not asked for |

This is verification, not mathematical proof: the fingerprint is a 128-bit hash
aggregate (two independent 64-bit hashes) over each row's text form, so a match is
probabilistic agreement, and it ignores column names, column types and
`ORDER BY`. Read [Verify result equivalence](workflows/verify-results.md) before relying on it.

## Two report types

| Type | Produced by | LLM |
|---|---|---|
| **Investigation report** | `POST /api/v1/investigations/{id}/report` | None. A deterministic template over the stored evidence. Once a candidate has been compared, it requires `VerifiedEqual`, or `SampleMatch` with an explicit `accept_sample_match=true` |
| **Workbench report** | `POST /api/v1/reports/generate` (Query runner, Ask) | Uses the configured LLM for the narrative; falls back to a deterministic metrics narrative if the LLM call fails |

See [Reports and sharing](workbench/reports.md).

## Regressions and applied fixes

With `pg_stat_statements` available, a background poller snapshots statement
statistics per connection, computes per-interval deltas, compares them to a baseline,
and raises regression alerts. An alert is an **entry point** into the same investigation
loop. When a fix is marked `applied`, the poller later marks it `confirmed` or
`regressed` from measured statistics — those two states cannot be set by hand.
See [Regressions and applied fixes](workflows/regressions.md). On the default demo the
inbox is empty unless real statistics exist; `APP_ENV=demo` seeds sample alerts and
fabricated workspace KPIs — never enable it where the numbers matter.

## Connections, schemas and organizations

- A **connection** is a read-only analytical data source. There is always a `default`
  connection; more come from `DATABASE_CONNECTIONS_JSON` or per-organization secrets.
  Requests choose one with `connection_id`. See [Multiple connections](workflows/connections.md).
- Queries may only reference schemas in the connection's **allowlist**
  (`DATABASE_ALLOWED_SCHEMAS`, default `demo`). See [Query execution safety](security/query-safety.md).
- **Organizations** isolate application metadata (investigations, reports, saved
  queries…) from each other. See [Organizations and tenancy](security/tenancy.md).

## Optional LLM layer

Natural-language Ask, chat and plain-English SQL explanation need an LLM provider; the
workbench report narrative uses one when available. Plan findings, candidates, compare,
verification and investigation reports do not. See [LLM providers](integrations/llm.md).
