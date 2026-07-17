package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// ExecWithOrg runs a statement with app.current_org_id set for RLS policies.
func ExecWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) error {
	if pool == nil {
		return nil
	}
	if orgID == "" {
		orgID = auth.DefaultOrgID()
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, orgID); err != nil {
		return err
	}
	_, err = conn.Exec(ctx, sql, args...)
	_, _ = conn.Exec(context.Background(), `SELECT set_config('app.current_org_id', '', false)`)
	return err
}

// QueryRowWithOrg runs QueryRow with org context set until Scan completes.
func QueryRowWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) func(dest ...any) error {
	if pool == nil {
		return func(dest ...any) error { return nil }
	}
	if orgID == "" {
		orgID = auth.DefaultOrgID()
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return func(dest ...any) error { return err }
	}
	if _, err := conn.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, orgID); err != nil {
		conn.Release()
		return func(dest ...any) error { return err }
	}
	row := conn.QueryRow(ctx, sql, args...)
	return func(dest ...any) error {
		defer func() {
			_, _ = conn.Exec(context.Background(), `SELECT set_config('app.current_org_id', '', false)`)
			conn.Release()
		}()
		return row.Scan(dest...)
	}
}
