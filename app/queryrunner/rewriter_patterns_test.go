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

func TestSuggestRewrites_TextCastNumeric(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE quantity::text = '5'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "implicit_cast")
	got := normalizeSQL(c.SQL)
	if strings.Contains(got, "::text") || strings.Contains(got, "cast(") {
		t.Fatalf("text cast should be removed, got: %s", c.SQL)
	}
	if !strings.Contains(got, "quantity = 5") && !strings.Contains(got, "quantity=5") {
		t.Fatalf("expected quantity = 5, got: %s", c.SQL)
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
	if !strings.Contains(got, "is not null") {
		t.Fatalf("NULL-safe NOT IN must include IS NOT NULL, got: %s", c.SQL)
	}
	if !strings.Contains(got, "is null") {
		t.Fatalf("NULL-safe NOT IN must treat subquery NULLs, got: %s", c.SQL)
	}
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

func TestSuggestRewrites_NumericCast(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE quantity::numeric = 5`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "implicit_cast")
	got := normalizeSQL(c.SQL)
	if strings.Contains(got, "::numeric") {
		t.Fatalf("cast should move off column, got: %s", c.SQL)
	}
	if !strings.Contains(got, "quantity = 5") {
		t.Fatalf("expected bare column compare, got: %s", c.SQL)
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
