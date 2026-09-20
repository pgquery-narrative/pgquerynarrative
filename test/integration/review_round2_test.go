package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
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
