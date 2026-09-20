package pqncli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// spyBackend remembers what reached the database.
type spyBackend struct {
	fakeBackend
	ran     []string
	limit   int
	records int
}

func (s *spyBackend) Run(ctx context.Context, sql string, n int) (*RunResult, error) {
	s.ran, s.limit = append(s.ran, sql), n
	return s.fakeBackend.Run(ctx, sql, n)
}
func (s *spyBackend) RecordInvestigation(ctx context.Context, q, title string, id *int64) (int64, error) {
	s.records++
	return s.fakeBackend.RecordInvestigation(ctx, q, title, id)
}

// A flag means the same thing before the statement, after it, or between its words.
func TestFlagsWorkAfterTheStatement(t *testing.T) {
	const bound = "SELECT * FROM shop.orders WHERE date_trunc('day', created_at) = $1"
	var dsns [2]string
	newBE := func() *spyBackend {
		return &spyBackend{fakeBackend: fakeBackend{measure: &Measurement{Equal: true, BeforeRows: 10, AfterRows: 10, BeforeMs: 100, AfterMs: 10, Speedup: 10},
			proof: &Proof{Verdict: "Proven"}}}
	}
	run := func(be *spyBackend, args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		connect := func(_ context.Context, primary, replica string) (Backend, error) {
			dsns = [2]string{primary, replica}
			return be, nil
		}
		code := Main(args, &out, &errb, func(string) string { return "" }, connect)
		return code, out.String(), errb.String()
	}

	for _, args := range [][]string{
		{"run", "SELECT 1 AS n", "--json"},
		{"run", "--json", "SELECT 1 AS n"},
		{"run", "SELECT", "--json", "1 AS n"},
		{"run", "--json", "--", "SELECT 1 AS n"},
	} {
		be := newBE()
		code, out, errs := run(be, args...)
		if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "{") || len(be.ran) != 1 || strings.Contains(be.ran[0], "json") {
			t.Errorf("%q: exit %d, sql %q, out %.40q, err %q", args, code, be.ran, out, errs)
		}
	}

	be := newBE()
	if code, _, _ := run(be, "run", "SELECT id FROM orders", "-n", "3"); code != 0 || be.limit != 3 || be.ran[0] != "SELECT id FROM orders" {
		t.Errorf("-n after the statement: exit %d limit %d sql %q", code, be.limit, be.ran)
	}
	if code, _, _ := run(be, "run", "SELECT 1", "--dsn=postgres://a", "--replica", "postgres://b"); code != 0 || dsns != [2]string{"postgres://a", "postgres://b"} {
		t.Errorf("--dsn/--replica after the statement: exit %d dsns %v", code, dsns)
	}

	be = newBE()
	run(be, "investigate", dayQuery, "--no-record")
	if be.records != 0 || len(be.evidence) != 0 {
		t.Errorf("--no-record after the statement must write nothing: %d investigations, evidence %v", be.records, be.evidence)
	}
	be = newBE()
	run(be, "investigate", dayQuery)
	if be.records != 1 {
		t.Errorf("without --no-record it must record: %d", be.records)
	}

	be = newBE()
	if code, out, _ := run(be, "investigate", bound, "--bind", "2025-03-01", "--no-record"); code != 0 || be.measured == 0 || strings.Contains(out, "Unverified") {
		t.Errorf("--bind after the statement must measure: exit %d measured %d %.200q", code, be.measured, out)
	}
	if strings.Contains(strings.Join(be.planCalls, "|"), "--bind") {
		t.Errorf("a flag leaked into the statement: %q", be.planCalls)
	}
}

// Words that look like flags are refused, not sent to the database as SQL.
func TestStatementWordsAreNotSwallowedSilently(t *testing.T) {
	be := &spyBackend{}
	run := func(args ...string) (int, string) {
		var out, errb bytes.Buffer
		code := Main(args, &out, &errb, func(string) string { return "" },
			func(context.Context, string, string) (Backend, error) { return be, nil })
		return code, errb.String()
	}

	if code, errs := run("run", "SELECT 1", "--nonexistent"); code != 1 || !strings.Contains(errs, "quotes or after --") || len(be.ran) != 0 {
		t.Errorf("an unknown flag must fail with a hint and send nothing: %d %q %q", code, errs, be.ran)
	}
	for _, args := range [][]string{
		{"run", "--sql", "SELECT 1", "SELECT", "2"},
		{"run", "--sql", "SELECT 1", "--file", "x.sql"},
		{"plan", "--file", "x.sql", "SELECT 2"},
	} {
		if code, errs := run(args...); code != 1 || !strings.Contains(errs, "statement once") || len(be.ran) != 0 {
			t.Errorf("%q: the statement given twice must be refused: %d %q", args, code, errs)
		}
	}
	for _, args := range [][]string{{"top", "5"}, {"doctor", "x"}, {"investigations", "1"}, {"evidence", "1", "2"}} {
		if code, errs := run(args...); code != 1 || !strings.Contains(errs, "argument") {
			t.Errorf("%q: a stray argument must be refused: %d %q", args, code, errs)
		}
	}
	if code, _ := run("run", "SELECT 1", "--timeout"); code != 1 {
		t.Errorf("a value flag with no value must fail: %d", code)
	}
}

// A dash that is part of the statement is kept: numbers, and a SQL comment after the first word.
func TestDashesInsideAStatement(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"run", "SELECT", "-1", "AS", "n"}, "SELECT -1 AS n"},
		{[]string{"run", "SELECT", "-1.5", "AS", "n"}, "SELECT -1.5 AS n"},
		{[]string{"run", "--", "SELECT", "-x"}, "SELECT -x"},
		{[]string{"run", "SELECT", "1", "--", "a", "comment", "--json"}, "SELECT 1 -- a comment --json"},
		{[]string{"run", "--title", "--", "SELECT 1"}, "SELECT 1"},
	} {
		be := &spyBackend{}
		var out, errb bytes.Buffer
		Main(tc.args, &out, &errb, func(string) string { return "" },
			func(context.Context, string, string) (Backend, error) { return be, nil })
		if len(be.ran) != 1 || be.ran[0] != tc.want {
			t.Errorf("%q: sent %q, want %q (%s)", tc.args, be.ran, tc.want, errb.String())
		}
	}
}
