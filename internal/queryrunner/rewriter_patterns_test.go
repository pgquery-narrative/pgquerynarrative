package queryrunner

import (
	"strings"
	"testing"
)

func TestSuggestRewrites_ExtractYear(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE EXTRACT(YEAR FROM date) = 2025`
	cands := SuggestRewrites(sql, nil)
	c := mustFindCategory(t, cands, "function_wrap")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "date >=") || !strings.Contains(got, "date <") {
		t.Fatalf("expected year range, got: %s", c.SQL)
	}
	if !strings.Contains(got, "2025-01-01") || !strings.Contains(got, "2026-01-01") {
		t.Fatalf("expected 2025→2026 bounds, got: %s", c.SQL)
	}
	if strings.Contains(strings.ToLower(got), "extract") {
		t.Fatalf("EXTRACT should be removed, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_DatePartYear(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE date_part('year', date) = 2025`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "2025-01-01") || !strings.Contains(got, "2026-01-01") {
		t.Fatalf("expected year range, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_ExtractYearAndMonth(t *testing.T) {
	sql := `SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE EXTRACT(YEAR FROM date) = 2025 AND EXTRACT(MONTH FROM date) = 1 GROUP BY product_category ORDER BY revenue DESC`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "2025-01-01") || !strings.Contains(got, "2025-02-01") {
		t.Fatalf("expected January range, got: %s", c.SQL)
	}
	if strings.Contains(strings.ToLower(got), "extract") {
		t.Fatalf("EXTRACT should be removed, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_ExtractMonthAloneSkipped(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE EXTRACT(MONTH FROM date) = 1`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("month-only EXTRACT is not a contiguous range, got %#v", cands)
	}
}

func TestSuggestRewrites_ToCharMonth(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE to_char(date, 'YYYY-MM') = '2025-03'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "2025-03-01") || !strings.Contains(got, "2025-04-01") {
		t.Fatalf("expected March range, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_CoalesceDifferentDefault(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE COALESCE(region, 'Unknown') = 'North'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "coalesce_unwrap")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "region =") || !strings.Contains(got, "north") {
		t.Fatalf("expected region = North, got: %s", c.SQL)
	}
	if strings.Contains(strings.ToLower(got), "coalesce") {
		t.Fatalf("COALESCE should be removed, got: %s", c.SQL)
	}
	if strings.Contains(strings.ToLower(got), "is null") {
		t.Fatalf("default ≠ const should not add IS NULL, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_CoalesceMatchingDefault(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE COALESCE(region, 'North') = 'North'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "coalesce_unwrap")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "is null") {
		t.Fatalf("matching default must include IS NULL, got: %s", c.SQL)
	}
}

// Dropping a cast off a column is only safe when the column's actual
// PostgreSQL type makes the cast a no-op, which the AST alone cannot tell us.
// These two tests pin the deliberate non-rewrite so it is not reintroduced
// without catalog-resolved column types.
func TestSuggestRewrites_TextCastNotRewritten(t *testing.T) {
	// price numeric = 12.0 makes price::text = '12' FALSE but price = 12 TRUE;
	// on a text column the rewrite would not even parse (no text = integer op).
	for _, sql := range []string{
		`SELECT 1 FROM demo.sales WHERE quantity::text = '5'`,
		`SELECT 1 FROM demo.sales WHERE price::text = '12'`,
		`SELECT 1 FROM demo.sales WHERE sku::text = '5'`,
	} {
		for _, c := range SuggestRewrites(sql, nil) {
			got := normalizeSQL(c.SQL)
			if !strings.Contains(got, "::text") && !strings.Contains(got, "cast(") {
				t.Fatalf("text cast must not be dropped without catalog types.\n in: %s\nout: %s", sql, c.SQL)
			}
		}
	}
}

func TestSuggestRewrites_OrToUnion(t *testing.T) {
	sql := `SELECT id, date, region, product_category FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "or_to_union")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "union all") {
		t.Fatalf("expected UNION ALL, got: %s", c.SQL)
	}
	if !strings.Contains(got, "region") || !strings.Contains(got, "product_category") {
		t.Fatalf("expected both predicates, got: %s", c.SQL)
	}
	// The later branch must subtract the earlier one with a NULL-safe negation:
	// `(region = 'North') IS NOT TRUE`, never a bare `NOT (region = 'North')`
	// (which is NULL — and drops the row — when region is NULL).
	if !strings.Contains(got, "is not true") {
		t.Fatalf("expected `IS NOT TRUE` of the prior branch, got: %s", c.SQL)
	}
	if strings.Contains(got, "not (region") || strings.Contains(got, "not region") {
		t.Fatalf("bare NOT drops NULL-column rows; expected IS NOT TRUE, got: %s", c.SQL)
	}
}

// Splitting an OR into UNION ALL branches changes the result of an aggregate
// with no GROUP BY: SUM(x) WHERE a=1 OR b=2 becomes two partial sums (one row
// each) instead of one combined total. Pins the B-03 fix: the rewriter must
// fail closed here rather than propose a candidate the equivalence check
// later has to catch.
func TestSuggestRewrites_OrAggregateNoGroupBySkipped(t *testing.T) {
	for _, sql := range []string{
		`SELECT SUM(total_amount) FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics'`,
		`SELECT count(*) FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics'`,
	} {
		if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
			t.Fatalf("aggregate without GROUP BY must not be split across UNION ALL branches.\n in: %s\nout: %#v", sql, cands)
		}
	}
}

// UNION ALL branches are separate SELECTs, so an ORDER BY referencing a
// column absent from the (explicit) target list does not deparse to valid
// SQL. Pins the B-04 fix.
func TestSuggestRewrites_OrOrderByMissingColumnSkipped(t *testing.T) {
	sql := `SELECT id FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics' ORDER BY date`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("ORDER BY on a column outside the target list must not be rewritten, got %#v", cands)
	}
}

// The same query, but selecting the sorted column, must still rewrite —
// confirms the B-04 guard only rejects genuinely unresolvable ORDER BY
// columns, not every ORDER BY.
func TestSuggestRewrites_OrOrderBySelectedColumnStillRewrites(t *testing.T) {
	sql := `SELECT id, date FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics' ORDER BY date`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "or_to_union")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "union all") || !strings.Contains(got, "order by date") {
		t.Fatalf("expected UNION ALL with ORDER BY date preserved, got: %s", c.SQL)
	}
}

// ORDER BY an expression (not a plain column reference) is deparsed verbatim
// onto the UNION ALL result by this rewrite — it is never resolved or
// rebuilt — so if the expression touches a column absent from the target
// list (here, only `id` is selected), the result does not parse
// ("column date does not exist"). Confirmed by actually running the
// generated SQL: it fails with exactly that error.
func TestSuggestRewrites_OrOrderByExpressionSkipped(t *testing.T) {
	sql := `SELECT id FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics' ORDER BY date + 1`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("an ORDER BY expression this rewrite cannot resolve must not be rewritten, got %#v", cands)
	}
}

// A qualified ORDER BY reference (`demo.sales.date`) fails after the rewrite
// even when the referenced column IS selected: the UNION ALL result has no
// table alias in scope, so PostgreSQL rejects the qualifier ("missing
// FROM-clause entry"). Confirmed by actually running the generated SQL.
func TestSuggestRewrites_OrOrderByQualifiedColumnSkipped(t *testing.T) {
	sql := `SELECT id, date FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics' ORDER BY demo.sales.date`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("a qualified ORDER BY reference must not be rewritten (no alias in scope after UNION), got %#v", cands)
	}
}

// A wildcard target list makes every column name available, but it does not
// make a qualified ORDER BY reference safe — the union still has no table
// alias in scope. This must stay declined even with SELECT *.
func TestSuggestRewrites_OrOrderByQualifiedColumnSkippedEvenWithWildcard(t *testing.T) {
	sql := `SELECT * FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics' ORDER BY demo.sales.date`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("a qualified ORDER BY reference must not be rewritten even with a wildcard target list, got %#v", cands)
	}
}

// An ordinal ORDER BY position (`ORDER BY 2`) is always safe to deparse onto
// a UNION ALL result regardless of column names or aliasing, and must still
// rewrite normally.
func TestSuggestRewrites_OrOrderByOrdinalStillRewrites(t *testing.T) {
	sql := `SELECT id, date FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics' ORDER BY 2`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "or_to_union")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "union all") || !strings.Contains(got, "order by 2") {
		t.Fatalf("expected UNION ALL with ORDER BY 2 preserved, got: %s", c.SQL)
	}
}

// An aggregate hidden inside a CASE expression must be caught the same way a
// bare aggregate call is (B-03): splitting the OR into UNION ALL branches
// would give each branch its own COUNT(*) instead of one combined count.
func TestSuggestRewrites_OrAggregateInsideCaseSkipped(t *testing.T) {
	sql := `SELECT CASE WHEN COUNT(*) > 0 THEN 1 ELSE 0 END FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics'`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("an aggregate inside a CASE expression must not be split across UNION ALL branches, got %#v", cands)
	}
}

// bit_and is a real PostgreSQL aggregate that was missing from the name
// allowlist; without it, this query would be split the same wrong way as
// the SUM/COUNT cases in TestSuggestRewrites_OrAggregateNoGroupBySkipped.
func TestSuggestRewrites_OrAggregateBitAndSkipped(t *testing.T) {
	sql := `SELECT bit_and(id) FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics'`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("bit_and is an aggregate and must not be split across UNION ALL branches, got %#v", cands)
	}
}

func TestSuggestRewrites_OrComplexLeafSkipped(t *testing.T) {
	sql := `SELECT id, date, region, product_category FROM demo.sales WHERE region = 'North' OR product_category LIKE 'Elec%'`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("LIKE leaf must fail closed, got %#v", cands)
	}
}

func TestSuggestRewrites_OrMultiTableSkipped(t *testing.T) {
	sql := `SELECT a.id FROM demo.sales a, demo.sales b WHERE a.region = 'North' OR b.product_category = 'Electronics'`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("multi-table OR must fail closed, got %#v", cands)
	}
}

func TestSuggestRewrites_InExpressionTargetSkipped(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales s WHERE s.region IN (SELECT upper(region) FROM demo.sales)`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("expression subquery target must fail closed, got %#v", cands)
	}
}

func TestSuggestRewrites_OrWithGroupBySkipped(t *testing.T) {
	sql := `SELECT region, COUNT(*) FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics' GROUP BY region`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("GROUP BY OR→UNION is not equivalent, got %#v", cands)
	}
}

func TestSuggestRewrites_InToExists(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales s WHERE s.region IN (SELECT region FROM demo.sales)`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "in_to_exists")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "exists") {
		t.Fatalf("expected EXISTS, got: %s", c.SQL)
	}
	if strings.Contains(got, " in ") {
		t.Fatalf("IN should be removed, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_NotInToExists(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales s WHERE s.region NOT IN (SELECT region FROM demo.sales)`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "not_in_to_exists")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "not exists") && !strings.Contains(got, "not (exists") {
		t.Fatalf("expected NOT EXISTS, got: %s", c.SQL)
	}
	// A NULL subquery value and a NULL outer value both stop NOT IN from being TRUE, unless the subquery is
	// empty; the two `IS NULL` tests inside NOT EXISTS say exactly that.
	if strings.Count(got, "is null") != 2 || strings.Contains(got, "is not null") {
		t.Fatalf("NOT IN must test the subquery value and the outer value for NULL inside NOT EXISTS, got: %s", c.SQL)
	}
}

// Shapes where EXISTS is not the same statement are declined: under another NOT, for an operator that is
// not equality, and when both scopes name the same table.
func TestSuggestRewrites_InToExistsDeclinesWhatItCannotProve(t *testing.T) {
	for _, sql := range []string{
		`SELECT id FROM t WHERE NOT (v IN (SELECT v FROM s) OR k = 2)`,
		`SELECT id FROM t WHERE NOT (v NOT IN (SELECT v FROM s))`,
		`SELECT id FROM t WHERE NOT (k = 2 AND v NOT IN (SELECT v FROM s))`,
		`SELECT id FROM t WHERE v > ANY (SELECT v FROM s)`,
		`SELECT id FROM t WHERE v < ANY (SELECT v FROM s)`,
		`SELECT id FROM t WHERE id IN (SELECT parent_id FROM t)`,
		`SELECT id FROM t a WHERE id IN (SELECT parent_id FROM t a)`,
		`SELECT id FROM t WHERE id NOT IN (SELECT parent_id FROM t)`,
	} {
		for _, c := range SuggestRewrites(sql, nil) {
			if c.Category == "in_to_exists" || c.Category == "not_in_to_exists" {
				t.Errorf("%s\n  was rewritten to %s", sql, c.SQL)
			}
		}
	}
	// = ANY is IN, IN under AND / OR is still rewritten, and NOT (x IN ...) is NOT IN.
	for _, sql := range []string{
		`SELECT id FROM t WHERE v = ANY (SELECT v FROM s)`,
		`SELECT id FROM t WHERE k = 1 OR v IN (SELECT v FROM s)`,
	} {
		mustFindCategory(t, SuggestRewrites(sql, nil), "in_to_exists")
	}
	mustFindCategory(t, SuggestRewrites(`SELECT id FROM t WHERE NOT (v IN (SELECT v FROM s))`, nil), "not_in_to_exists")
}

func TestSuggestRewrites_MultiplePatterns(t *testing.T) {
	sql := `SELECT id, date, region, product_category FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01' AND (region = 'North' OR product_category = 'Electronics')`
	cands := SuggestRewrites(sql, nil)
	if len(cands) < 2 {
		t.Fatalf("expected sargable + OR→UNION candidates, got %#v", cands)
	}
	cats := map[string]bool{}
	for _, c := range cands {
		cats[c.Category] = true
	}
	if !cats["function_wrap"] || !cats["or_to_union"] {
		t.Fatalf("expected function_wrap and or_to_union, got %v", cats)
	}
}

func TestSuggestRewrites_DateTruncBetween(t *testing.T) {
	sql := `SELECT SUM(total_amount) FROM demo.sales WHERE DATE_TRUNC('month', date) BETWEEN DATE '2025-01-01' AND DATE '2025-03-31'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "between") || strings.Contains(strings.ToLower(got), "date_trunc") {
		t.Fatalf("expected sargable range, got: %s", c.SQL)
	}
	if !strings.Contains(got, "date >=") || !strings.Contains(got, "date <") {
		t.Fatalf("expected range bounds, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_DateTruncInequality(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) >= DATE '2025-02-01'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "date_trunc") {
		t.Fatalf("expected unwrap, got: %s", c.SQL)
	}
	if !strings.Contains(got, "date >=") {
		t.Fatalf("expected >= on bare column, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_NumericCastNotRewritten(t *testing.T) {
	// amount numeric = 1.4 makes amount::integer = 1 TRUE but amount = 1 FALSE.
	for _, sql := range []string{
		`SELECT 1 FROM demo.sales WHERE quantity::numeric = 5`,
		`SELECT 1 FROM demo.sales WHERE amount::integer = 1`,
		`SELECT 1 FROM demo.sales WHERE total::float8 = 100`,
	} {
		for _, c := range SuggestRewrites(sql, nil) {
			got := normalizeSQL(c.SQL)
			if !strings.Contains(got, "::") && !strings.Contains(got, "cast(") {
				t.Fatalf("numeric cast must not be dropped without catalog types.\n in: %s\nout: %s", sql, c.SQL)
			}
		}
	}
}

func TestSuggestRewrites_ParamEqualityRewritten(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) = $1`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	// The column is unwrapped; DATE_TRUNC survives only as the param-alignment guard.
	if strings.Contains(strings.ToLower(got), "date_trunc('month', date)") {
		t.Fatalf("expected the column wrap to be unwrapped, got: %s", c.SQL)
	}
	if !strings.Contains(got, "date >= $1") || !strings.Contains(got, "date < ($1 + '1 month'::interval)") {
		t.Fatalf("expected placeholder-preserving month range, got: %s", c.SQL)
	}
	if !strings.Contains(got, "date_trunc('month', $1) = $1") {
		t.Fatalf("expected the param-alignment guard, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_ParamInequalitySkipped(t *testing.T) {
	// A parameterized inequality is only safe if the bind is already unit-aligned,
	// which we cannot verify — fail closed.
	sql := `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) >= $1`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("parameterized inequality must fail closed: %#v", cands)
	}
}

func TestSuggestRewrites_ParamOrUnionSkipped(t *testing.T) {
	// OR→UNION ALL stays literals-only even when a param is present.
	sql := `SELECT 1 FROM demo.sales WHERE region = $1 OR product_category = $2`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("parameterized OR must not produce a UNION rewrite: %#v", cands)
	}
}

func TestSuggestRewrites_NestedSubqueryWhere(t *testing.T) {
	sql := `SELECT * FROM (SELECT id, date, total_amount FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01') s`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "date_trunc") {
		t.Fatalf("expected nested WHERE unwrap, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_CTEWhere(t *testing.T) {
	sql := `WITH m AS (SELECT id, date FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01') SELECT * FROM m`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "date_trunc") {
		t.Fatalf("expected CTE WHERE unwrap, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_CastDateBetween(t *testing.T) {
	sql := `SELECT SUM(total_amount) FROM demo.sales WHERE date::date BETWEEN DATE '2025-01-01' AND DATE '2025-01-31'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "between") || strings.Contains(strings.ToLower(got), "date::date") {
		t.Fatalf("expected sargable day range, got: %s", c.SQL)
	}
	if !strings.Contains(got, "date >=") || !strings.Contains(got, "date <") {
		t.Fatalf("expected range bounds, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_CastDateInequality(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE date::date >= DATE '2025-02-01'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "date::date") {
		t.Fatalf("expected unwrap, got: %s", c.SQL)
	}
	if !strings.Contains(got, "date >=") {
		t.Fatalf("expected >= on bare column, got: %s", c.SQL)
	}
}

// DeclineReason is the single source of truth for why SuggestRewrites found
// nothing (surfaced by both the API's decline_reason and the Investigate
// UI), so its claims about which patterns rewrite must stay true. Sourcery
// flagged it omitting date_part and narrowing COALESCE to date columns when
// SuggestRewrites' own doc comment (and this file's other tests) show both
// rewriting generally. Pin the two cases live, and pin the wording.
func TestDeclineReason_MatchesSupportedPatterns(t *testing.T) {
	if !strings.Contains(DeclineReason, "date_part") {
		t.Errorf("DeclineReason omits date_part, which SuggestRewrites supports: %q", DeclineReason)
	}
	if strings.Contains(DeclineReason, "COALESCE over a date") || strings.Contains(DeclineReason, "COALESCE over") {
		t.Errorf("DeclineReason must not narrow COALESCE to date columns — it also rewrites text columns: %q", DeclineReason)
	}

	if cands := SuggestRewrites(`SELECT 1 FROM demo.sales WHERE date_part('year', date) = 2025`, nil); len(cands) == 0 {
		t.Fatal("date_part must actually rewrite — DeclineReason claims it does")
	}
	if cands := SuggestRewrites(`SELECT 1 FROM demo.sales WHERE COALESCE(region, 'Unknown') = 'North'`, nil); len(cands) == 0 {
		t.Fatal("COALESCE on a non-date (text) column must actually rewrite — DeclineReason claims it does")
	}
}

func mustFindCategory(t *testing.T, cands []RewriteCandidate, category string) RewriteCandidate {
	t.Helper()
	for _, c := range cands {
		if c.Category == category {
			return c
		}
	}
	t.Fatalf("expected category %q in %#v", category, cands)
	return RewriteCandidate{}
}
