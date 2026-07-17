package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

func TestMembershipStore_ResolveAndAutoJoin(t *testing.T) {
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

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
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

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	store := auth.NewMembershipStore(pool, true)
	p, err := store.ResolvePrincipal(ctx, "oidc-user-1", "", auth.RoleAnalyst)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if p.OrgID != auth.DefaultOrganizationID {
		t.Fatalf("org = %s", p.OrgID)
	}
	if p.Role != auth.RoleAnalyst {
		t.Fatalf("role = %s", p.Role)
	}

	p2, err := store.ResolvePrincipal(ctx, "oidc-user-1", auth.DefaultOrganizationID, auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if p2.Role != auth.RoleAnalyst {
		t.Fatalf("membership role should win, got %s", p2.Role)
	}
}
