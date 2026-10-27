package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// qrIsolationSetup mirrors pgrstAnalyzeLeakSetup's shape (see
// postgrest_analyze_session_leak_test.go) but needs no PostgREST stack:
// Runner.Run and Runner.RunSnapshot are plain queryrunner-level execution,
// never involving PostgREST at all. One role ("app_role") is granted a
// STABLE SECURITY DEFINER RPC that sets request.jwt.claims at SESSION scope
// (set_config's third argument, is_local, is false), and a claims-keyed RLS
// policy over api.secrets — the same real shape a PostgREST-backed schema
// uses, exercised here directly through the query runner instead.
const qrIsolationSetup = `
CREATE ROLE app_role LOGIN PASSWORD 'app' NOINHERIT;
CREATE SCHEMA api;

CREATE TABLE api.secrets(id int PRIMARY KEY, owner text NOT NULL UNIQUE, secret text NOT NULL);
INSERT INTO api.secrets VALUES (1, 'victim', 'victims-private-data');
ALTER TABLE api.secrets ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_secret ON api.secrets FOR SELECT TO app_role
  USING (owner = current_setting('request.jwt.claims', true)::json->>'sub');

CREATE FUNCTION api.whoami() RETURNS TABLE(me text)
LANGUAGE plpgsql STABLE SECURITY DEFINER AS $$
BEGIN
  PERFORM set_config('request.jwt.claims', '{"sub":"victim"}', false);
  RETURN QUERY SELECT 'ok'::text;
END;
$$;

GRANT USAGE ON SCHEMA api TO app_role;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO app_role;
GRANT EXECUTE ON FUNCTION api.whoami() TO app_role;
`

// qrIsolationPool starts a disposable PostgreSQL, applies qrIsolationSetup,
// and returns a MaxConns=1 pool connected as app_role — forcing every
// acquisition in a test below onto the exact same physical connection
// unless the fix in question actually discards it.
func qrIsolationPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	adminConnStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, adminConnStr)

	admin, err := pgxpool.New(ctx, adminConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	if _, err := admin.Exec(ctx, qrIsolationSetup); err != nil {
		t.Fatalf("setup: %v", err)
	}

	// Reuse the admin connection string's host/port/dbname/sslmode, swapping
	// only the credentials, rather than reconstructing a URL by hand (the
	// container's database name is not exported by the testcontainers
	// postgres module).
	cfg, err := pgxpool.ParseConfig(adminConnStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User = "app_role"
	cfg.ConnConfig.Password = "app"
	cfg.ConnConfig.RuntimeParams["search_path"] = "api"
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestRunNeverLeavesSessionStateOnSharedConnection is a permanent regression
// test for Runner.Run: ordinary (non-EXPLAIN) execution of a query that
// calls a granted STABLE SECURITY DEFINER function which sets
// request.jwt.claims at session scope must not leave that claim on the
// connection for the next, unrelated Run call to inherit.
func TestRunNeverLeavesSessionStateOnSharedConnection(t *testing.T) {
	ctx := context.Background()
	pool := qrIsolationPool(t, ctx)
	validator := queryrunner.NewValidator([]string{"api"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 100, 10*time.Second)

	if _, err := runner.Run(ctx, "SELECT me FROM api.whoami()", 10); err != nil {
		t.Fatalf("Run (poisoning call): %v", err)
	}

	clean, err := runner.Run(ctx, "SELECT id, owner FROM api.secrets", 10)
	if err != nil {
		t.Fatalf("Run (clean call): %v", err)
	}
	if clean.RowCount != 0 {
		t.Fatalf("a prior Run call leaked session state onto the reused connection: clean Run as app_role with no claims of its own saw %d row(s) of victim's data, want 0", clean.RowCount)
	}
}

// TestRunSnapshotNeverLeavesSessionStateOnSharedConnection is the same
// regression for Runner.RunSnapshot, which backs result-equivalence
// checking (AddCandidate's VerifyResults flow) — see
// TestPostgRESTAddCandidateVerifyResultsNeverLeavesSessionStateOnSharedConnection
// for that flow exercised through the full service layer.
func TestRunSnapshotNeverLeavesSessionStateOnSharedConnection(t *testing.T) {
	ctx := context.Background()
	pool := qrIsolationPool(t, ctx)
	validator := queryrunner.NewValidator([]string{"api"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 100, 10*time.Second)

	poison := runner.RunSnapshot(ctx, []queryrunner.SnapshotQuery{{SQL: "SELECT me FROM api.whoami()", Limit: 10}})
	if len(poison) != 1 || poison[0].Err != nil {
		t.Fatalf("RunSnapshot (poisoning call): %+v", poison)
	}

	clean := runner.RunSnapshot(ctx, []queryrunner.SnapshotQuery{{SQL: "SELECT id, owner FROM api.secrets", Limit: 10}})
	if len(clean) != 1 || clean[0].Err != nil {
		t.Fatalf("RunSnapshot (clean call): %+v", clean)
	}
	if clean[0].Result.RowCount != 0 {
		t.Fatalf("a prior RunSnapshot call leaked session state onto the reused connection: clean RunSnapshot as app_role with no claims of its own saw %d row(s) of victim's data, want 0", clean[0].Result.RowCount)
	}
}
