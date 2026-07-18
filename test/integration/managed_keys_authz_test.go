package integration

import (
	"context"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/security"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

func setupMigratedPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				break
			}
		}
		if waitCtx.Err() != nil {
			t.Fatal("postgres not ready")
		}
		time.Sleep(200 * time.Millisecond)
	}

	migrationsPath, err := filepath.Abs(filepath.Join("..", "..", "app", "db", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}
	srcErr, dbErr := m.Close()
	if srcErr != nil || dbErr != nil {
		t.Fatalf("migrate close: %v %v", srcErr, dbErr)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func TestManagedAPIKeys_CreateAuthenticateRevoke(t *testing.T) {
	pool, ctx := setupMigratedPool(t)
	orgID := auth.DefaultOrgID()
	store := auth.NewManagedKeyStore(pool)
	issued, err := store.Create(ctx, orgID, auth.RoleAnalyst, "admin-user", []string{"read"}, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if issued.Secret == "" || issued.Prefix == "" {
		t.Fatal("expected secret and prefix")
	}
	entry, ok, err := store.LookupBySecret(ctx, issued.Secret)
	if err != nil || !ok {
		t.Fatalf("lookup: ok=%v err=%v", ok, err)
	}
	if entry.ID != issued.ID || entry.OrgID != orgID {
		t.Fatalf("entry mismatch: %+v", entry)
	}
	authn := auth.NewAuthenticator(true, "", "", "", nil)
	authn.SetManagedKeyStore(store)
	r, _ := httpNewRequestWithBearer(issued.Secret)
	p, ok := authn.ValidatePrincipal(r)
	if !ok || p.UserID != issued.ID {
		t.Fatalf("auth failed: ok=%v principal=%+v", ok, p)
	}
	revoked, err := store.Revoke(ctx, orgID, issued.ID)
	if err != nil || !revoked {
		t.Fatalf("revoke: %v %v", revoked, err)
	}
	_, ok = authn.ValidatePrincipal(r)
	if ok {
		t.Fatal("expected auth failure after revoke")
	}
}

func TestConnectionAuthz_AssignAndGrantMatrix(t *testing.T) {
	pool, ctx := setupMigratedPool(t)
	orgID := auth.DefaultOrgID()
	authz := auth.NewConnectionAuthorizer(pool)
	if err := authz.AssignConnection(ctx, orgID, "analytics"); err != nil {
		t.Fatal(err)
	}
	user := "user-finance"
	// Without grant, analyst should be denied once org has assignments.
	if err := authz.AuthorizeConnection(ctx, orgID, user, auth.RoleAnalyst, "analytics", auth.ActionQuery); err == nil {
		t.Fatal("expected deny without grant")
	}
	if err := authz.GrantPermission(ctx, orgID, "analytics", user, map[string]bool{auth.ActionQuery: true}); err != nil {
		t.Fatal(err)
	}
	if err := authz.AuthorizeConnection(ctx, orgID, user, auth.RoleAnalyst, "analytics", auth.ActionQuery); err != nil {
		t.Fatalf("expected allow after grant: %v", err)
	}
	if err := authz.AuthorizeConnection(ctx, orgID, user, auth.RoleAnalyst, "analytics", auth.ActionReport); err == nil {
		t.Fatal("expected deny for ungranted action")
	}
}

func TestExplainSnapshotSealEnvelope(t *testing.T) {
	secret := []byte("integration-data-key")
	sealed, err := security.Seal(secret, "SELECT 1 FROM demo.sales WHERE email = $1")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := security.Open(secret, sealed)
	if err != nil || plain == "" {
		t.Fatalf("open: %v %q", err, plain)
	}
}

func httpNewRequestWithBearer(secret string) (*http.Request, error) {
	r, err := http.NewRequest(http.MethodGet, "http://example/api/v1/queries", nil)
	if err != nil {
		return nil, err
	}
	r.Header.Set("Authorization", "Bearer "+secret)
	return r, nil
}
