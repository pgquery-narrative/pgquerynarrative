package queryrunner

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/internal/errors"
)

func TestExecutionFailedKeepsOnlyStatementCausedDetail(t *testing.T) {
	for code, want := range map[string]bool{
		"42703": true, "42501": true, "22P02": true, "0A000": true, "54001": true, "57014": true, "25006": true,
		"XX000": false, "53100": false, "58030": false, "08006": false, "40001": false, "": false,
	} {
		err := executionFailed(&pgconn.PgError{Code: code, Message: "column orders.label does not exist", Detail: "secret detail", Hint: "hint"})
		if !errors.Is(err, apperrors.ErrQueryExecutionFailed) {
			t.Fatalf("%s: sentinel lost", code)
		}
		if got := strings.Contains(err.Error(), "SQLSTATE "+code); got != want {
			t.Fatalf("%s: detail kept = %v, want %v (%v)", code, got, want, err)
		}
		if strings.Contains(err.Error(), "secret detail") || strings.Contains(err.Error(), "hint") {
			t.Fatalf("%s: DETAIL/HINT must never be kept: %v", code, err)
		}
	}
	if err := executionFailed(errors.New("dial tcp: connection refused")); err != apperrors.ErrQueryExecutionFailed {
		t.Fatalf("non-PostgreSQL errors stay opaque: %v", err)
	}
	long := executionFailed(&pgconn.PgError{Code: "22P02", Message: strings.Repeat("é", 400)})
	if !utf8.ValidString(long.Error()) || len(long.Error()) > 400 {
		t.Fatalf("long message not cut safely: %d bytes", len(long.Error()))
	}
}

func TestParseSearchPath(t *testing.T) {
	for in, want := range map[string][]string{
		`"api", "public"`:             {"api", "public"},
		`"$user", public, extensions`: {"public", "extensions"},
		`"Mixed""Case", api`:          {`Mixed"Case`, "api"},
		``:                            nil,
	} {
		if got := ParseSearchPath(in); !reflect.DeepEqual(got, want) {
			t.Fatalf("ParseSearchPath(%q) = %q, want %q", in, got, want)
		}
	}
}

// The search path applied in a replay never leaves the connection's allowlist.
func TestReplaySearchPathStaysInAllowlist(t *testing.T) {
	r := NewRunner(nil, NewValidator([]string{"data", "api"}, 10000), 10, 0)
	if got := r.ReplaySearchPath(t.Context(), []string{"api", "extensions", "public", "data"}); !reflect.DeepEqual(got, []string{"api", "data"}) {
		t.Fatalf("applied search_path = %q", got)
	}
	stmts := r.replayStatements(WithReplayContext(t.Context(), &ReplayContext{
		SearchPath: []string{"api", "extensions"},
		Settings:   map[string]string{"timezone": "America/New_York", "statement_timeout": "20ms"},
		Method:     "GET", Path: "/orders",
	}))
	var rendered []string
	for _, s := range stmts {
		rendered = append(rendered, s.sql+" "+toStrings(s.args))
	}
	all := strings.Join(rendered, "\n")
	for _, want := range []string{`search_path "api"`, "timezone America/New_York", "request.method GET", "request.path /orders"} {
		if !strings.Contains(all, want) {
			t.Fatalf("replay missing %q:\n%s", want, all)
		}
	}
	for _, bad := range []string{"extensions", "statement_timeout", "request.jwt.claims", "SET LOCAL ROLE"} {
		if strings.Contains(all, bad) {
			t.Fatalf("replay must not apply %q without a role / outside policy:\n%s", bad, all)
		}
	}
}

// ReplayContext carries no field for request.jwt.claims — regardless of role,
// a replay must never be able to set it, because the capture that produces a
// ReplayContext is caller-supplied log text with no verification it is
// authentic. A forged claims value would let any principal who can create a
// PostgREST-imported investigation choose the identity an RLS policy keyed on
// it (e.g. auth.uid()) evaluates for. This is a security regression test: see
// TestForgedCaptureImpersonatesArbitraryIdentity in test/integration for a
// live proof against real PostgreSQL/PostgREST RLS.
func TestReplayNeverSetsJWTClaimsEvenWithRole(t *testing.T) {
	r := NewRunner(nil, NewValidator([]string{"api"}, 10000), 10, 0)
	stmts := r.replayStatements(WithReplayContext(t.Context(), &ReplayContext{
		Role: "authenticated", SearchPath: []string{"api"}, Method: "GET", Path: "/secrets",
	}))
	var rendered []string
	for _, s := range stmts {
		rendered = append(rendered, s.sql+" "+toStrings(s.args))
	}
	all := strings.Join(rendered, "\n")
	if !strings.Contains(all, `SET LOCAL ROLE "authenticated"`) {
		t.Fatalf("role was not applied: %s", all)
	}
	if strings.Contains(all, "request.jwt.claims") || strings.Contains(all, "jwt") {
		t.Fatalf("claims must never be replayable, even with a role set: %s", all)
	}
}

func toStrings(args []any) string {
	var parts []string
	for _, a := range args {
		parts = append(parts, a.(string))
	}
	return strings.Join(parts, " ")
}
