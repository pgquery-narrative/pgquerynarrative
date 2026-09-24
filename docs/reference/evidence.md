# Evidence and status vocabulary

Exact values for every status and evidence field the API returns. Concept-level
explanation lives on the linked workflow pages; this page is the lookup table.

## Result equivalence {#result-equivalence}

`result_equivalence_status` on a compare or candidate result:

| Value | Meaning |
|---|---|
| `VerifiedEqual` | Every row of both results contributed to a full-result, order-independent fingerprint, and the fingerprints matched, or the fallback path ran, both sides were ≤ 1,000 rows, and every row matched |
| `SampleMatch` | Full-result fingerprinting could not run; row counts matched and a bounded, deterministic sample of up to 1,000 rows matched |
| `Different` | Row counts, fingerprints, or the sample disagree |
| `Unverified` | The check could not complete (error, timeout, unsupported shape). Never a mismatch |
| `NotRequested` | `verify_results` was not set |

Full semantics, the fingerprint algorithm, and what it does not check:
[Verify result equivalence](../workflows/verify-results.md).

## Timing fields {#timing-fields}

On an `ExplainQueryResult` (from `/queries/explain`, and embedded in compare
results):

| Field | Present when | Meaning |
|---|---|---|
| `evidence_mode` | Always | `estimated` (plain EXPLAIN) or `observed` (ANALYZE reported a non-zero execution time) |
| `planning_time_ms` | Always | PostgreSQL's own planning time |
| `server_execution_time_ms` | ANALYZE only | PostgreSQL's own execution time for that run |
| `request_wall_time_ms` | Always | Server-measured network + planning + (under ANALYZE) execution time, not a substitute for `server_execution_time_ms` |

**There is no `execution_time_ms` field on an EXPLAIN result**: it was removed in
`v2.1.0` in favor of the fields above. It legitimately still exists on
`RunQueryResult` (from `/queries/run`) and on ranked-candidate results
(`rank-candidates`), where it means the same thing it always did: wall time for
that run.

`timing_runs` (1–5, compare/candidate only) adds `median`, `min`, `max`, and a
`spread` disclosure when the observed range is comparable to the measured
difference. See [Compare plans](../workflows/compare.md#timing_runs).

## Plan findings {#plan-findings}

| `node_type` finding kind | Meaning |
|---|---|
| `seq_scan` | Sequential scan present |
| `high_cost` | Unusually high planner cost relative to the plan |
| `cardinality_misestimate` | Estimated vs actual rows disagree sharply (ANALYZE only) |
| `low_selectivity` | ≥ 90% of scanned rows discarded, ≥ 1,000 scanned |
| `sort_spill` | Sort spilled to disk |
| `hash_batches` | Hash join used more than one batch |
| `loop_inflation` | A node ran ≥ 1,000 loops |
| `parallel_shortage` | Fewer parallel workers launched than planned |
| `partition_pruning` | ≥ 3 child subplans, none pruned |
| `buffer_pressure` | ≥ 1,000 blocks read, ≥ 50% from disk (ANALYZE + BUFFERS) |
| `stale_statistics` | Estimates suggest `ANALYZE` hasn't run recently |
| `index_candidate` | A scan shape suggests an index would help |
| `index_health` | An existing index looks unused, duplicated, or partially covering |

Details: [Understand plan findings](../workflows/plan-findings.md).

## Rewrite categories

| Category | Rules |
|---|---|
| `function_wrap` | `DATE_TRUNC`/`EXTRACT(YEAR ...)`/`to_char`/`::date` equality, `BETWEEN`, or inequality on a partition/filter column |
| `coalesce_unwrap` | `COALESCE(col, x) = ...` |
| `or_to_union` | `OR` across different columns → `UNION ALL` |
| `in_to_exists` / `not_in_to_exists` | `IN`/`NOT IN (SELECT ...)` → `EXISTS`/`NOT EXISTS` |
| `left_join_antijoin_to_not_exists` | `LEFT JOIN ... WHERE right.col IS NULL` → `NOT EXISTS` |

`implicit_cast` (numeric/text casts on a compared column) was removed: casts are no
longer rewritten. See [Suggest and rank candidates](../workflows/candidates.md).

## Index-DDL projection method

| Value | Meaning |
|---|---|
| `hypopg` | Cost projected via a real hypothetical index (HypoPG installed + grants present) |
| `heuristic` | Baseline cost × 0.3, labelled for review only, never ranked as if it were a real projection |
| `unavailable` | Neither could run |

## Investigation status

`analyzing` → `open` → `comparing` → `complete` (adding a new candidate to a
`complete` investigation moves it back to `comparing`). See
[Architecture: investigation lifecycle](../architecture.md#investigation-lifecycle).

## Fix status

`proposed`, `verified`, `applied`, `confirmed`, `regressed`, `abandoned`.
`confirmed` and `regressed` can only be set by the regression poller, from measured
statistics, for a fix linked to a regression alert, never through the API. See
[Regressions and applied fixes](../workflows/regressions.md#fix-lifecycle).

## Regression impact tiers

`critical` (≥ `REGRESSION_CRITICAL_THRESHOLD_PCT`, default 200%), `high` (≥
`REGRESSION_HIGH_THRESHOLD_PCT`, default 100%), otherwise `medium`. See
[Regressions and applied fixes](../workflows/regressions.md#alert-rules).

## See also

[API reference](api.md) · [API errors](api-errors.md) ·
[Verify result equivalence](../workflows/verify-results.md)
