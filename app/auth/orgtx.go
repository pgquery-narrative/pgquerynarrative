package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func withOrgTx(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	if orgID == "" {
		orgID = DefaultOrgID()
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, orgID); err != nil {
		return err
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func execWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) error {
	return withOrgTx(ctx, pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

func queryRowWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) func(dest ...any) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return func(dest ...any) error { return err }
	}
	if orgID == "" {
		orgID = DefaultOrgID()
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, orgID); err != nil {
		_ = tx.Rollback(ctx)
		return func(dest ...any) error { return err }
	}
	row := tx.QueryRow(ctx, sql, args...)
	return func(dest ...any) error {
		scanErr := row.Scan(dest...)
		if scanErr != nil {
			_ = tx.Rollback(context.Background())
			return scanErr
		}
		return tx.Commit(context.Background())
	}
}
