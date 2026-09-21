package integration

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/httpmw"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// rlsExecCommit is rlsExec that keeps its change, so a test can see what a statement really did.
func rlsExecCommit(ctx context.Context, pool *pgxpool.Pool, settings map[string]string, stmt string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for k, v := range settings {
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, k, v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, stmt); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// The login lookups (a user's own memberships, the mappings for a token's groups) are read-only
// exceptions. A DELETE or UPDATE that reaches a row only through an exception must change nothing.
func TestLoginLookupExceptionsCannotDeleteOrUpdate(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	appPool, err := testhelpers.AppPoolFromAdmin(ctx, admin, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)
	for _, stmt := range []string{
		`INSERT INTO app.organizations (id, name, slug) VALUES ('` + rlsOrgB + `', 'Org B', 'org-b') ON CONFLICT DO NOTHING`,
		`DELETE FROM app.organization_members WHERE user_id = 'victim'`,
		`INSERT INTO app.organization_members (organization_id, user_id, role) VALUES
			('` + rlsOrgA + `', 'victim', 'viewer'), ('` + rlsOrgB + `', 'victim', 'analyst')`,
		`INSERT INTO app.oidc_group_org_mappings (group_claim, organization_id, role) VALUES
			('grp-victim', '` + rlsOrgA + `', 'viewer'), ('grp-victim', '` + rlsOrgB + `', 'analyst') ON CONFLICT DO NOTHING`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}

	memberships := `SELECT count(*) FROM app.organization_members WHERE user_id = 'victim'`
	mappings := `SELECT count(*) FROM app.oidc_group_org_mappings WHERE group_claim = 'grp-victim'`
	for _, tc := range []struct {
		name, stmt, count string
		settings          map[string]string
	}{
		{"delete memberships from another org", `DELETE FROM app.organization_members WHERE user_id = 'victim'`, memberships, map[string]string{"app.membership_user_id": "victim"}},
		{"delete mappings through the lookup", `DELETE FROM app.oidc_group_org_mappings WHERE group_claim = 'grp-victim'`, mappings, map[string]string{"app.oidc_groups": "grp-victim"}},
	} {
		var before int
		if err := admin.QueryRow(ctx, tc.count).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if before == 0 {
			t.Fatalf("%s: nothing seeded to test against", tc.name)
		}
		_ = rlsExecCommit(ctx, appPool, tc.settings, tc.stmt)
		var after int
		if err := admin.QueryRow(ctx, tc.count).Scan(&after); err != nil {
			t.Fatal(err)
		}
		if after != before {
			t.Errorf("%s: rows went from %d to %d", tc.name, before, after)
		}
	}

	for _, tc := range []struct {
		name, stmt, promoted string
		settings             map[string]string
	}{
		{"update a membership through the lookup", `UPDATE app.organization_members SET role = 'tenant_admin' WHERE user_id = 'victim'`,
			`SELECT count(*) FROM app.organization_members WHERE user_id = 'victim' AND role = 'tenant_admin'`, map[string]string{"app.membership_user_id": "victim"}},
		{"update mappings through the lookup", `UPDATE app.oidc_group_org_mappings SET role = 'tenant_admin' WHERE group_claim = 'grp-victim'`,
			`SELECT count(*) FROM app.oidc_group_org_mappings WHERE group_claim = 'grp-victim' AND role = 'tenant_admin'`, map[string]string{"app.oidc_groups": "grp-victim"}},
	} {
		_ = rlsExecCommit(ctx, appPool, tc.settings, tc.stmt)
		var n int
		if err := admin.QueryRow(ctx, tc.promoted).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Errorf("%s: %d rows were rewritten", tc.name, n)
		}
	}

	// Inside its own organization a session can still delete its own organization's row, and only that.
	if err := rlsExecCommit(ctx, appPool, map[string]string{"app.current_org_id": rlsOrgA, "app.membership_user_id": "victim"},
		`DELETE FROM app.organization_members WHERE user_id = 'victim'`); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.organization_members WHERE user_id = 'victim' AND organization_id = '`+rlsOrgB+`'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 1 {
		t.Errorf("deleting inside org A removed org B's membership (left %d, want 1)", left)
	}
}

// A worker resolves a claimed schedule's owner before any request exists. Under row-level security
// that lookup needs the claimed organization, or the owner "is no longer a member" and the schedule
// is disabled.
func TestScheduleRunnerResolvesItsOwnerUnderRowLevelSecurity(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	appPool, err := testhelpers.AppPoolFromAdmin(ctx, admin, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)

	var scheduleID string
	if _, err := admin.Exec(ctx, `INSERT INTO app.organization_members (organization_id, user_id, role)
		VALUES ($1, 'sched-owner', 'analyst') ON CONFLICT DO NOTHING`, rlsOrgA); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `
		INSERT INTO app.schedules (name, sql, connection_id, interval_expr, destination_type, destination_target,
			enabled, next_run_at, organization_id, created_by)
		VALUES ('owner-lookup', 'SELECT 1', 'default', '@every 5m', 'log', '', true, NOW() - INTERVAL '1 minute', $1::uuid, 'sched-owner')
		RETURNING id`, rlsOrgA).Scan(&scheduleID); err != nil {
		t.Fatal(err)
	}

	svc := service.NewSchedulesService(db.NewOrgScoped(appPool), nil, nil)
	svc.SetRawPool(appPool)
	if err := svc.RunDue(ctx, "worker-rls"); err != nil {
		t.Fatal(err)
	}

	var enabled bool
	var code *string
	if err := admin.QueryRow(ctx, `
		SELECT s.enabled, (SELECT r.failure_code FROM app.schedule_runs r WHERE r.schedule_id = s.id ORDER BY r.started_at DESC LIMIT 1)
		FROM app.schedules s WHERE s.id = $1`, scheduleID).Scan(&enabled, &code); err != nil {
		t.Fatal(err)
	}
	// With no reports service the run stops right after the owner is resolved, with "misconfigured".
	if !enabled || code == nil || *code != "misconfigured" {
		got := "<none>"
		if code != nil {
			got = *code
		}
		t.Errorf("schedule enabled=%v, failure_code=%s: want the owner resolved (enabled, misconfigured)", enabled, got)
	}
}

// Whether statement statistics are shared is a fact about the connection (no dedicated credentials
// for the organization), not about what the role is called.
func TestStatStatementsRefusedOnASharedRoleWhateverItIsCalled(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	appPool, err := testhelpers.AppPoolFromAdmin(ctx, admin, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)
	for _, stmt := range []string{
		`INSERT INTO app.organizations (id, name, slug) VALUES ('` + rlsOrgB + `', 'Org B', 'org-b') ON CONFLICT DO NOTHING`,
		`DO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'reporting_ro') THEN CREATE ROLE reporting_ro LOGIN PASSWORD 'reporting_ro'; END IF; END $$`,
		`GRANT pg_monitor TO reporting_ro`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = "reporting_ro", "reporting_ro"
	roPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(roPool.Close)

	runner := queryrunner.NewRunner(roPool, queryrunner.NewValidator(nil, 1000), 100, 10*time.Second)
	// The library constructor does not know the configured role's name, and "reporting_ro" is not the default.
	svc := service.NewQueriesService(roPool, db.NewOrgScoped(appPool), runner, config.MetricsConfig{})
	svc.SetStatStatementsEnabled(true)

	tenant := auth.WithPrincipal(ctx, auth.Principal{UserID: "u", OrgID: rlsOrgA, Role: auth.RoleViewer})
	_, err = svc.StatStatements(tenant, &queries.StatStatementsPayload{})
	var ve *queries.ValidationError
	if !errors.As(err, &ve) || ve.Code == nil || *ve.Code != "STAT_STATEMENTS_SHARED" {
		t.Errorf("a tenant read another organization's statement statistics on a shared role: err = %v", err)
	}
}

// A role that may read a large object (SELECT on it, or lo_compat_privileges) could read its bytes through
// lo_get, which the deny-list did not name. The read runs on the analytical role with that privilege held.
func TestLargeObjectReadsAreDeniedEvenWhenThePrivilegeIsHeld(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	if err := testhelpers.EnsurePostgresRoles(ctx, admin); err != nil {
		t.Fatal(err)
	}
	var oid uint32
	if err := admin.QueryRow(ctx, `SELECT lo_from_bytea(0, 'top secret'::bytea)`).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, fmt.Sprintf(`GRANT SELECT ON LARGE OBJECT %d TO pgquerynarrative_readonly`, oid)); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.User, cfg.ConnConfig.Password = "pgquerynarrative_readonly", "pgquerynarrative_readonly"
	roPool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(roPool.Close)

	// The privilege really is held: the raw read works on this pool.
	var raw string
	if err := roPool.QueryRow(ctx, fmt.Sprintf(`SELECT convert_from(lo_get(%d), 'utf8')`, oid)).Scan(&raw); err != nil || raw != "top secret" {
		t.Fatalf("precondition: the read-only role should be able to read the large object: %q %v", raw, err)
	}
	runner := queryrunner.NewRunner(roPool, queryrunner.NewValidator([]string{"demo"}, 1000), 100, 10*time.Second)
	if _, err := runner.Run(ctx, fmt.Sprintf(`SELECT convert_from(lo_get(%d), 'utf8') AS body`, oid), 10); err == nil {
		t.Error("the runner returned a large object's bytes")
	}
}

// AddCandidate accepted only one EXPLAIN ANALYZE run: timing_runs was on the plan comparison but not on
// the candidate call, so a candidate's speedup always rested on a single sample.
func TestAddCandidateForwardsTimingRuns(t *testing.T) {
	admin, _, ctx := multiOrgPostgres(t)
	if _, err := admin.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-01-20', INTERVAL '1 day') AS d`); err != nil {
		t.Fatal(err)
	}
	runner := queryrunner.NewRunner(admin, queryrunner.NewValidator([]string{"demo"}, 10000), 5000, 30*time.Second,
		queryrunner.WithExplainAnalyze(true))
	appDB := db.NewOrgScoped(admin)
	queriesSvc := service.NewQueriesService(admin, appDB, runner, config.MetricsConfig{})
	reportsSvc := service.NewReportsService(admin, appDB, runner, nil, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "timing", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	inv, err := invSvc.Create(reqCtx, &investigations.CreateInvestigationPayload{
		Title: "timing runs", SQL: `SELECT region, count(*) FROM demo.sales WHERE region = 'North' GROUP BY region`})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := invSvc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{
		ID: inv.ID, CandidateSQL: `SELECT region, count(*) FROM demo.sales WHERE region = 'North' GROUP BY 1`,
		Analyze: true, TimingRuns: 3}); err != nil {
		t.Fatal(err)
	}
	var comparison string
	if err := admin.QueryRow(ctx, `SELECT comparison::text FROM app.investigation_candidates WHERE investigation_id = $1`, inv.ID).Scan(&comparison); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(comparison, "3 runs") {
		t.Errorf("the stored comparison does not show three timing runs: %.300s", comparison)
	}
}

// Managed (database-stored) API keys were invisible to the rate limiter's DB-free identity check, so
// every one fell into its client's IP bucket: keys behind one NAT shared a budget, and one key could
// spend another's. Once a key has authenticated it is keyed by organization and key id; a token that
// never authenticated stays in the IP bucket, so guessing tokens cannot shed the IP limit.
func TestManagedKeysGetTheirOwnRateLimitBucket(t *testing.T) {
	pool, ctx := setupMigratedPool(t)
	org := auth.DefaultOrgID()
	store := auth.NewManagedKeyStore(pool)
	a, err := store.Create(ctx, org, auth.RoleAnalyst, "admin", []string{"read"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	b, err := store.Create(ctx, org, auth.RoleAnalyst, "admin", []string{"read"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	authn := auth.NewAuthenticator(true, "", "", "", nil)
	authn.SetManagedKeyStore(store)

	req := func(token string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/api/v1/queries", nil)
		r.Header.Set("Authorization", "Bearer "+token)
		r.RemoteAddr = "203.0.113.7:4000" // the same NAT for every caller
		return r
	}
	key := func(token string) string { k, _ := httpmw.RateLimitKey(req(token), nil, authn, nil); return k }

	// Authentication is what teaches the limiter a key is real.
	for _, k := range []*auth.IssuedKey{a, b} {
		if _, ok := authn.ValidatePrincipal(req(k.Secret)); !ok {
			t.Fatal("managed key did not authenticate")
		}
	}
	ka, kb := key(a.Secret), key(b.Secret)
	if strings.HasPrefix(ka, "ip:") || strings.HasPrefix(kb, "ip:") {
		t.Errorf("authenticated managed keys are still in the IP bucket: %q %q", ka, kb)
	}
	if ka == kb {
		t.Errorf("two keys share one bucket: %q", ka)
	}
	if !strings.Contains(ka, a.ID) {
		t.Errorf("the bucket does not name the key: %q", ka)
	}
	if k := key("pgqn_" + strings.Repeat("x", 43)); !strings.HasPrefix(k, "ip:") {
		t.Errorf("a token that never authenticated must stay in the IP bucket, got %q", k)
	}
}
