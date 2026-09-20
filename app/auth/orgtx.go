package auth

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// withLookupTx runs fn in a transaction where the identity tables reveal rows the caller is
// entitled to see before an organization is chosen (login). settings are transaction-local
// GUCs the row-level-security policies read: app.membership_user_id (the caller's own
// memberships in every organization) and app.oidc_groups (the mappings for the token's groups).
// The exceptions are read-only: writes stay scoped to one organization by the policies'
// WITH CHECK.
func withLookupTx(ctx context.Context, pool *pgxpool.Pool, settings map[string]string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for name, value := range settings {
		if _, err := tx.Exec(ctx, `SELECT set_config($1, $2, true)`, name, value); err != nil {
			return err
		}
	}
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// orgID must be non-empty: silently substituting a default org for an
// unscoped caller would redirect its write to the wrong organization instead
// of failing loudly.
func withOrgTx(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if pool == nil {
		return fmt.Errorf("database pool is not configured")
	}
	if orgID == "" {
		return fmt.Errorf("withOrgTx: organization id is required")
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

// orgID must be non-empty; see withOrgTx.
func queryWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) (pgx.Rows, error) {
	if pool == nil {
		return nil, fmt.Errorf("database pool is not configured")
	}
	if orgID == "" {
		return nil, fmt.Errorf("queryWithOrg: organization id is required")
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

// orgID must be non-empty; see withOrgTx.
func queryRowWithOrg(ctx context.Context, pool *pgxpool.Pool, orgID, sql string, args ...any) func(dest ...any) error {
	if orgID == "" {
		return func(dest ...any) error { return fmt.Errorf("queryRowWithOrg: organization id is required") }
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return func(dest ...any) error { return err }
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
