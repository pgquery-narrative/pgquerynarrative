package service

import (
	"context"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
)

func TestPostgRESTPlanSQLPreservesCapturedText(t *testing.T) {
	for _, value := range []string{"001", "  paid  ", "true", "2023-03-01", "O'Reilly"} {
		t.Run(value, func(t *testing.T) {
			got, err := planSQL("SELECT $1::text", []string{value})
			if err != nil {
				t.Fatal(err)
			}
			want := "SELECT E'" + strings.ReplaceAll(value, "'", "''") + "'::text"
			if got != want {
				t.Fatalf("captured value changed: got %q, want %q", got, want)
			}
		})
	}
}

func TestPostgRESTCompareUsesCapturedBinds(t *testing.T) {
	inv := &investigations.Investigation{SQL: "SELECT $1::text", Binds: []string{" 001 "}, PostgrestRequest: &investigations.PostgRESTRequest{}}
	p := &investigations.AddCandidatePayload{CandidateSQL: "SELECT $1::text WHERE true"}
	before, after, binds, err := investigationCompareSQL(inv, p)
	if err != nil || before != "SELECT E' 001 '::text" || after != "SELECT E' 001 '::text WHERE true" || !reflect.DeepEqual(binds, inv.Binds) {
		t.Fatalf("comparison: before=%q after=%q binds=%q err=%v", before, after, binds, err)
	}
	p.Binds = []string{"001"}
	if _, _, _, err := investigationCompareSQL(inv, p); err == nil {
		t.Fatal("a changed bind must not be compared as evidence of the captured request")
	}
	p.Binds = nil
	p.CandidateSQL = "SELECT $2::text"
	if _, _, _, err := investigationCompareSQL(inv, p); err == nil {
		t.Fatal("missing candidate bind must fail")
	}
	p.CandidateSQL = "SELECT $1::text"
	inv.Binds = nil
	if _, _, _, err := investigationCompareSQL(inv, p); err == nil {
		t.Fatal("missing stored binds must not fall back to a generic plan")
	}
	inv.PostgrestRequest = nil
	p.Binds = []string{"manual"}
	before, after, binds, err = investigationCompareSQL(inv, p)
	if err != nil || before != inv.SQL || after != p.CandidateSQL || !reflect.DeepEqual(binds, p.Binds) {
		t.Fatal("manual investigations must retain the existing bind path")
	}
}

func TestPostgRESTRejectsInvalidHTTPTimingBeforeDB(t *testing.T) {
	for _, duration := range []float64{-1, math.NaN(), math.Inf(1)} {
		_, err := (&InvestigationsService{}).CreateFromPostgrest(context.Background(), &investigations.CreateFromPostgRESTPayload{HTTPDurationMs: &duration})
		var validation *investigations.ValidationError
		if !errors.As(err, &validation) || !strings.Contains(validation.Message, "finite and non-negative") {
			t.Fatalf("invalid duration %v: %v", duration, err)
		}
	}
}

// Server-Timing that is supplied but unusable must be rejected, not silently
// dropped from the evidence. It fails before any database work.
func TestPostgRESTRejectsUnusableServerTimingBeforeDB(t *testing.T) {
	log, err := os.ReadFile("../queryrunner/testdata/postgrest_orders_daily.txt")
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"jwt;dur=0.6", "transaction;dur=-3", "garbage"} {
		_, err := (&InvestigationsService{}).CreateFromPostgrest(context.Background(), &investigations.CreateFromPostgRESTPayload{PostgresLog: string(log), ServerTiming: &header})
		var validation *investigations.ValidationError
		if !errors.As(err, &validation) || !strings.Contains(validation.Message, "transaction;dur=") {
			t.Fatalf("Server-Timing %q: %v", header, err)
		}
	}
}

// Limitations list only what the replay really does not reproduce: settings
// and search_path schemas it applies are not limitations.
func TestPostgRESTLimitationsMatchTheReplay(t *testing.T) {
	c := &queryrunner.PostgRESTCapture{ContextLogged: true, Role: "web_anon", SearchPath: `"api", "extensions"`,
		Settings: map[string]string{"timezone": "America/New_York", "statement_timeout": "20ms"}, OtherSettings: 1}
	asConnection := strings.Join(postgrestReplayLimitations(c, &queryrunner.ReplayContext{}, []string{"api"}), "\n")
	for _, want := range []string{`captured PostgREST role "web_anon" is not impersonated`, "request.jwt.claims", "extensions is outside", "statement_timeout=20ms", "1 other setting(s)"} {
		if !strings.Contains(asConnection, want) {
			t.Fatalf("limitations missing %q:\n%s", want, asConnection)
		}
	}
	if strings.Contains(asConnection, "timezone") {
		t.Fatalf("an applied setting is not a limitation:\n%s", asConnection)
	}
	asRole := strings.Join(postgrestReplayLimitations(c, &queryrunner.ReplayContext{Role: "web_anon"}, []string{"api", "extensions"}), "\n")
	if strings.Contains(asRole, "not impersonated") || strings.Contains(asRole, "search_path") {
		t.Fatalf("replay as the captured role with the full path:\n%s", asRole)
	}
	// Even with the role impersonated, claims must still be disclosed as unreplayed.
	if !strings.Contains(asRole, "headers, cookies and JWT claims") || !strings.Contains(asRole, "request.jwt.claims") {
		t.Fatalf("role replay must still disclose that claims are never replayed:\n%s", asRole)
	}
	noContext := strings.Join(postgrestReplayLimitations(&queryrunner.PostgRESTCapture{}, &queryrunner.ReplayContext{}, nil), "\n")
	if !strings.Contains(noContext, "session context was not logged") {
		t.Fatalf("missing context not disclosed:\n%s", noContext)
	}
}

// An unknown Prepared status (a failed statement recovered only from the
// error context) must not be reported as a confident "this ran unprepared" —
// the plan-cache note has to say the status is unknown instead. Both branches
// return before touching s.queriesSvc, so a zero-value service is enough.
func TestModelPlanCacheDistinguishesUnknownFromUnprepared(t *testing.T) {
	s := &InvestigationsService{}

	unknown := &investigations.PostgRESTRequest{PreparedStatement: nil}
	s.modelPlanCache(context.Background(), investigationDraft{Binds: []string{"1"}, PostgREST: unknown}, nil)
	if unknown.PlanCacheNote == nil || !strings.Contains(*unknown.PlanCacheNote, "unknown") {
		t.Fatalf("nil PreparedStatement must produce an 'unknown' note, got %v", unknown.PlanCacheNote)
	}
	if strings.Contains(*unknown.PlanCacheNote, "prepared statements off") {
		t.Fatalf("nil PreparedStatement must not be reported as a confident 'unprepared', got %q", *unknown.PlanCacheNote)
	}

	no := false
	unprepared := &investigations.PostgRESTRequest{PreparedStatement: &no}
	s.modelPlanCache(context.Background(), investigationDraft{Binds: []string{"1"}, PostgREST: unprepared}, nil)
	if unprepared.PlanCacheNote == nil || !strings.Contains(*unprepared.PlanCacheNote, "prepared statements off") {
		t.Fatalf("a known-false PreparedStatement must still get the definite 'unprepared' note, got %v", unprepared.PlanCacheNote)
	}
}
