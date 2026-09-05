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

func TestSubstituteParams_BackslashBindIsQuotedNotRejected(t *testing.T) {
	// Under standard_conforming_strings=on a backslash in a single-quoted string
	// is a literal char, so a LIKE / path bind must still work.
	got, err := SubstituteParams("SELECT 1 FROM demo.sales WHERE path LIKE $1", []string{`C:\logs\%`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, `'C:\logs\%'`) {
		t.Fatalf("backslash bind should be quoted literally, got: %s", got)
	}
	if _, sel, ok := parseSingleSelect(got); !ok || selectTreeContainsParamRef(sel) {
		t.Fatalf("result is not a clean param-free SELECT: %s", got)
	}
}

func TestSubstituteParams_BlockCommentDollarIsNotAParam(t *testing.T) {
	// `$1` only appears inside a /* */ block comment, so it is not a parameter:
	// nothing is substituted and the hostile bind is never used. This closes the
	// `... /* $1 */` + `x */ OR pg_sleep(10) --` injection.
	sql := "SELECT * FROM demo.sales WHERE id = 5 /* $1 */"
	got, err := SubstituteParams(sql, []string{`x */ OR pg_sleep(10) --`})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sql {
		t.Fatalf("a $N inside a block comment must not be substituted, got: %s", got)
	}
	if strings.Contains(stripQuotedAndComments(got), "pg_sleep") {
		t.Fatalf("block-comment payload escaped: %s", got)
	}
}

func TestSubstituteParams_RealParamBesideBlockComment(t *testing.T) {
	// A real $1 is still substituted when a decoy $2 sits in a block comment.
	got, err := SubstituteParams(
		"SELECT 1 FROM demo.sales WHERE region = $1 /* keep $2 out */",
		[]string{"North"},
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(got, "region = 'North'") || !strings.Contains(got, "/* keep $2 out */") {
		t.Fatalf("real param not substituted / comment altered: %s", got)
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
	cases := []struct{ bind, want string }{
		{"2025-01-01 12:30:00", "'2025-01-01 12:30:00'::timestamp"},
		{"2025-01-01T12:30:00.123456", "'2025-01-01T12:30:00.123456'::timestamp"},
		// Zone offset / Z → timestamptz so the offset is not dropped.
		{"2025-01-01T12:30:00.123456+00:00", "'2025-01-01T12:30:00.123456+00:00'::timestamptz"},
		{"2025-01-01 12:30:00Z", "'2025-01-01 12:30:00Z'::timestamptz"},
		{"2025-01-01T12:30:00-05:00", "'2025-01-01T12:30:00-05:00'::timestamptz"},
	}
	for _, tc := range cases {
		got, err := SubstituteParams("SELECT 1 FROM demo.sales WHERE date = $1", []string{tc.bind})
		if err != nil {
			t.Fatalf("%q: unexpected error %v", tc.bind, err)
		}
		if !strings.Contains(got, tc.want) {
			t.Fatalf("%q should produce %s, got: %s", tc.bind, tc.want, got)
		}
	}
}
