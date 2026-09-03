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

// A bind that starts like an ISO timestamp but carries a SQL payload must not
// take the bare `'..'::timestamp` path: the value has to end up fully inside a
// single-quoted, quote-doubled literal (or be rejected). This is the
// unanchored-timestampRe / unescaped-timestamp-branch regression.
func TestSubstituteParams_HostileTimestampPrefixBinds(t *testing.T) {
	sql := "SELECT 1 FROM demo.sales WHERE date >= $1 AND date < ($1 + '1 month'::interval)"

	// The SQL skeleton with every string literal and -- comment blanked. A hostile
	// bind that stays inside its quotes leaves this identical to a benign plain
	// string bind (which, like the hostile ones, takes the unquoted-cast-free path).
	benign, err := SubstituteParams(sql, []string{"North"})
	if err != nil {
		t.Fatalf("benign substitution failed: %v", err)
	}
	wantSkeleton := stripQuotedAndComments(benign)

	hostile := []string{
		`2025-01-01T00:00:00'::timestamp OR 1=1 --`,
		`2025-01-01 00:00' OR '1'='1`,
		`2025-01-01T00:00:00'::text || (SELECT current_user) --`,
		`2025-01-01' ; DROP TABLE demo.sales -- `,
		`2025-01-01T00:00:00) OR 1=1 --`,
	}
	for _, b := range hostile {
		t.Run(b, func(t *testing.T) {
			got, err := SubstituteParams(sql, []string{b})
			if err != nil {
				return // rejecting outright is a valid neutralization
			}
			// Every caller quote must be doubled (breakout neutralized).
			esc := "'" + strings.ReplaceAll(strings.TrimSpace(b), "'", "''") + "'"
			if !strings.Contains(got, esc) {
				t.Fatalf("hostile bind not emitted as a quote-doubled literal:\n bind: %q\n  got: %s", b, got)
			}
			// Nothing the caller supplied may appear outside a string literal.
			if skel := stripQuotedAndComments(got); skel != wantSkeleton {
				t.Fatalf("hostile bind changed the SQL skeleton:\n want: %s\n  got: %s\n from: %s", wantSkeleton, skel, got)
			}
			if _, sel, ok := parseSingleSelect(got); !ok || selectTreeContainsParamRef(sel) {
				t.Fatalf("result is not a clean param-free SELECT: %s", got)
			}
		})
	}
}

func TestSubstituteParams_RejectsUnsafeBytes(t *testing.T) {
	cases := []struct {
		name string
		bind string
	}{
		{"backslash", `C:\Users\x`},
		{"nul byte", "a\x00b"},
		{"over length", strings.Repeat("x", maxBindValueLen+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := SubstituteParams("SELECT 1 FROM demo.sales WHERE region = $1", []string{tc.bind}); err == nil {
				t.Fatalf("expected %s bind to be rejected", tc.name)
			}
		})
	}
}

func TestSubstituteParams_NonFiniteNumbersAreQuoted(t *testing.T) {
	// strconv.ParseFloat would accept these; emitting them bare would let the
	// planner read `NaN` / `Infinity` as column references or worse.
	for _, b := range []string{"NaN", "Infinity", "-Inf", "0x1p4", "1_000"} {
		got, err := SubstituteParams("SELECT 1 FROM demo.sales WHERE quantity > $1", []string{b})
		if err != nil {
			t.Fatalf("%q: unexpected error %v", b, err)
		}
		if !strings.Contains(got, "'"+b+"'") {
			t.Fatalf("%q must be quoted, got: %s", b, got)
		}
	}
}

func TestSubstituteParams_TimestampBindKeepsCast(t *testing.T) {
	for _, b := range []string{"2025-01-01 12:30:00", "2025-01-01T12:30:00.123456+00:00", "2025-01-01 12:30:00Z"} {
		got, err := SubstituteParams("SELECT 1 FROM demo.sales WHERE date = $1", []string{b})
		if err != nil {
			t.Fatalf("%q: unexpected error %v", b, err)
		}
		want := "'" + b + "'::timestamp"
		if !strings.Contains(got, want) {
			t.Fatalf("%q should keep the timestamp cast (%s), got: %s", b, want, got)
		}
	}
}
