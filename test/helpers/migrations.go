package helpers

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
)

// RunMigrations runs all up migrations from internal/db/migrations against connStr, resolved
// relative to the caller's package directory (so it must be called from a test two directories
// below the repo root, e.g. test/integration or test/e2e). It also enables pgvector first
// (CREATE EXTENSION IF NOT EXISTS, so it's a no-op where unneeded) so migration
// 000007_pgvector_embeddings.up.sql can run against any test Postgres image.
func RunMigrations(t *testing.T, connStr string) {
	t.Helper()

	migrationsPath, err := filepath.Abs("../../internal/db/migrations")
	if err != nil {
		t.Fatalf("migrations path: %v", err)
	}

	extPool, err := pgxpool.New(context.Background(), connStr)
	if err != nil {
		t.Fatalf("pool for extension: %v", err)
	}
	_, _ = extPool.Exec(context.Background(), "CREATE EXTENSION IF NOT EXISTS vector")
	extPool.Close()

	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("migrate new: %v", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}
}
