# Adding a rewrite rule

The rewrite engine is a trust boundary: a syntactically plausible but semantically
unsafe rewrite is worse than no rewrite at all, because it looks like a fix. Every
new rule must earn its place against the checklist below, not just pass a smoke
test.

## Required for any new rule

1. **AST-based, not string matching.** Detect and transform the pattern by walking
   the `pg_query` parse tree (as every existing rule does), never by matching SQL
   text. String matching cannot distinguish `date = '2024-01-01'` in a `WHERE`
   clause from the same text inside a string literal.
2. **Document the semantic assumption the rewrite relies on**, as a code comment
   next to the rule. "This is safe because X" should name the specific PostgreSQL
   behavior being relied on — see the existing comments on the anti-join rule
   (`app/queryrunner/rewriter_antijoin.go`) for the level of detail expected.
3. **A PostgreSQL-backed equivalence test**, in `test/integration/`, that runs both
   the original and rewritten SQL against a real database and asserts the same rows
   come back — following the pattern in
   `test/integration/rewrite_equivalence_test.go`. A unit test that only checks the
   generated SQL string is not sufficient; the rewrite's claim is about runtime
   behavior.
4. **NULL behavior**, explicitly considered. Most unsound rewrites in this class of
   engine come from `OR`/`IN`/anti-join transforms that change how `NULL` rows are
   treated — see `TestRewriteEquivalence_OrUnionKeepsNullRows` for the shape of test
   this needs.
5. **Type behavior.** If the rule touches a comparison involving a cast or an
   implicit type coercion, verify the rewrite is safe for the column's *actual*
   PostgreSQL type — not just for the literal in the example query. This is exactly
   why numeric/text casts are **not** rewritten today (`implicit_cast` was removed):
   the rewriter has no catalog access to the column's declared type, and dropping a
   cast is only safe when it happens to be a no-op for that type.
6. **Timestamp/timezone behavior**, when the rule touches a date or timestamp
   expression. `DATE_TRUNC` and `::date` rewrites already guard against a literal
   carrying an explicit UTC offset and against a truncation unit that doesn't align
   with the literal — a new date-shaped rule needs the same class of guard.
7. **State the candidate's rationale** in the returned `RewriteCandidate.rationale`
   — the sentence a reviewer reads before applying the change.
8. **Fail closed when an assumption doesn't hold.** The existing rules bail rather
   than guess whenever a precondition is unmet: parameterized SQL for the
   `OR`/`IN`/anti-join rules, a misaligned date literal, an anti-join whose `IS
   NULL` column isn't the join key, a right-hand table still selected in an
   anti-join, `NATURAL`/`USING` joins, more than one `FROM` item, or matching
   aliases. A new rule must have an equivalent bail-out list, and a test proving
   each one is actually refused (see `OutOfScope_FailsClosed` in
   `rewriter_param_test.go` for the pattern).

## Where the code goes

- A literal-only rule: add a `suggestX(sql, findings) *RewriteCandidate` function
  and call it from `SuggestRewrites` (`app/queryrunner/rewriter.go`).
- A sargable-style rule (function-wrapped column comparisons): extend the
  `rewriteFunctionWrapInExpr` dispatch so it also runs inside CTEs and FROM
  subqueries automatically (`rewriter_nested.go` already threads this).
- A parameterized variant of an existing rule: follow the pattern in
  `rewriter_sargable_param.go`, including its alignment guard.

## Existing rule catalog

See [Evidence and status vocabulary — rewrite categories](../reference/evidence.md#rewrite-categories)
for the current shipped set, and [Suggest and rank candidates](../workflows/candidates.md)
for what each one does and does not cover.

## Review bar

A reviewer should be able to answer, from the PR alone: what pattern this rule
targets, why the rewrite preserves results for every value the column can hold (not
just the demo's data), what makes it bail out, and what the equivalence test checks.
If any of those isn't answerable, the rule isn't ready.

## See also

[Suggest and rank candidates](../workflows/candidates.md) ·
[Verify result equivalence](../workflows/verify-results.md) ·
[Change workflows](change-workflows.md)
