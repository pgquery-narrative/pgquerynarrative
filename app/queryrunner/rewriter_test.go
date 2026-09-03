package queryrunner

import (
	"strings"
	"testing"
)

func TestSuggestRewrites_DateTruncMonth(t *testing.T) {
	sql := `SELECT product_category, SUM(total_amount) AS revenue
FROM demo.sales
WHERE DATE_TRUNC('month', date) = DATE '2025-01-01'
GROUP BY product_category
ORDER BY revenue DESC`

	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	got := normalizeSQL(cands[0].SQL)
	if !strings.Contains(got, "date >=") || !strings.Contains(got, "date <") {
		t.Fatalf("expected range predicate, got: %s", cands[0].SQL)
	}
	if !strings.Contains(got, "2025-01-01") || !strings.Contains(got, "2025-02-01") {
		t.Fatalf("expected Jan→Feb bounds, got: %s", cands[0].SQL)
	}
	if strings.Contains(strings.ToLower(got), "date_trunc") {
		t.Fatalf("DATE_TRUNC should be removed, got: %s", cands[0].SQL)
	}
	if cands[0].Category != "function_wrap" {
		t.Fatalf("category = %q, want function_wrap", cands[0].Category)
	}
	if !strings.Contains(strings.ToLower(cands[0].Rationale), "date_trunc") {
		t.Fatalf("rationale should mention DATE_TRUNC: %s", cands[0].Rationale)
	}
}

func TestSuggestRewrites_DateTruncDay(t *testing.T) {
	sql := `SELECT 1 FROM demo.events WHERE DATE_TRUNC('day', created_at) = '2025-03-15'`
	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	got := normalizeSQL(cands[0].SQL)
	if !strings.Contains(got, "2025-03-15") || !strings.Contains(got, "2025-03-16") {
		t.Fatalf("expected day range, got: %s", cands[0].SQL)
	}
}

func TestSuggestRewrites_FlippedOperands(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE DATE '2025-01-01' = DATE_TRUNC('month', date)`
	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	got := normalizeSQL(cands[0].SQL)
	if !strings.Contains(got, "2025-01-01") || !strings.Contains(got, "2025-02-01") {
		t.Fatalf("expected month range, got: %s", cands[0].SQL)
	}
}

func TestSuggestRewrites_AndPreservesOtherPredicates(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE region = 'North' AND DATE_TRUNC('month', date) = DATE '2025-06-01'`
	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	got := normalizeSQL(cands[0].SQL)
	if !strings.Contains(got, "region") || !strings.Contains(got, "north") {
		t.Fatalf("expected region predicate preserved, got: %s", cands[0].SQL)
	}
	if !strings.Contains(got, "2025-06-01") || !strings.Contains(got, "2025-07-01") {
		t.Fatalf("expected June→July bounds, got: %s", cands[0].SQL)
	}
}

func TestSuggestRewrites_QualifiedColumn(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales s WHERE DATE_TRUNC('month', s.date) = DATE '2025-01-01'`
	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	got := normalizeSQL(cands[0].SQL)
	if !strings.Contains(got, "s.date >=") || !strings.Contains(got, "s.date <") {
		t.Fatalf("expected qualified column range, got: %s", cands[0].SQL)
	}
}

func TestSuggestRewrites_MisalignedDateTruncConstIsNotRewritten(t *testing.T) {
	// DATE_TRUNC('month', date) always returns midnight on the 1st, so
	// `= DATE '2025-01-15'` matches no rows at all. Rewriting it to the January
	// range would invent a month of matches — the rewrite must be skipped.
	sql := `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-15'`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("expected no candidate for a misaligned DATE_TRUNC constant, got: %#v", cands)
	}
}

func TestSuggestRewrites_AlignedDateTruncConstIsRewritten(t *testing.T) {
	// The alignment-safe constant still unwraps to the sargable month range.
	sql := `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01'`
	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	got := normalizeSQL(cands[0].SQL)
	if !strings.Contains(got, "2025-01-01") || !strings.Contains(got, "2025-02-01") {
		t.Fatalf("expected month bounds, got: %s", cands[0].SQL)
	}
	if strings.Contains(strings.ToLower(got), "date_trunc") {
		t.Fatalf("DATE_TRUNC should be unwrapped, got: %s", cands[0].SQL)
	}
}

func TestSuggestRewrites_FindingsBoostConfidence(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01'`
	without := SuggestRewrites(sql, nil)
	with := SuggestRewrites(sql, []PlanFinding{{
		Category: CategorySeqScan,
		Message:  "Seq Scan — function-wrapped partition/index key blocks pruning",
	}})
	if len(without) != 1 || len(with) != 1 {
		t.Fatalf("expected candidates")
	}
	if without[0].Confidence != "medium" {
		t.Fatalf("without findings confidence = %q, want medium", without[0].Confidence)
	}
	if with[0].Confidence != "high" {
		t.Fatalf("with findings confidence = %q, want high", with[0].Confidence)
	}
}

func TestSuggestRewrites_NoDateTrunc(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE date >= DATE '2025-01-01' AND date < DATE '2025-02-01'`
	if cands := SuggestRewrites(sql, nil); len(cands) != 0 {
		t.Fatalf("expected no candidates, got %#v", cands)
	}
}

func TestSuggestRewrites_EmptySQL(t *testing.T) {
	if cands := SuggestRewrites("", nil); len(cands) != 0 {
		t.Fatalf("expected nil/empty, got %#v", cands)
	}
}

func TestSuggestRewrites_CastDateEquality(t *testing.T) {
	sql := `SELECT COUNT(*) FROM demo.sales WHERE date::date = DATE '2025-01-15'`
	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	out := strings.ToLower(cands[0].SQL)
	if !strings.Contains(out, ">=") || !strings.Contains(out, "<") {
		t.Fatalf("expected sargable range, got %s", cands[0].SQL)
	}
	if strings.Contains(out, "::date =") || (strings.Contains(out, "cast(") && strings.Contains(out, " as date)")) {
		t.Fatalf("cast equality should be rewritten away: %s", cands[0].SQL)
	}
	if !strings.Contains(strings.ToLower(cands[0].Rationale), "date") {
		t.Fatalf("rationale should mention cast/date: %s", cands[0].Rationale)
	}
}

func TestSuggestRewrites_CastAsDateEquality(t *testing.T) {
	sql := `SELECT product_category FROM demo.sales WHERE CAST(date AS date) = '2025-03-01' GROUP BY 1`
	cands := SuggestRewrites(sql, nil)
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if !strings.Contains(cands[0].SQL, ">=") || !strings.Contains(cands[0].SQL, "<") {
		t.Fatalf("expected range rewrite, got %s", cands[0].SQL)
	}
}

func TestSuggestRewrites_DemoScenarioSQL(t *testing.T) {
	// Mirrors the slow-dashboard demo SQL; engine must work without DemoScenarios.
	sql := `SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01' GROUP BY product_category ORDER BY revenue DESC`
	cands := SuggestRewrites(sql, []PlanFinding{{
		Category: CategoryPartitionPruning,
		Message:  "Append with no partition pruning",
	}})
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	got := normalizeSQL(cands[0].SQL)
	wantBits := []string{"date >=", "date <", "2025-01-01", "2025-02-01", "product_category", "demo.sales"}
	for _, bit := range wantBits {
		if !strings.Contains(got, bit) {
			t.Fatalf("rewritten SQL missing %q: %s", bit, cands[0].SQL)
		}
	}
}

func normalizeSQL(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
