# Suggest and rank candidates

PgQueryNarrative never applies a change to your database. It proposes; you review and
apply.

## Suggest rewrite

`POST /investigations/{id}/suggest-rewrite` walks the source SQL's PostgreSQL parse
tree (via `pg_query`) and proposes a rewrite when — and only when — it can show the
transform is safe. No SQL executes for this step.

**Covered shapes** (category in parentheses is what a demo scenario or candidate
reports):

| Pattern | Rewrite | Category |
|---|---|---|
| `DATE_TRUNC('month', date) = …` | Sargable range (`date >= … AND date < …`) | `function_wrap` |
| `EXTRACT(YEAR/MONTH FROM date) = …` | Sargable range | `function_wrap` |
| `to_char(date, …) = …`, `date::date = …` | Sargable range | `function_wrap` |
| `DATE_TRUNC`/`::date` `BETWEEN` or inequality | Sargable range | `function_wrap` |
| `COALESCE(col, x) = …` | Unwrapped equivalent predicate | `coalesce_unwrap` |
| `OR` across different columns | `UNION ALL` of indexable branches | `or_to_union` |
| `IN`/`NOT IN (SELECT …)` | `EXISTS`/`NOT EXISTS` | `in_to_exists` / `not_in_to_exists` |
| `LEFT JOIN … WHERE right.col IS NULL` | `NOT EXISTS` anti-join | `left_join_antijoin_to_not_exists` |

All of the above also work inside a CTE or a FROM-subquery, and (for the sargable
rules) against parameterized SQL (`$1`) with an alignment guard.

**What it deliberately does not rewrite:**

- **Numeric or text casts on a compared column** (`amount::integer = 1`,
  `price::text = '12'`). Dropping a cast is only safe when it is a no-op for the
  column's real type, and the rewriter has no catalog access to know that —
  `amount::integer = 1` and `amount = 1` disagree the moment `amount` holds `1.4`.
- **Parameterized SQL**, for the `OR`, `IN`/`NOT IN` and anti-join rules — only the
  sargable date rules handle `$1`.
- A `DATE_TRUNC`/`::date` literal that is **misaligned** with the truncation unit, or
  carries an explicit UTC offset.
- An anti-join whose `IS NULL` column is not the join key, whose right-hand table is
  still selected, or whose `ON`/`WHERE` shape is anything more complex than a plain
  equality (`OR`/`NOT`, a subquery, `NATURAL`/`USING`, more than one FROM item, or
  matching aliases).

Getting findings with **no** rewrite is the expected outcome for most real-world
queries — the engine is a small rule set over an AST, not a general optimizer.

## Rank candidates

`POST /investigations/{id}/rank-candidates` dry-EXPLAINs the baseline and every
proposed rewrite (no ANALYZE unless requested), and projects index DDL cost:

- **HypoPG**, when the extension is installed and the analytical role has the
  grants: creates a hypothetical index inside a scoped read-write transaction,
  re-EXPLAINs, then resets and rolls back — nothing persists.
- **Heuristic fallback** otherwise: baseline cost × 0.3, explicitly labelled
  `HEURISTIC`. Heuristic projections are **never ranked** alongside real EXPLAIN
  results — they are review-only.

A rewrite whose dry EXPLAIN fails is silently dropped from the ranking. Candidates
that are rankable and improve on the baseline get a rank (ties broken by cost, then
partitions scanned, then timing, then SQL text); rankable candidates that don't
improve are marked "not recommended". **Ranking runs no result-equivalence check** —
a ranked candidate still needs [Compare plans](compare.md) and
[Verify result equivalence](verify-results.md) before it is trustworthy.

## Index advice and HypoPG

Plan findings such as `index_candidate` produce `IndexAdvice`: suggested DDL with an
issue classification (`no_covering_index`, `already_covered`, `partial_coverage`,
`invalid`, `low_use`, `duplicate_prefix`, `overlapping`). This DDL is **always
suggest-only** — nothing executes it against your schema. If HypoPG is absent, index
cost is still shown, but labelled a heuristic rather than a projected plan.

## Human review

Every candidate — rewrite or index — is a proposal. Applying it (running the DDL,
shipping the rewritten query) is a manual step outside PgQueryNarrative. Tracking
that a fix was applied, and later confirmed or regressed by measured statistics, is
covered in [Regressions and applied fixes](regressions.md).

## See also

[Investigate a slow query](investigate.md) · [Compare plans](compare.md) ·
[Adding a rewrite rule](../development/rewrite-rules.md) (for contributors)
