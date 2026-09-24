# Understand plan findings

`EXPLAIN` findings name specific anti-patterns in the plan tree; they are the input
to candidate generation, not a diagnosis on their own.

## Finding kinds

| Finding | Fires when |
|---|---|
| `seq_scan` | A sequential scan node appears in the plan |
| `high_cost` | A node's planner cost is unusually high relative to the plan |
| `cardinality_misestimate` | Estimated rows disagree sharply with actual rows (ANALYZE only) |
| `low_selectivity` | ≥ 90% of scanned rows are discarded, with ≥ 1,000 rows scanned |
| `sort_spill` | A sort node's space type is `Disk` (work_mem exceeded) |
| `hash_batches` | A hash node used more than one batch |
| `loop_inflation` | A node ran ≥ 1,000 loops (e.g. a nested-loop inner side) |
| `parallel_shortage` | Fewer parallel workers launched than planned |
| `partition_pruning` | ≥ 3 child subplans, with none removed by pruning |
| `buffer_pressure` | ≥ 1,000 blocks read, with ≥ 50% of reads from disk (ANALYZE + BUFFERS) |
| `stale_statistics` | Row estimates suggest `ANALYZE` has not run recently |
| `index_candidate` | A scan shape suggests an index would help |
| `index_health` | An existing index looks unused, duplicated, or only partially covering |

Most of these except `cardinality_misestimate` and `buffer_pressure` are visible on a
plain `EXPLAIN`; the ones tied to actual timings and buffers need `EXPLAIN ANALYZE`.

## Estimated versus actual

| | Plain `EXPLAIN` | `EXPLAIN ANALYZE` |
|---|---|---|
| Executes the query | No | Yes |
| Rows | Planner's estimate | Estimate **and** actual |
| Cost | Planner cost (arbitrary units) | Same, ANALYZE does not change cost |
| Timing | None | `planning_time_ms`, `server_execution_time_ms` |
| `evidence_mode` | `estimated` | `observed` (once a non-zero execution time is reported) |

**Planner cost is never a time.** It is in arbitrary, engine-internal units and is
not proportional to wall-clock duration; two plans with a 10× cost difference can
run in nearly the same time, or the reverse. Only `EXPLAIN ANALYZE` measures
anything, and one run is one sample, see [Compare plans](compare.md#timing_runs)
for repeated timing.

## Partitions and pruning

On the demo's partitioned `demo.sales`, a predicate the planner can range-bound
(`date >= … AND date < …`) prunes to the relevant monthly partitions; a
function-wrapped predicate (`DATE_TRUNC('month', date) = …`) usually cannot, so the
planner scans every partition and `partition_pruning` fires. This is the shape
[Suggest and rank candidates](candidates.md) targets first.

## Buffers

`Buffers: shared hit=… read=…` (under `EXPLAIN (ANALYZE, BUFFERS)`) distinguishes a
plan served from cache (`hit`) from one that reads from disk (`read`). A lower
planner cost with more disk reads is not automatically a win; it depends on cache
state, which changes between runs.

## Index advice

`index_candidate` and `index_health` findings feed `IndexAdvice`: suggested DDL,
labelled for review only, never applied automatically. See
[Suggest and rank candidates](candidates.md#index-advice-and-hypopg).

## See also

[Investigate a slow query](investigate.md) · [Compare plans](compare.md) ·
[Evidence and status vocabulary](../reference/evidence.md#plan-findings)
