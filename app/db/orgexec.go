package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// ExecWithOrg runs a statement with app.current_org_id set (via SET LOCAL, transaction-local)
// for RLS policies. Unlike a session-level set_config, the setting is discarded by Postgres
// the instant the transaction ends, so it cannot leak onto the connection once released back
// to the pool.
func ExecWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) error {
	if pool == nil {
		return nil
	}
	if orgID == "" {
		orgID = auth.DefaultOrgID()
	}
	return withOrgTxID(ctx, pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, args...)
		return err
	})
}

// QueryRowWithOrg runs QueryRow with app.current_org_id set (via SET LOCAL) until the returned
// scan function is called, which commits (or rolls back on scan error) the underlying
// transaction.
func QueryRowWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) func(dest ...any) error {
	if pool == nil {
		return func(dest ...any) error { return nil }
	}
	if orgID == "" {
		orgID = auth.DefaultOrgID()
	}
	tx, err := beginOrgTx(ctx, pool, orgID)
	if err != nil {
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
