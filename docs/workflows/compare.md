# Compare plans

Two independent checks live under "compare": a **plan** comparison, and an optional
**result** comparison. Keep them separate — a better plan does not by itself mean
the query still returns the same rows.

Two entry points, same payload shape: `POST /queries/explain/compare` (standalone,
no investigation record) and `POST /investigations/{id}/candidate` (attaches the
result to an investigation).

## EXPLAIN comparison (the default)

```bash
curl -s -X POST http://localhost:8080/api/v1/queries/explain/compare \
  -H "Content-Type: application/json" \
  -d '{"before_sql": "...", "after_sql": "...", "connection_id": "default"}'
```

Plans both queries (no execution), and returns:

- `before` / `after` — full `ExplainQueryResult` for each side
- `metrics` — a table of before/after/change rows, each with an optional `caveat`
  ("planner cost is an estimate in arbitrary units, not a time")
- `diff` — plan nodes removed, added, and structural improvements detected (e.g.
  fewer partitions scanned)

Requires the connection's `explain` permission.

## EXPLAIN ANALYZE comparison

Add `"analyze": true`. This **executes both queries** and requires
`SECURITY_EXPLAIN_ANALYZE_ENABLED=true` server-side plus the connection's `analyze`
permission; forbidden in production StrictMode. It adds real timings
(`planning_time_ms`, `server_execution_time_ms`) and buffer counts to each side.

### `timing_runs`

```json
{"analyze": true, "timing_runs": 5}
```

Runs each side under ANALYZE up to 5 times (default 1) and reports the **median**
and the observed **min/max spread**, instead of a single sample. When the spread is
as large as the gap between the two medians, the comparison says so rather than
claiming a speedup — a claimed improvement should not rest on one lucky run.
`timing_runs` only has an effect when `analyze` is true; it does nothing on a plain
plan comparison.

## Result verification (separate, opt-in)

Add `"verify_results": true` to **execute** both queries and check whether they
return the same rows. This is a distinct switch from `analyze` — you can ANALYZE
without verifying, and verify without ANALYZE. It requires the connection's `query`
permission in addition to `explain`/`analyze`. Full semantics, states, and the
report gate: [Verify result equivalence](verify-results.md) — read it before
treating a result as checked.

## Bind values

For parameterized `before_sql`/`after_sql` (`$1`, …), pass sample values in `binds`.
They are used only for the executed compare/equivalence run and are not stored.

## See also

[Understand plan findings](plan-findings.md) · [Verify result equivalence](verify-results.md) ·
[Evidence and status vocabulary](../reference/evidence.md#timing-fields)
