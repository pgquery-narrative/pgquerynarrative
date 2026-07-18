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
//
// Every method below runs its statement inside a dedicated transaction and sets
// app.current_org_id with SET LOCAL (transaction-scoped), rather than the session-level
// set_config used previously. This matters because pgxpool.Pool.Begin acquires-and-releases
// the underlying connection automatically on Commit/Rollback, and a transaction-local setting
// is discarded by Postgres itself the instant the transaction ends — by commit, rollback, *or*
// the connection being dropped. So even if a caller forgets to close iterators, or a request
// context is canceled or panics mid-flight, rolling back (which our deferred cleanup always
// does unless we reach a successful Commit) guarantees no stale org_id is ever left on a
// connection returned to the pool. The previous approach — session-level set_config plus a
// best-effort, ignored-error clear afterward — could leak an org_id onto a pooled connection if
// that clear Exec itself failed or was never reached.
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

// beginOrgTx starts a pool-managed transaction with app.current_org_id set via SET LOCAL for
// the given orgID. Pool.Begin manages the underlying connection's acquire/release lifecycle,
// so callers only need to Commit or Rollback the returned Tx.
func beginOrgTx(ctx context.Context, pool *pgxpool.Pool, orgID string) (pgx.Tx, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.current_org_id', $1, true)`, orgID); err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

// BeginOrgTx starts a transaction with app.current_org_id set (via SET LOCAL) for RLS
// policies, using the organization from ctx.
func BeginOrgTx(ctx context.Context, pool *pgxpool.Pool) (pgx.Tx, error) {
	return beginOrgTx(ctx, pool, auth.OrgIDFromContext(ctx))
}

// WithOrgTx runs fn inside a transaction scoped (via SET LOCAL) to the organization from ctx,
// committing on success and rolling back on error or panic. Prefer this over the Query/
// QueryRow/Exec methods below when a call site needs several statements to execute atomically
// within one RLS-scoped unit of work; it also guarantees cleanup (via defer) even if fn panics,
// so no connection is ever returned to the pool with a stale org_id.
func WithOrgTx(ctx context.Context, pool *pgxpool.Pool, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return withOrgTxID(ctx, pool, auth.OrgIDFromContext(ctx), fn)
}

// WithOrgTxID is WithOrgTx for an explicit organization ID, for callers without a request
// context carrying the organization (e.g. background workers processing a specific org's row).
func WithOrgTxID(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return withOrgTxID(ctx, pool, orgID, fn)
}

func withOrgTxID(ctx context.Context, pool *pgxpool.Pool, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if pool == nil {
		return pgx.ErrNoRows
	}
	tx, err := beginOrgTx(ctx, pool, orgID)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
}

// Query implements DB. The org-scoped transaction stays open until Rows.Close, which commits
// (or rolls back, if iteration ended in error) and releases the connection — see the OrgScoped
// doc comment for why this cannot leak org state the way session-level set_config could.
func (o *OrgScoped) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if o == nil || o.pool == nil {
		return nil, pgx.ErrNoRows
	}
	tx, err := BeginOrgTx(ctx, o.pool)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, err
	}
	return &orgRows{Rows: rows, tx: tx}, nil
}

// orgRows wraps pgx.Rows to commit (or roll back on error) the org-scoped transaction when the
// caller closes the rows, ending the SET LOCAL scope on the same statement boundary as normal
// query completion.
type orgRows struct {
	pgx.Rows
	tx     pgx.Tx
	closed bool
}

// Close ends iteration and finalizes the underlying org-scoped transaction. Safe to call more
// than once (e.g. via both an explicit Close and a deferred Close).
func (r *orgRows) Close() {
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

// orgRow wraps pgx.Row to commit (or roll back on error) the org-scoped transaction once the
// caller scans the row.
type orgRow struct {
	err error
	row pgx.Row
	tx  pgx.Tx
}

func (r orgRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	err := r.row.Scan(dest...)
	if err != nil {
		_ = r.tx.Rollback(context.Background())
		return err
	}
	return r.tx.Commit(context.Background())
}

// QueryRow implements DB. The org-scoped transaction is committed (or rolled back on scan
// error) when the caller calls Scan.
func (o *OrgScoped) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if o == nil || o.pool == nil {
		return orgRow{err: pgx.ErrNoRows}
	}
	tx, err := BeginOrgTx(ctx, o.pool)
	if err != nil {
		return orgRow{err: err}
	}
	return orgRow{row: tx.QueryRow(ctx, sql, args...), tx: tx}
}

// Exec implements DB, running the statement inside a short-lived org-scoped transaction that
// is committed on success or rolled back on error before Exec returns.
func (o *OrgScoped) Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	if o == nil || o.pool == nil {
		return pgconn.CommandTag{}, pgx.ErrNoRows
	}
	var tag pgconn.CommandTag
	err := WithOrgTx(ctx, o.pool, func(ctx context.Context, tx pgx.Tx) error {
		var execErr error
		tag, execErr = tx.Exec(ctx, sql, arguments...)
		return execErr
	})
	return tag, err
}

// Begin implements DB using a transaction-local org setting.
func (o *OrgScoped) Begin(ctx context.Context) (pgx.Tx, error) {
	if o == nil || o.pool == nil {
		return nil, pgx.ErrNoRows
	}
	return BeginOrgTx(ctx, o.pool)
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
