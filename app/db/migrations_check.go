package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RequiredMigrationVersion is the minimum schema_migrations.version required at startup.
const RequiredMigrationVersion uint = 32

// CheckMigrationVersion ensures golang-migrate has applied all required schema changes.
func CheckMigrationVersion(ctx context.Context, pool *pgxpool.Pool) error {
	if pool == nil {
		return fmt.Errorf("app pool not configured")
	}
	var version int64
	var dirty bool
	err := pool.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)
	if err != nil {
		return fmt.Errorf("schema migrations not initialized (required >= %d): run database migrations: %w", RequiredMigrationVersion, err)
	}
	if dirty {
		return fmt.Errorf("schema migrations dirty at version %d: resolve with migrate force before starting", version)
	}
	if version < int64(RequiredMigrationVersion) {
		return fmt.Errorf("schema migration version %d < required %d: run database migrations", version, RequiredMigrationVersion)
	}
	return nil
}
