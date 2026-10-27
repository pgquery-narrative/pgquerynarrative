package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/internal/config"
	"github.com/pgquerynarrative/pgquerynarrative/internal/db"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// pgrstAnalyzeLeakSetup gives one allowlisted, grantable role ("authenticated")
// a claims-keyed RLS policy over api.secrets, plus two RPCs that mirror a real
// PostgREST shape: STABLE SECURITY DEFINER (so PostgREST permits them over
// GET), with a body the validator cannot see inside, that mutates
// request.jwt.claims at SESSION scope (set_config's third argument, is_local,
// is false) — exactly what a real connection-pooling/impersonation helper
// function can do. api.whoami_then_fail does the same, then raises, to
// exercise the failure path.
const pgrstAnalyzeLeakSetup = `
CREATE ROLE web_anon NOLOGIN;
CREATE ROLE authenticated NOLOGIN;
CREATE ROLE authenticator LOGIN PASSWORD 'auth' NOINHERIT;
GRANT web_anon, authenticated TO authenticator;
CREATE ROLE pqn_reader LOGIN PASSWORD 'reader' NOINHERIT;
GRANT authenticated TO pqn_reader;
CREATE SCHEMA api;

CREATE TABLE api.secrets(id int PRIMARY KEY, owner text NOT NULL UNIQUE, secret text NOT NULL);
INSERT INTO api.secrets VALUES (1, 'victim', 'victims-private-data');
ALTER TABLE api.secrets ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_secret ON api.secrets FOR SELECT TO authenticated
  USING (owner = current_setting('request.jwt.claims', true)::json->>'sub');

CREATE FUNCTION api.whoami() RETURNS TABLE(me text)
LANGUAGE plpgsql STABLE SECURITY DEFINER AS $$
BEGIN
  PERFORM set_config('request.jwt.claims', '{"sub":"victim"}', false);
  RETURN QUERY SELECT 'ok'::text;
END;
$$;

CREATE FUNCTION api.whoami_then_fail() RETURNS TABLE(me text)
LANGUAGE plpgsql STABLE SECURITY DEFINER AS $$
BEGIN
  PERFORM set_config('request.jwt.claims', '{"sub":"victim"}', false);
  RAISE EXCEPTION 'synthetic failure after setting session state';
  RETURN QUERY SELECT 'unreachable'::text;
END;
$$;

GRANT USAGE ON SCHEMA api TO web_anon, authenticated, pqn_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO web_anon, authenticated, pqn_reader;
GRANT EXECUTE ON FUNCTION api.whoami() TO web_anon, authenticated, pqn_reader;
GRANT EXECUTE ON FUNCTION api.whoami_then_fail() TO web_anon, authenticated, pqn_reader;
ANALYZE;
`

// TestPostgRESTAnalyzeExecutionNeverLeavesSessionStateOnSharedConnection is a
// permanent regression test for a session-poisoning bug: EXPLAIN ANALYZE of a
// granted, allowed STABLE SECURITY DEFINER RPC actually executes its body,
// including any set_config(..., false) it runs at SESSION scope. The schema/
// function allowlist (queryrunner.Validator) governs which functions a query
// may call, but never inspects what a called function's body does, so that
// mutation is invisible to validation. If the physical connection it ran on
// were returned to the shared analytical pool afterward, the next caller to
// acquire it — replaying a different, genuinely clean request under the same
// or a different identity — would inherit that leftover request.jwt.claims,
// and own_secret's claims-keyed RLS policy would leak the first caller's row
// to it.
//
// MaxConns=1 forces every call below onto the exact same physical connection
// unless the fix's discardConn removes it from the pool — the condition the
// fix must guarantee regardless of how large a real deployment's pool is.
func TestPostgRESTAnalyzeExecutionNeverLeavesSessionStateOnSharedConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstAnalyzeLeakSetup, pgrstJWTSecret)
	admin, err := pgxpool.New(ctx, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	app, err := helpers.AppPoolFromAdmin(ctx, admin, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	readerCfg, err := pgxpool.ParseConfig(stack.ReaderConnStr + "&search_path=api")
	if err != nil {
		t.Fatal(err)
	}
	readerCfg.MaxConns = 1
	reader, err := pgxpool.NewWithConfig(ctx, readerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)

	runner := queryrunner.NewRunner(reader, queryrunner.NewValidator([]string{"api"}, 10000), 5000, 30*time.Second, queryrunner.WithExplainAnalyze(true))
	appDB := db.NewOrgScoped(app)
	qs := service.NewQueriesService(app, appDB, runner, config.MetricsConfig{})
	rs := service.NewReportsService(app, appDB, runner, noopLLM{}, config.MetricsConfig{})
	svc := service.NewInvestigationsService(appDB, qs, rs)
	svc.SetPostgRESTReplayRoles([]string{"authenticated"})
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "analyze-leak", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	authenticatedToken := pgrstJWT(t, map[string]any{"role": "authenticated"})

	// A second, genuinely clean capture of api.secrets, replayed as the same
	// granted role with no claims of its own (claims are never replayed —
	// see ReplayContext). If the connection carries no leftover session
	// state, own_secret's RLS policy must see none and return exactly 0 rows
	// regardless of what ran on this connection before.
	assertSecretsStillClean := func(t *testing.T, label string) {
		t.Helper()
		_, _, secretsLog := pgrstCall(t, ctx, stack, "/secrets?select=id,owner", authenticatedToken, endsTransaction)
		inv, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: secretsLog, Analyze: true})
		if err != nil {
			t.Fatalf("%s: import secrets capture: %v", label, err)
		}
		if inv.PostgrestRequest.ReplayRole == nil || *inv.PostgrestRequest.ReplayRole != "authenticated" {
			t.Fatalf("%s: secrets capture was not replayed as the granted role: %+v", label, inv.PostgrestRequest)
		}
		rows, ok := scanActualRows(inv.Explain.Plan, "secrets")
		if !ok || rows != 0 {
			t.Fatalf("%s: a prior analyzed execution leaked session state onto the reused connection: replay as authenticated with no claims of its own saw %v row(s) of victim's data, want exactly 0", label, rows)
		}
	}

	t.Run("success path: analyzed RPC that sets claims must not poison the next request", func(t *testing.T) {
		_, _, whoamiLog := pgrstCall(t, ctx, stack, "/rpc/whoami", authenticatedToken, endsTransaction)
		poison, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: whoamiLog, Analyze: true})
		if err != nil {
			t.Fatalf("import RPC capture: %v", err)
		}
		if poison.PostgrestRequest.ReplayRole == nil || *poison.PostgrestRequest.ReplayRole != "authenticated" {
			t.Fatalf("RPC capture was not replayed as the granted role: %+v", poison.PostgrestRequest)
		}
		if poison.Explain.EvidenceMode != "observed" {
			t.Fatalf("the RPC must actually have been executed (EXPLAIN ANALYZE), got evidence mode %q", poison.Explain.EvidenceMode)
		}
		assertSecretsStillClean(t, "after successful analyzed RPC")
	})

	t.Run("failure path: an analyzed execution that errors must still discard its connection", func(t *testing.T) {
		_, _, failLog := pgrstCall(t, ctx, stack, "/rpc/whoami_then_fail", authenticatedToken, endsTransaction)
		failed, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: failLog, Analyze: true})
		if err != nil {
			t.Fatalf("import of a failing RPC capture must still succeed by degrading to an estimate-only plan: %v", err)
		}
		if failed.Explain.EvidenceMode != "estimated" {
			t.Fatalf("a failing EXPLAIN ANALYZE must fall back to an estimate-only plan, got evidence mode %q", failed.Explain.EvidenceMode)
		}
		assertSecretsStillClean(t, "after a failed analyzed RPC")
	})

	t.Log("EXPLAIN ANALYZE of a STABLE SECURITY DEFINER RPC that sets session-level claims, on success or on failure, never poisoned the next request's connection")
}

// TestPostgRESTAddCandidateVerifyResultsNeverLeavesSessionStateOnSharedConnection
// is the same regression as the Analyze=true case above, for a different
// trigger: AddCandidate's VerifyResults flag runs result-equivalence
// checking (compareResultEquivalence), which executes both the
// investigation's own SQL and the candidate SQL for real via
// Runner.RunSnapshot — independent of the Analyze flag entirely. Analyze is
// left false throughout this test so the already-covered EXPLAIN ANALYZE
// path cannot be the one doing the work.
func TestPostgRESTAddCandidateVerifyResultsNeverLeavesSessionStateOnSharedConnection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstAnalyzeLeakSetup, pgrstJWTSecret)
	admin, err := pgxpool.New(ctx, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	app, err := helpers.AppPoolFromAdmin(ctx, admin, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)

	readerCfg, err := pgxpool.ParseConfig(stack.ReaderConnStr + "&search_path=api")
	if err != nil {
		t.Fatal(err)
	}
	readerCfg.MaxConns = 1
	reader, err := pgxpool.NewWithConfig(ctx, readerCfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)

	runner := queryrunner.NewRunner(reader, queryrunner.NewValidator([]string{"api"}, 10000), 5000, 30*time.Second)
	appDB := db.NewOrgScoped(app)
	qs := service.NewQueriesService(app, appDB, runner, config.MetricsConfig{})
	rs := service.NewReportsService(app, appDB, runner, noopLLM{}, config.MetricsConfig{})
	svc := service.NewInvestigationsService(appDB, qs, rs)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "addcand-leak", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	// own_secret's policy is "TO authenticated", and the connecting role
	// (pqn_reader) is NOINHERIT, so it must impersonate "authenticated" (SET
	// LOCAL ROLE, same mechanism a real PostgREST-imported investigation
	// uses) for the policy to apply at all — otherwise every read below
	// would see zero rows regardless of whether a leak occurred, and the
	// test would pass for the wrong reason. No claims are ever part of this
	// ReplayContext, matching production: an investigation replayed under
	// an impersonated role still carries none of its own.
	replayCtx := queryrunner.WithReplayContext(reqCtx, &queryrunner.ReplayContext{Role: "authenticated"})

	// A benign investigation whose own SQL never calls the RPC, so any
	// poisoning below can only have come from the candidate comparison.
	inv, err := svc.Create(replayCtx, &investigations.CreateInvestigationPayload{
		Title: "add-candidate leak repro",
		SQL:   "SELECT id, owner FROM api.secrets WHERE owner = 'nobody'",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// VerifyResults:true, Analyze:false: compareResultEquivalence runs both
	// SQL texts for real via RunSnapshot regardless. The candidate here
	// calls the granted RPC, whose body sets request.jwt.claims at session
	// scope (is_local=false).
	if _, err := svc.AddCandidate(replayCtx, &investigations.AddCandidatePayload{
		ID:            inv.ID,
		CandidateSQL:  "SELECT me FROM api.whoami()",
		VerifyResults: true,
	}); err != nil {
		t.Fatalf("AddCandidate (poisoning call): %v", err)
	}

	// A second, unrelated investigation, replayed under the same
	// impersonated role but carrying no claims of its own — exactly a
	// genuinely clean request after the poisoning one above. If the
	// connection RunSnapshot used to verify the candidate leaked session
	// state, this read would inherit the leaked identity's row.
	clean, err := runner.Run(replayCtx, "SELECT id, owner FROM api.secrets", 10)
	if err != nil {
		t.Fatalf("clean Run after AddCandidate: %v", err)
	}
	if clean.RowCount != 0 {
		t.Fatalf("AddCandidate's VerifyResults flow leaked session state onto the reused connection: clean read as the impersonated role with no claims of its own saw %d row(s) of victim's data, want 0", clean.RowCount)
	}
}
