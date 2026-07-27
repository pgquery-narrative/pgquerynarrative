package integration

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

const multiOrgDataEncKey = "integration-data-encryption-key-32b!"

// TestMultiOrg_TenantAdminDeniesCrossOrgAdminScope verifies tenant admins cannot
// scope management requests to another organisation (HTTP admin guard).
func TestMultiOrg_TenantAdminDeniesCrossOrgAdminScope(t *testing.T) {
	admin, _, ctx := multiOrgPostgres(t)
	defer admin.Close()

	orgB := insertPilotOrg(t, ctx, admin, "multi-org-admin-b", "multi-org-admin-b")
	orgA := auth.DefaultOrganizationID

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/memberships?organization_id="+orgB, nil)
	req = req.WithContext(auth.WithPrincipal(ctx, auth.Principal{
		UserID: "tenant-admin",
		OrgID:  orgA,
		Role:   auth.RoleTenantAdmin,
	}))
	rec := httptest.NewRecorder()

	if !auth.RequireAdmin(rec, req) {
		t.Fatal("tenant admin should pass admin role gate")
	}
	orgID, ok := auth.ResolveAdminOrgScope(rec, req, orgB)
	if !ok {
		t.Fatal("tenant admin should resolve to their own organisation")
	}
	if orgID != orgA {
		t.Fatalf("tenant admin must not scope admin requests to another organisation, got %q", orgID)
	}

	platformReq := httptest.NewRequest(http.MethodGet, "/api/v1/admin/organizations", nil)
	platformReq = platformReq.WithContext(auth.WithPrincipal(ctx, auth.Principal{
		UserID: "platform-admin",
		OrgID:  orgA,
		Role:   auth.RolePlatformAdmin,
	}))
	platformRec := httptest.NewRecorder()
	if !auth.RequirePlatformAdmin(platformRec, platformReq) {
		t.Fatal("platform admin should access platform-only routes")
	}
}

// TestMultiOrg_PerOrgSchemaEnforcement verifies tenant allowed_schemas drive validation.
func TestMultiOrg_PerOrgSchemaEnforcement(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	defer admin.Close()

	orgA := auth.DefaultOrganizationID
	orgB := insertPilotOrg(t, ctx, admin, "multi-org-schema-b", "multi-org-schema-b")

	if _, err := admin.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS tenant_b;
		CREATE TABLE IF NOT EXISTS tenant_b.metrics (id int NOT NULL);
		TRUNCATE tenant_b.metrics;
		INSERT INTO tenant_b.metrics (id) VALUES (1);
		GRANT USAGE ON SCHEMA tenant_b TO pgquerynarrative_readonly;
		GRANT SELECT ON ALL TABLES IN SCHEMA tenant_b TO pgquerynarrative_readonly;
	`); err != nil {
		t.Fatalf("seed tenant_b schema: %v", err)
	}

	pools, secrets, _ := multiOrgPools(t, admin, connStr)
	defer pools.Close()

	readonlyDSN := readonlyDSNFromAdmin(t, connStr)
	if err := secrets.Upsert(ctx, orgA, "default", readonlyDSN, []string{"demo"}); err != nil {
		t.Fatalf("upsert org A secret: %v", err)
	}
	if err := secrets.Upsert(ctx, orgB, "default", readonlyDSN, []string{"tenant_b"}); err != nil {
		t.Fatalf("upsert org B secret: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunnerForConnection(pools, "default", validator, 100, 30*time.Second)

	ctxA := auth.WithPrincipal(ctx, auth.Principal{OrgID: orgA})
	ctxB := auth.WithPrincipal(ctx, auth.Principal{OrgID: orgB})

	if err := runner.ValidateQueryWithContext(ctxA, "SELECT id FROM demo.sales"); err != nil {
		t.Fatalf("org A should allow demo schema: %v", err)
	}
	if err := runner.ValidateQueryWithContext(ctxA, "SELECT id FROM tenant_b.metrics"); err == nil {
		t.Fatal("org A must not allow tenant_b schema")
	}
	if err := runner.ValidateQueryWithContext(ctxB, "SELECT id FROM tenant_b.metrics"); err != nil {
		t.Fatalf("org B should allow tenant_b schema: %v", err)
	}
	if err := runner.ValidateQueryWithContext(ctxB, "SELECT id FROM demo.sales"); err == nil {
		t.Fatal("org B must not allow demo schema")
	}
}

// TestMultiOrg_StatStatementsUsesOrgPool verifies stats resolve the org-scoped pool.
func TestMultiOrg_StatStatementsUsesOrgPool(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	defer admin.Close()

	orgA := auth.DefaultOrganizationID
	orgB := insertPilotOrg(t, ctx, admin, "multi-org-stats-b", "multi-org-stats-b")

	pools, secrets, _ := multiOrgPools(t, admin, connStr)
	defer pools.Close()

	readonlyDSN := readonlyDSNFromAdmin(t, connStr)
	if err := secrets.Upsert(ctx, orgA, "default", readonlyDSN, []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	if err := secrets.Upsert(ctx, orgB, "default", readonlyDSN, []string{"demo"}); err != nil {
		t.Fatal(err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunnerForConnection(pools, "default", validator, 100, 30*time.Second)

	ctxA := auth.WithPrincipal(ctx, auth.Principal{OrgID: orgA})
	ctxB := auth.WithPrincipal(ctx, auth.Principal{OrgID: orgB})

	poolA := runner.StatsPoolFor(ctxA)
	poolB := runner.StatsPoolFor(ctxB)
	if poolA == nil || poolB == nil {
		t.Fatal("expected org stats pools to be available")
	}
	if poolA == poolB {
		t.Fatal("expected distinct org-scoped stats pools for different organisations")
	}

	// Background context must not reuse an org-owned pool.
	if bg := runner.StatsPool(); bg != nil && bg == poolA {
		t.Fatal("background stats pool must not reuse org A pool")
	}
}

// TestMultiOrg_TenantDSNRotationInvalidatesPool verifies secret rotation/deletion
// closes cached tenant pools immediately.
func TestMultiOrg_TenantDSNRotationInvalidatesPool(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	defer admin.Close()

	orgA := auth.DefaultOrganizationID
	pools, secrets, _ := multiOrgPools(t, admin, connStr)
	defer pools.Close()

	readonlyDSN := readonlyDSNFromAdmin(t, connStr)
	if err := secrets.Upsert(ctx, orgA, "default", readonlyDSN, []string{"demo"}); err != nil {
		t.Fatal(err)
	}

	ctxA := auth.WithPrincipal(ctx, auth.Principal{OrgID: orgA})
	poolBefore := pools.ReadOnly(ctxA, "default")
	if poolBefore == nil {
		t.Fatal("expected tenant pool before rotation")
	}

	rotatedDSN := readonlyDSN + "&application_name=rotated"
	if err := secrets.Upsert(ctx, orgA, "default", rotatedDSN, []string{"demo"}); err != nil {
		t.Fatal(err)
	}
	poolAfterRotate := pools.ReadOnly(ctxA, "default")
	if poolAfterRotate == nil {
		t.Fatal("expected tenant pool after rotation")
	}
	if poolAfterRotate == poolBefore {
		t.Fatal("expected policy-version change to invalidate cached pool immediately")
	}

	pools.InvalidateOrgReadOnlyPool(orgA, "default")
	poolAfterExplicit := pools.ReadOnly(ctxA, "default")
	if poolAfterExplicit == nil {
		t.Fatal("expected tenant pool after explicit invalidation")
	}

	if err := secrets.Delete(ctx, orgA, "default"); err != nil {
		t.Fatal(err)
	}
	pools.InvalidateOrgReadOnlyPool(orgA, "default")

	if _, _, ok, err := secrets.OpenDSN(ctx, orgA, "default"); err != nil || ok {
		t.Fatalf("expected deleted secret to be unavailable: ok=%v err=%v", ok, err)
	}
	poolAfterDelete := pools.ReadOnly(ctxA, "default")
	if poolAfterDelete == nil {
		t.Fatal("expected shared fallback pool after tenant secret deletion")
	}
	if poolAfterDelete == poolBefore || poolAfterDelete == poolAfterRotate {
		t.Fatal("expected deleted tenant DSN pool to be evicted")
	}
}

// TestMultiOrg_MissingEncryptionKeyNeverFallsBackToShared verifies P0-2: when a
// tenant secret exists but the encryption key is absent, queries must fail closed
// instead of silently using the shared catalog pool.
func TestMultiOrg_MissingEncryptionKeyNeverFallsBackToShared(t *testing.T) {
	admin, connStr, ctx := multiOrgPostgres(t)
	defer admin.Close()

	orgA := auth.DefaultOrganizationID
	poolsWithKey, secrets, _ := multiOrgPools(t, admin, connStr)
	defer poolsWithKey.Close()

	readonlyDSN := readonlyDSNFromAdmin(t, connStr)
	if err := secrets.Upsert(ctx, orgA, "default", readonlyDSN, []string{"demo"}); err != nil {
		t.Fatal(err)
	}

	// New pool manager with empty encryption key but the same secret rows.
	poolsNoKey, err := db.NewPools(ctx, poolsWithKeyConfig(t, connStr))
	if err != nil {
		t.Fatal(err)
	}
	defer poolsNoKey.Close()
	secretsNoKey := auth.NewOrgConnectionSecretStore(poolsNoKey.App, "")
	poolsNoKey.SetOrgDSNLookup(secretsNoKey)

	res, err := secretsNoKey.Resolve(ctx, orgA, "default")
	if err != nil {
		t.Fatal(err)
	}
	if res.Mode != auth.OrgConnectionKeyUnavailable {
		t.Fatalf("expected key_unavailable, got %s", res.Mode)
	}

	ctxA := auth.WithPrincipal(ctx, auth.Principal{OrgID: orgA})
	if pool := poolsNoKey.ReadOnly(ctxA, "default"); pool != nil {
		t.Fatal("missing encryption key must not fall back to the shared analytical pool")
	}
}

func poolsWithKeyConfig(t *testing.T, connStr string) config.DatabaseConfig {
	t.Helper()
	pcfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	host := pcfg.ConnConfig.Host
	port := 5432
	if pcfg.ConnConfig.Port != 0 {
		port = int(pcfg.ConnConfig.Port)
	}
	return config.DatabaseConfig{
		Host:             host,
		Port:             port,
		Database:         pcfg.ConnConfig.Database,
		User:             pcfg.ConnConfig.User,
		Password:         pcfg.ConnConfig.Password,
		ReadOnlyUser:     "pgquerynarrative_readonly",
		ReadOnlyPassword: "pgquerynarrative_readonly",
		SSLMode:          "disable",
		AllowedSchemas:   []string{"demo"},
		DefaultID:        "default",
		MaxConnections:   5,
		QueryTimeout:     30 * time.Second,
		Connections: []config.DataConnectionConfig{{
			ID:               "default",
			Name:             "Default",
			Host:             host,
			Port:             port,
			Database:         pcfg.ConnConfig.Database,
			ReadOnlyUser:     "pgquerynarrative_readonly",
			ReadOnlyPassword: "pgquerynarrative_readonly",
			SSLMode:          "disable",
			AllowedSchemas:   []string{"demo"},
			QueryTimeout:     30 * time.Second,
		}},
	}
}

func multiOrgPostgres(t *testing.T) (*pgxpool.Pool, string, context.Context) {
	t.Helper()
	ctx := context.Background()
	pool, connStr := pilotPostgres(t, ctx)
	return pool, connStr, ctx
}

func multiOrgPools(t *testing.T, admin *pgxpool.Pool, connStr string) (*db.Pools, *auth.OrgConnectionSecretStore, string) {
	t.Helper()
	ctx := context.Background()
	if err := testhelpers.EnsurePostgresRoles(ctx, admin); err != nil {
		t.Fatal(err)
	}

	pcfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	host := pcfg.ConnConfig.Host
	port := 5432
	if pcfg.ConnConfig.Port != 0 {
		port = int(pcfg.ConnConfig.Port)
	}

	dbCfg := config.DatabaseConfig{
		Host:             host,
		Port:             port,
		Database:         pcfg.ConnConfig.Database,
		User:             pcfg.ConnConfig.User,
		Password:         pcfg.ConnConfig.Password,
		ReadOnlyUser:     "pgquerynarrative_readonly",
		ReadOnlyPassword: "pgquerynarrative_readonly",
		SSLMode:          "disable",
		AllowedSchemas:   []string{"demo"},
		DefaultID:        "default",
		MaxConnections:   5,
		QueryTimeout:     30 * time.Second,
		Connections: []config.DataConnectionConfig{{
			ID:               "default",
			Name:             "Default",
			Host:             host,
			Port:             port,
			Database:         pcfg.ConnConfig.Database,
			ReadOnlyUser:     "pgquerynarrative_readonly",
			ReadOnlyPassword: "pgquerynarrative_readonly",
			SSLMode:          "disable",
			AllowedSchemas:   []string{"demo"},
			QueryTimeout:     30 * time.Second,
		}},
	}

	pools, err := db.NewPools(ctx, dbCfg)
	if err != nil {
		t.Fatal(err)
	}
	secrets := auth.NewOrgConnectionSecretStore(pools.App, multiOrgDataEncKey)
	pools.SetOrgDSNLookup(secrets)
	return pools, secrets, connStr
}

func readonlyDSNFromAdmin(t *testing.T, adminConnStr string) string {
	t.Helper()
	u, err := url.Parse(adminConnStr)
	if err != nil {
		t.Fatalf("parse admin DSN: %v", err)
	}
	u.User = url.UserPassword("pgquerynarrative_readonly", "pgquerynarrative_readonly")
	return u.String()
}
