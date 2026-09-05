package queryrunner

import (
	"strings"
	"testing"
)

// Parameterized-query rewrites: the shape the regression poller reads from
// pg_stat_statements. Only unambiguous `=` / `BETWEEN` transforms are emitted;
// everything else fails closed.

func TestParamRewrite_InScope(t *testing.T) {
	cases := []struct {
		name     string
		sql      string
		wantSub  []string // substrings that must appear in the rewrite
		wantGone string   // substring that must NOT appear (the wrap that was removed)
	}{
		{
			// The column wrap is removed; the trailing `date_trunc('month', $1) = $1`
			// guard keeps the rewrite correct for a bind that is not month-aligned.
			name:     "date_trunc month equality",
			sql:      `SELECT category, SUM(amount) FROM demo.sales WHERE DATE_TRUNC('month', date) = $1 GROUP BY 1`,
			wantSub:  []string{"date >= $1", "date < ($1 + '1 month'::interval)", "date_trunc('month', $1) = $1"},
			wantGone: "date_trunc('month', date)",
		},
		{
			name:     "date_trunc day equality, param on the left",
			sql:      `SELECT 1 FROM demo.sales WHERE $1 = DATE_TRUNC('day', date)`,
			wantSub:  []string{"date >= $1", "date < ($1 + '1 day'::interval)", "date_trunc('day', $1) = $1"},
			wantGone: "date_trunc('day', date)",
		},
		{
			name:     "col::date equality casts the bind to date",
			sql:      `SELECT 1 FROM demo.sales WHERE date::date = $1`,
			wantSub:  []string{"date >= $1::date", "date < ($1::date + '1 day'::interval)"},
			wantGone: "::date =",
		},
		{
			name:     "CAST(col AS date) between casts each bind to date",
			sql:      `SELECT 1 FROM demo.sales WHERE CAST(date AS date) BETWEEN $1 AND $2`,
			wantSub:  []string{"date >= $1::date", "date < ($2::date + '1 day'::interval)"},
			wantGone: "cast(",
		},
		{
			name:     "param rewrite composes with other AND predicates",
			sql:      `SELECT 1 FROM demo.sales WHERE region = $2 AND DATE_TRUNC('month', date) = $1`,
			wantSub:  []string{"region = $2", "date >= $1", "date < ($1 + '1 month'::interval)", "date_trunc('month', $1) = $1"},
			wantGone: "date_trunc('month', date)",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mustFindCategory(t, SuggestRewrites(tc.sql, nil), "function_wrap")
			got := normalizeSQL(c.SQL)
			low := strings.ToLower(got)
			for _, sub := range tc.wantSub {
				if !strings.Contains(got, sub) {
					t.Fatalf("want %q in rewrite\n got: %s", sub, c.SQL)
				}
			}
			if tc.wantGone != "" && strings.Contains(low, tc.wantGone) {
				t.Fatalf("wrap %q should be gone\n got: %s", tc.wantGone, c.SQL)
			}
			if !strings.Contains(strings.ToLower(c.Rationale), "placeholder") {
				t.Errorf("rationale should note placeholders are preserved: %q", c.Rationale)
			}
		})
	}
}

func TestParamRewrite_OutOfScope_FailsClosed(t *testing.T) {
	cases := []struct {
		name string
		sql  string
	}{
		{"date_trunc >= param", `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) >= $1`},
		{"date_trunc > param", `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) > $1`},
		{"col::date < param", `SELECT 1 FROM demo.sales WHERE date::date < $1`},
		{"EXTRACT year = param", `SELECT 1 FROM demo.sales WHERE EXTRACT(YEAR FROM date) = $1`},
		{"date_part = param", `SELECT 1 FROM demo.sales WHERE date_part('year', date) = $1`},
		{"to_char = param", `SELECT 1 FROM demo.sales WHERE to_char(date, 'YYYY-MM') = $1`},
		{"COALESCE = param", `SELECT 1 FROM demo.sales WHERE COALESCE(region, 'x') = $1`},
		{"col::text = param", `SELECT 1 FROM demo.sales WHERE quantity::text = $1`},
		{"OR of two params", `SELECT 1 FROM demo.sales WHERE region = $1 OR product_category = $2`},
		{"IN subquery with param", `SELECT 1 FROM demo.sales WHERE id IN (SELECT id FROM demo.orders WHERE region = $1)`},
		{"BETWEEN with one literal one param", `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) BETWEEN DATE '2025-01-01' AND $1`},
		{"unknown trunc unit with param", `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('decade', date) = $1`},
		// A parameterized DATE_TRUNC BETWEEN cannot be made equivalent with a single
		// alignment guard (a misaligned lower bound shifts the range, not empties it),
		// and bind alignment is unknowable at rewrite time — so it is left alone.
		{"date_trunc BETWEEN two params", `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('quarter', date) BETWEEN $1 AND $2`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if cands := SuggestRewrites(tc.sql, nil); len(cands) != 0 {
				t.Fatalf("expected no rewrite (fail closed), got: %#v", cands)
			}
		})
	}
}

// The literal path is unchanged by the param work.
func TestParamRewrite_LiteralPathUnaffected(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "function_wrap")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "date_trunc") {
		t.Fatalf("literal unwrap regressed: %s", c.SQL)
	}
	if !strings.Contains(got, "'2025-01-01'::date") || !strings.Contains(got, "'2025-02-01'::date") {
		t.Fatalf("literal range regressed: %s", c.SQL)
	}
}
