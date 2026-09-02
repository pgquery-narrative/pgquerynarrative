package queryrunner

import (
	"strings"
	"testing"
)

func TestSubstituteParams(t *testing.T) {
	cases := []struct {
		name  string
		sql   string
		binds []string
		want  string
	}{
		{
			name:  "date range with cast so interval arithmetic type-checks",
			sql:   "SELECT 1 FROM demo.sales WHERE date >= $1 AND date < ($1 + '1 month'::interval)",
			binds: []string{"2025-01-01"},
			want:  "SELECT 1 FROM demo.sales WHERE date >= '2025-01-01'::date AND date < ('2025-01-01'::date + '1 month'::interval)",
		},
		{
			name:  "mixed string and numeric binds",
			sql:   "SELECT 1 FROM demo.sales WHERE region = $1 AND quantity > $2",
			binds: []string{"North", "5"},
			want:  "SELECT 1 FROM demo.sales WHERE region = 'North' AND quantity > 5",
		},
		{
			name:  "BETWEEN with two params",
			sql:   "SELECT 1 FROM demo.sales WHERE date >= $1 AND date < ($2 + '1 day'::interval)",
			binds: []string{"2025-01-01", "2025-01-31"},
			want:  "SELECT 1 FROM demo.sales WHERE date >= '2025-01-01'::date AND date < ('2025-01-31'::date + '1 day'::interval)",
		},
		{
			name:  "dollar token inside a string literal is left alone",
			sql:   "SELECT 1 FROM demo.sales WHERE note = 'costs $1 total' AND region = $1",
			binds: []string{"North"},
			want:  "SELECT 1 FROM demo.sales WHERE note = 'costs $1 total' AND region = 'North'",
		},
		{
			name:  "single quote in a bind value is escaped",
			sql:   "SELECT 1 FROM demo.sales WHERE region = $1",
			binds: []string{"O'Brien"},
			want:  "SELECT 1 FROM demo.sales WHERE region = 'O''Brien'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := SubstituteParams(tc.sql, tc.binds)
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if normalizeSQL(got) != normalizeSQL(tc.want) {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestSubstituteParams_Errors(t *testing.T) {
	if _, err := SubstituteParams("SELECT 1 FROM demo.sales WHERE x = $1 AND y = $2", []string{"a"}); err == nil {
		t.Error("expected an error when fewer binds than params are supplied")
	}
	if _, err := SubstituteParams("SELECT 1 FROM demo.sales WHERE x = $1 AND y = $tag$ z $tag$", []string{"a"}); err == nil {
		t.Error("expected dollar-quoted strings to be refused when substitution is attempted")
	}
}

func TestSubstituteParams_NoParams(t *testing.T) {
	sql := "SELECT 1 FROM demo.sales WHERE region = 'North'"
	got, err := SubstituteParams(sql, nil)
	if err != nil || got != sql {
		t.Fatalf("got %q err %v", got, err)
	}
}

// A hostile bind value cannot break out — it becomes a quoted string literal and
// the result must still parse as a single param-free SELECT.
func TestSubstituteParams_InjectionNeutralized(t *testing.T) {
	got, err := SubstituteParams(
		"SELECT 1 FROM demo.sales WHERE region = $1",
		[]string{"x'); DROP TABLE demo.sales; --"},
	)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(strings.ToUpper(got), "DROP TABLE") && !strings.Contains(got, "'") {
		t.Fatalf("value was not quoted: %s", got)
	}
	// still one param-free SELECT
	if _, sel, ok := parseSingleSelect(got); !ok || selectTreeContainsParamRef(sel) {
		t.Fatalf("result is not a clean param-free SELECT: %s", got)
	}
}
