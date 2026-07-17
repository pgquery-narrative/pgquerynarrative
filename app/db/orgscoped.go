package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// DB is the subset of pgxpool.Pool used by application services.
type DB interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Ensure *pgxpool.Pool satisfies DB.
var _ DB = (*pgxpool.Pool)(nil)

// OrgScoped wraps a pool so every query runs with app.current_org_id set from context.
// This activates Postgres RLS policies for defense-in-depth tenant isolation.
type OrgScoped struct {
	pool *pgxpool.Pool
}

// NewOrgScoped returns an org-aware DB wrapper. If pool is nil, returns nil.
func NewOrgScoped(pool *pgxpool.Pool) *OrgScoped {
	if pool == nil {
		return nil
	}
	return &OrgScoped{pool: pool}
}

// Pool returns the underlying pool for health checks and metrics.
func (o *OrgScoped) Pool() *pgxpool.Pool {
	if o == nil {
		return nil
	}
	return o.pool
}

var _ DB = (*OrgScoped)(nil)

func (o *OrgScoped) acquireWithOrg(ctx context.Context) (*pgxpool.Conn, error) {
	if o == nil || o.pool == nil {
		return nil, pgx.ErrNoRows
	}
	conn, err := o.pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	orgID := auth.OrgIDFromContext(ctx)
	if _, err := conn.Exec(ctx, `SELECT set_config('app.current_org_id', $1, false)`, orgID); err != nil {
		conn.Release()
		return nil, err
	}
	return conn, nil
}

func clearOrg(conn *pgxpool.Conn) {
	if conn == nil {
		return
	}
	_, _ = conn.Exec(context.Background(), `SELECT set_config('app.current_org_id', '', false)`)
}

// Query implements DB. The connection stays acquired until Rows.Close.
func (o *OrgScoped) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	conn, err := o.acquireWithOrg(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := conn.Query(ctx, sql, args...)
	if err != nil {
		clearOrg(conn)
		conn.Release()
		return nil, err
	}
	return &orgRows{Rows: rows, conn: conn}, nil
}

type orgRows struct {
	pgx.Rows
	conn   *pgxpool.Conn
	closed bool
}

func (r *orgRows) Close() {
	if r.closed {
		return
	}
	r.closed = true
	r.Rows.Close()
	clearOrg(r.conn)
	r.conn.Release()
}

type orgRow struct {
	err  error
	row  pgx.Row
	conn *pgxpool.Conn
}

func (r orgRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	err := r.row.Scan(dest...)
	clearOrg(r.conn)
	r.conn.Release()
	return err
}

// QueryRow implements DB.
func (o *OrgScoped) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	conn, err := o.acquireWithOrg(ctx)
	if err != nil {
		return orgRow{err: err}
	}
	return orgRow{row: conn.QueryRow(ctx, sql, args...), conn: conn}
}

// Exec implements DB.
func (o *OrgScoped) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	conn, err := o.acquireWithOrg(ctx)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	defer func() {
		clearOrg(conn)
		conn.Release()
	}()
	return conn.Exec(ctx, sql, arguments...)
}

// Begin implements DB using a transaction-local org setting.
func (o *OrgScoped) Begin(ctx context.Context) (pgx.Tx, error) {
	if o == nil || o.pool == nil {
		return nil, pgx.ErrNoRows
	}
	return BeginOrgTx(ctx, o.pool)
}

// BeginOrgTx starts a transaction with app.current_org_id set for RLS policies.
func BeginOrgTx(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	orgID := auth.OrgIDFromContext(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, orgID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// BeginSchedulerTx starts a transaction that can claim work across organizations.
func BeginSchedulerTx(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.scheduler_bypass', 'true', true)`); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}
