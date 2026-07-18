package integration

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// TestMigrationsRoundTrip verifies the latest migration can roll back one step
// and re-apply without leaving the schema in a dirty state.
func TestMigrationsRoundTrip(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitForPostgres(t, ctx, connStr)

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = m.Close()
	})

	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}
	version, dirty, err := m.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if dirty {
		t.Fatal("schema_migrations dirty after up")
	}
	if version < db.RequiredMigrationVersion {
		t.Fatalf("version %d < required %d", version, db.RequiredMigrationVersion)
	}

	if err := m.Steps(-1); err != nil {
		t.Fatalf("migrate down one step: %v", err)
	}
	versionAfterDown, dirty, err := m.Version()
	if err != nil {
		t.Fatalf("version after down: %v", err)
	}
	if dirty {
		t.Fatal("schema_migrations dirty after down")
	}
	if versionAfterDown != version-1 {
		t.Fatalf("expected version %d after down, got %d", version-1, versionAfterDown)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var managedKeysExists bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'app' AND table_name = 'managed_api_keys'
		)
	`).Scan(&managedKeysExists)
	if err != nil {
		t.Fatal(err)
	}
	if managedKeysExists {
		t.Fatal("app.managed_api_keys still exists after rolling back migration 42")
	}

	if err := m.Steps(1); err != nil {
		t.Fatalf("migrate up one step: %v", err)
	}
	finalVersion, dirty, err := m.Version()
	if err != nil {
		t.Fatalf("final version: %v", err)
	}
	if dirty {
		t.Fatal("schema_migrations dirty after re-up")
	}
	if finalVersion != version {
		t.Fatalf("expected version %d after re-up, got %d", version, finalVersion)
	}

	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'app' AND table_name = 'managed_api_keys'
		)
	`).Scan(&managedKeysExists)
	if err != nil {
		t.Fatal(err)
	}
	if !managedKeysExists {
		t.Fatal("app.managed_api_keys missing after re-applying migration 42")
	}
}
