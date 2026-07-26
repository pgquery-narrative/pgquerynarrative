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

func queryWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) (pgx.Rows, error) {
	if pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	if orgID == "" {
		orgID = DefaultOrgID()
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, orgID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &orgTxRows{Rows: rows, tx: tx}, nil
}

// orgTxRows commits the org-scoped transaction when the caller closes the rows.
type orgTxRows struct {
	pgx.Rows
	tx     pgx.Tx
	closed bool
}

func (r *orgTxRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.Rows.Close()
	if r.Rows.Err() != nil {
		_ = r.tx.Rollback(context.Background())
		return
	}
	_ = r.tx.Commit(context.Background())
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
