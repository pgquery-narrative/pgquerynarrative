package integration

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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

const raceWorkers = 40

// pgrstRaceSetup gives two allowlisted, grantable roles their own role-keyed
// RLS policy (not claims-keyed — a replay never carries claims, so a
// concurrency test for the replay mechanism itself must isolate by role) over
// one shared table, plus a single-row-per-owner table used by the forged-
// capture regression test in postgrest_impersonation_test.go.
const pgrstRaceSetup = `
CREATE ROLE web_anon NOLOGIN;
CREATE ROLE authenticated NOLOGIN;
CREATE ROLE tenant_a NOLOGIN;
CREATE ROLE tenant_b NOLOGIN;
CREATE ROLE authenticator LOGIN PASSWORD 'auth' NOINHERIT;
GRANT web_anon, authenticated, tenant_a, tenant_b TO authenticator;
CREATE ROLE pqn_reader LOGIN PASSWORD 'reader' NOINHERIT;
GRANT authenticated, tenant_a, tenant_b TO pqn_reader;
CREATE SCHEMA api;

CREATE TABLE api.secrets(id int PRIMARY KEY, owner text NOT NULL UNIQUE, secret text NOT NULL);
ALTER TABLE api.secrets ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_secret ON api.secrets FOR SELECT TO authenticated
  USING (owner = current_setting('request.jwt.claims', true)::json->>'sub');

CREATE TABLE api.tenant_rows(id int PRIMARY KEY, tenant text NOT NULL);
INSERT INTO api.tenant_rows SELECT g, CASE WHEN g % 2 = 0 THEN 'a' ELSE 'b' END FROM generate_series(1, 1000) g;
ALTER TABLE api.tenant_rows ENABLE ROW LEVEL SECURITY;
CREATE POLICY tenant_a_scope ON api.tenant_rows FOR SELECT TO tenant_a USING (tenant = 'a');
CREATE POLICY tenant_b_scope ON api.tenant_rows FOR SELECT TO tenant_b USING (tenant = 'b');

GRANT USAGE ON SCHEMA api TO web_anon, authenticated, tenant_a, tenant_b, pqn_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO web_anon, authenticated, tenant_a, tenant_b, pqn_reader;
ANALYZE;
`

// TestPostgRESTConcurrentReplayNeverLeaksAcrossGoroutines fires many concurrent
// CreateFromPostgrest + AddCandidate + RankCandidates calls on ONE shared
// InvestigationsService (one shared Runner, one shared pool) — the way a real
// server handles concurrent HTTP requests — each replayed as one of two real,
// role-keyed-RLS roles. If the replay context carried by context.Value ever
// leaked or got swapped between goroutines sharing the Runner, a worker would
// see the other tenant's 500 rows instead of its own, or a
// validation/compare mismatch. Run with -race.
func TestPostgRESTConcurrentReplayNeverLeaksAcrossGoroutines(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstRaceSetup, pgrstJWTSecret)
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

	reader, err := pgxpool.New(ctx, stack.ReaderConnStr+"&search_path=api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	cfg := reader.Config()
	cfg.MaxConns = 25
	reader, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)

	runner := queryrunner.NewRunner(reader, queryrunner.NewValidator([]string{"api"}, 10000), 5000, 30*time.Second, queryrunner.WithExplainAnalyze(true))
	appDB := db.NewOrgScoped(app)
	qs := service.NewQueriesService(app, appDB, runner, config.MetricsConfig{})
	rs := service.NewReportsService(app, appDB, runner, noopLLM{}, config.MetricsConfig{})
	svc := service.NewInvestigationsService(appDB, qs, rs)
	svc.SetPostgRESTReplayRoles([]string{"tenant_a", "tenant_b"})
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "race", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	// Capture one real request per tenant up front (sequentially, so each
	// capture's log excerpt is unambiguous), then hit the shared service
	// concurrently, alternating tenants across workers.
	captures := map[string]string{}
	for _, tenant := range []string{"tenant_a", "tenant_b"} {
		before, err := stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		token := pgrstJWT(t, map[string]any{"role": tenant})
		url := fmt.Sprintf("%s/tenant_rows?select=id,tenant&order=id", stack.BaseURL)
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("capture request for %s: http %d", tenant, resp.StatusCode)
		}
		var excerpt string
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
			logs, err := stack.PostgreSQLLogs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if excerpt = logs[len(before):]; strings.Contains(excerpt, "COMMIT") {
				if _, perr := queryrunner.ParsePostgRESTLog(excerpt); perr == nil {
					break
				}
			}
			if time.Now().After(deadline) {
				t.Fatalf("capture for %s not logged in time", tenant)
			}
		}
		captures[tenant] = excerpt
	}

	var wg sync.WaitGroup
	var leaks, errs int64
	for i := 0; i < raceWorkers; i++ {
		tenant := "tenant_a"
		if i%2 == 1 {
			tenant = "tenant_b"
		}
		wg.Add(1)
		go func(tenant string, n int) {
			defer wg.Done()
			inv, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: captures[tenant], Analyze: true})
			if err != nil {
				t.Errorf("worker %d (%s): create: %v", n, tenant, err)
				atomic.AddInt64(&errs, 1)
				return
			}
			if inv.PostgrestRequest.ReplayRole == nil || *inv.PostgrestRequest.ReplayRole != tenant {
				t.Errorf("worker %d: replay role = %v, want %s", n, inv.PostgrestRequest.ReplayRole, tenant)
				atomic.AddInt64(&leaks, 1)
				return
			}
			rows, ok := scanActualRows(inv.Explain.Plan, "tenant_rows")
			if !ok || rows != 500 {
				t.Errorf("worker %d (%s): plan scanned %v rows, want exactly 500 (its own tenant's half — a leak would show the other tenant's 500 or all 1000)", n, tenant, rows)
				atomic.AddInt64(&leaks, 1)
				return
			}
			cmp, err := svc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL, VerifyResults: true, Analyze: true})
			if err != nil {
				t.Errorf("worker %d (%s): compare: %v", n, tenant, err)
				atomic.AddInt64(&errs, 1)
				return
			}
			if cmp.Comparison == nil || cmp.Comparison.ResultEquivalenceStatus == nil || *cmp.Comparison.ResultEquivalenceStatus != "VerifiedEqual" {
				var s string
				if cmp.Comparison != nil && cmp.Comparison.ResultEquivalenceStatus != nil {
					s = *cmp.Comparison.ResultEquivalenceStatus
				}
				t.Errorf("worker %d (%s): self-compare = %q, want VerifiedEqual (both executions must share one replay context)", n, tenant, s)
				atomic.AddInt64(&leaks, 1)
				return
			}
			if _, err := svc.RankCandidates(reqCtx, &investigations.RankCandidatesPayload{ID: inv.ID}); err != nil {
				t.Errorf("worker %d (%s): rank: %v", n, tenant, err)
				atomic.AddInt64(&errs, 1)
			}
		}(tenant, i)
	}
	wg.Wait()
	if leaks > 0 {
		t.Fatalf("%d/%d workers saw cross-request contamination", leaks, raceWorkers)
	}
	if errs > 0 {
		t.Fatalf("%d/%d workers errored", errs, raceWorkers)
	}
	t.Logf("%d concurrent workers, 2 role-keyed-RLS replay roles, 0 leaks, 0 errors", raceWorkers)
}
