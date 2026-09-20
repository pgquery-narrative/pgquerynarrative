package integration

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

const (
	rlsOrgA = "00000000-0000-0000-0000-000000000001"
	rlsOrgB = "00000000-0000-0000-0000-0000000000b2"
)

// count runs one statement as the (non-superuser) application role with the given transaction-local
// settings, so row-level security applies exactly as it does in production.
func rlsCount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, settings map[string]string, query string) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for k, v := range settings {
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, k, v); err != nil {
			t.Fatal(err)
		}
	}
	var n int
	if err := tx.QueryRow(ctx, query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func rlsExec(ctx context.Context, pool *pgxpool.Pool, settings map[string]string, stmt string) error {
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
	_, err = tx.Exec(ctx, stmt)
	return err
}

// The three tables that carried an organization_id but had no row-level security now isolate
// organizations like the rest of the schema, and login (which resolves an identity before an
// organization is chosen) still works through its two narrow read-only exceptions.
func TestIdentityTablesAreIsolatedByRowLevelSecurity(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	appPool, err := testhelpers.AppPoolFromAdmin(ctx, admin, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)

	// Seeded as the superuser (RLS does not apply to it), the way an operator would.
	for _, stmt := range []string{
		`INSERT INTO app.organizations (id, name, slug) VALUES ('` + rlsOrgB + `', 'Org B', 'org-b') ON CONFLICT DO NOTHING`,
		`DELETE FROM app.organization_members WHERE user_id IN ('a-only','b-only','both')`,
		`INSERT INTO app.organization_members (organization_id, user_id, role) VALUES
			('` + rlsOrgA + `', 'a-only', 'viewer'), ('` + rlsOrgB + `', 'b-only', 'analyst'),
			('` + rlsOrgA + `', 'both', 'viewer'), ('` + rlsOrgB + `', 'both', 'tenant_admin')`,
		`INSERT INTO app.oidc_group_org_mappings (group_claim, organization_id, role) VALUES
			('grp-a', '` + rlsOrgA + `', 'viewer'), ('grp-b', '` + rlsOrgB + `', 'analyst') ON CONFLICT DO NOTHING`,
		`INSERT INTO app.audit_log_buffer (event_type, organization_id) VALUES ('RUN_QUERY', '` + rlsOrgA + `'), ('RUN_QUERY', '` + rlsOrgB + `')`,
	} {
		if _, err := admin.Exec(ctx, stmt); err != nil {
			t.Fatalf("seed: %v\n%s", err, stmt)
		}
	}

	members := `SELECT count(*) FROM app.organization_members WHERE user_id IN ('a-only','b-only','both')`
	t.Run("organization_members", func(t *testing.T) {
		if n := rlsCount(t, ctx, appPool, nil, members); n != 0 {
			t.Errorf("with no organization and no lookup the app role sees %d rows, want 0", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.current_org_id": rlsOrgA}, members); n != 2 {
			t.Errorf("inside org A: %d rows, want 2 (a-only, both)", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.current_org_id": rlsOrgB}, members); n != 2 {
			t.Errorf("inside org B: %d rows, want 2 (b-only, both)", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.membership_user_id": "both"}, members); n != 2 {
			t.Errorf("login lookup for 'both': %d rows, want its 2 memberships", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.membership_user_id": "a-only"}, members); n != 1 {
			t.Errorf("login lookup for 'a-only' must not reveal anyone else: %d rows, want 1", n)
		}
		if err := rlsExec(ctx, appPool, map[string]string{"app.current_org_id": rlsOrgA},
			`INSERT INTO app.organization_members (organization_id, user_id, role) VALUES ('`+rlsOrgB+`', 'smuggled', 'tenant_admin')`); err == nil {
			t.Error("a session scoped to org A wrote a membership into org B")
		}
		if err := rlsExec(ctx, appPool, map[string]string{"app.membership_user_id": "both"},
			`UPDATE app.organization_members SET role = 'tenant_admin' WHERE user_id = 'both'`); err != nil {
			t.Logf("update through the login lookup: %v", err)
		}
		var role string
		if err := admin.QueryRow(ctx, `SELECT role FROM app.organization_members WHERE organization_id='`+rlsOrgA+`' AND user_id='both'`).Scan(&role); err != nil {
			t.Fatal(err)
		}
		if role != "viewer" {
			t.Errorf("the login lookup changed a role to %q: it must be read-only", role)
		}
	})

	t.Run("oidc_group_org_mappings", func(t *testing.T) {
		q := `SELECT count(*) FROM app.oidc_group_org_mappings WHERE group_claim IN ('grp-a','grp-b')`
		if n := rlsCount(t, ctx, appPool, nil, q); n != 0 {
			t.Errorf("no context: %d mappings visible, want 0", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.oidc_groups": "grp-b"}, q); n != 1 {
			t.Errorf("the token's groups grp-b: %d mappings, want 1", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.oidc_groups": "grp-a\x1fgrp-b"}, q); n != 2 {
			t.Errorf("two groups: %d mappings, want 2", n)
		}
		if err := rlsExec(ctx, appPool, map[string]string{"app.oidc_groups": "grp-a"},
			`INSERT INTO app.oidc_group_org_mappings (group_claim, organization_id, role) VALUES ('evil', '`+rlsOrgA+`', 'admin')`); err == nil {
			t.Error("the group lookup allowed a write")
		}
	})

	t.Run("audit_log_buffer", func(t *testing.T) {
		q := `SELECT count(*) FROM app.audit_log_buffer`
		if n := rlsCount(t, ctx, appPool, nil, q); n != 0 {
			t.Errorf("no context: %d buffered entries visible, want 0", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.current_org_id": rlsOrgA}, q); n != 1 {
			t.Errorf("inside org A: %d entries, want 1", n)
		}
		if n := rlsCount(t, ctx, appPool, map[string]string{"app.scheduler_bypass": "true"}, q); n != 2 {
			t.Errorf("the replay worker (bypass): %d entries, want 2", n)
		}
	})

	t.Run("login still resolves through the membership store", func(t *testing.T) {
		store := auth.NewMembershipStore(appPool, false)
		if _, err := store.ResolvePrincipal(ctx, "both", "", "viewer"); err != auth.ErrOrganizationSelectionRequired {
			t.Errorf("a user in two orgs with no choice: err = %v, want ErrOrganizationSelectionRequired", err)
		}
		p, err := store.ResolvePrincipal(ctx, "both", rlsOrgB, "viewer")
		if err != nil || p.OrgID != rlsOrgB || p.Role != auth.RoleTenantAdmin {
			t.Errorf("preferred org B: %+v, %v", p, err)
		}
		if p, err := store.ResolvePrincipal(ctx, "a-only", "", "viewer"); err != nil || p.OrgID != rlsOrgA {
			t.Errorf("sole membership: %+v, %v", p, err)
		}
		if _, err := store.ResolvePrincipal(ctx, "nobody", "", "viewer"); err != auth.ErrNoOrganizationMembership {
			t.Errorf("unknown user: err = %v", err)
		}
		gp, err := store.ResolveFromGroupClaims(ctx, "group-user", "", "viewer", []string{"grp-b"})
		if err != nil || gp.OrgID != rlsOrgB || gp.Role != auth.RoleAnalyst {
			t.Errorf("group mapping: %+v, %v", gp, err)
		}
		members, err := store.ListOrgMembers(ctx, rlsOrgB)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range members {
			if m.OrgID != rlsOrgB {
				t.Errorf("ListOrgMembers(B) returned a row of org %s", m.OrgID)
			}
		}
	})
}
