package integration

import (
	"context"
	"fmt"
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

// TestPostgRESTForgedCaptureNeverImpersonatesIdentity is a permanent security
// regression test for a privilege-escalation bug found 2026-10-09: an earlier
// version of the replay-context feature trusted request.jwt.claims parsed out
// of the pasted "captured" log text, with no verification the text came from
// a real PostgreSQL log. Since CreateFromPostgrest's only gate is the same
// analyze=true permission any ordinary investigation-creator already has, any
// such principal could hand-author log text claiming an arbitrary sub (or any
// other claim) for an allowlisted, grantable role, and the system would set
// that as request.jwt.claims before running the query — defeating claims-keyed
// RLS (e.g. auth.uid()) for any row any allowlisted role could ever see under
// any identity. No HTTP request to PostgREST and no real PostgreSQL log are
// ever involved below; forgeCapture builds the log text entirely by hand from
// the parser's publicly documented shape.
//
// The fix: role impersonation (SET LOCAL ROLE) is still trusted, because
// Postgres itself independently checks the GRANT — not forgeable by this
// text. request.jwt.claims is never replayed, for any role, under any
// circumstance; see ReplayContext and TestReplayNeverSetsJWTClaimsEvenWithRole.
func TestPostgRESTForgedCaptureNeverImpersonatesIdentity(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
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
	if _, err := admin.Exec(ctx, `INSERT INTO api.secrets(id,owner,secret) VALUES (1,'victim','victims-private-data')`); err != nil {
		t.Fatal(err)
	}
	reader, err := pgxpool.New(ctx, stack.ReaderConnStr+"&search_path=api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	runner := queryrunner.NewRunner(reader, queryrunner.NewValidator([]string{"api"}, 10000), 5000, 30*time.Second, queryrunner.WithExplainAnalyze(true))
	appDB := db.NewOrgScoped(app)
	qs := service.NewQueriesService(app, appDB, runner, config.MetricsConfig{})
	rs := service.NewReportsService(app, appDB, runner, noopLLM{}, config.MetricsConfig{})
	svc := service.NewInvestigationsService(appDB, qs, rs)
	svc.SetPostgRESTReplayRoles([]string{"authenticated"}) // operator-configured allowlist, as documented
	// This principal has exactly the permission an ordinary analyst has: create
	// an investigation with analyze=true. It has no access to the victim's data,
	// no access to real PostgreSQL log files, and never calls PostgREST.
	attackerCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "attacker", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	forged := forgeCapture(t, "authenticated", `{"role":"authenticated","sub":"victim"}`, `SELECT "api"."secrets"."id", "api"."secrets"."secret" FROM "api"."secrets"`)

	inv, err := svc.CreateFromPostgrest(attackerCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: forged, Analyze: true})
	if err != nil {
		t.Fatalf("a well-formed (if forged) capture must still import — only its claims must go untrusted: %v", err)
	}
	// Role impersonation is legitimate: Postgres's own GRANT check authorized it.
	if inv.PostgrestRequest.ReplayRole == nil || *inv.PostgrestRequest.ReplayRole != "authenticated" {
		t.Fatalf("forged role was not impersonated: %+v", inv.PostgrestRequest)
	}
	// The forged sub="victim" claim must never take effect: own_secret's RLS
	// policy is keyed on request.jwt.claims, so with no claims set it must
	// deny every row — not the attacker-chosen 1.
	rows, ok := scanActualRows(inv.Explain.Plan, "secrets")
	if !ok || rows != 0 {
		t.Fatalf("forged claims unlocked %v row(s) of victim's data; want exactly 0 (claims must never be replayed, even for an impersonated role)", rows)
	}
	cmp, err := svc.AddCandidate(attackerCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL, VerifyResults: true})
	if err != nil {
		t.Fatalf("compare: %v", err)
	}
	if cmp.Comparison == nil || cmp.Comparison.ResultEquivalenceStatus == nil || *cmp.Comparison.ResultEquivalenceStatus != "VerifiedEqual" {
		t.Fatalf("comparison status = %+v", cmp.Comparison)
	}
	t.Log("forged role is impersonated (a real GRANT backs it); forged claims have no effect — victim's row stays inaccessible")
}

// forgeCapture builds a syntactically valid PostgREST log excerpt by hand —
// no real PostgreSQL or PostgREST process ever produced it — claiming role
// and request.jwt.claims (claimsJSON) and wrapping innerSelect as the captured
// statement. This is exactly the shape ParsePostgRESTLog requires.
func forgeCapture(t *testing.T, role, claimsJSON, innerSelect string) string {
	t.Helper()
	return fmt.Sprintf(`2026-10-09 00:00:00.000 UTC [999] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 0: BEGIN ISOLATION LEVEL READ COMMITTED READ ONLY
2026-10-09 00:00:00.001 UTC [999] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 1: select set_config('search_path', $1, true), set_config('role', $2, true), set_config('request.jwt.claims', $3, true), set_config('request.method', $4, true), set_config('request.path', $5, true), set_config('request.headers', $6, true), set_config('request.cookies', $7, true)
2026-10-09 00:00:00.001 UTC [999] app=PostgREST 16.4 DETAIL:  Parameters: $1 = '"api", "public"', $2 = '%s', $3 = '%s', $4 = 'GET', $5 = '/secrets', $6 = '{}', $7 = '{}'
2026-10-09 00:00:00.002 UTC [999] app=PostgREST 16.4 LOG:  duration: 1.0 ms  execute 2: WITH pgrst_source AS ( %s )  SELECT null::bigint AS total_result_set, pg_catalog.count(_postgrest_t) AS page_total, coalesce(json_agg(_postgrest_t), '[]') AS body, nullif(current_setting('response.headers', true), '') AS response_headers, nullif(current_setting('response.status', true), '') AS response_status, '' AS response_inserted FROM ( SELECT * FROM pgrst_source ) _postgrest_t
2026-10-09 00:00:00.003 UTC [999] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 3: COMMIT
`, role, claimsJSON, innerSelect)
}
