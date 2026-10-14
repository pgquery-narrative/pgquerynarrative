package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/debuglog"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/internal/errors"
)

type ColumnInfo struct {
	Name string
	Type string
}

type Result struct {
	Columns          []ColumnInfo
	Rows             [][]interface{}
	RowCount         int
	ExecutionTimeMs  int64
	RowLimitApplied  int
	OriginalRowLimit int
}

type poolResolver interface {
	ReadOnly(ctx context.Context, connectionID string) *pgxpool.Pool
}

// sharedRoleResolver is implemented by a pool resolver that knows whether an organization has
// credentials of its own for a connection.
type sharedRoleResolver interface {
	SharedReadOnlyRole(ctx context.Context, connectionID string) bool
}

type schemaResolver interface {
	AllowedSchemas(ctx context.Context, connectionID string) []string
}

type Runner struct {
	pool                *pgxpool.Pool
	poolResolver        poolResolver
	schemaResolver      schemaResolver
	connectionID        string
	validator           *Validator
	maxRows             int
	queryLimit          time.Duration
	allowExplainAnalyze bool
	maxResultBytes      int
	maxCellBytes        int
	maxColumns          int
}

// RunnerOption configures optional Runner behavior.
type RunnerOption func(*Runner)

// WithExplainAnalyze sets whether EXPLAIN ANALYZE (query execution) is permitted.
func WithExplainAnalyze(enabled bool) RunnerOption {
	return func(r *Runner) {
		r.allowExplainAnalyze = enabled
	}
}

// WithResultLimits bounds result materialization before responses reach clients.
func WithResultLimits(maxResultBytes, maxCellBytes, maxColumns int) RunnerOption {
	return func(r *Runner) {
		r.maxResultBytes = capPositive(maxResultBytes, DefaultMaxResultBytes)
		r.maxCellBytes = capPositive(maxCellBytes, DefaultMaxCellBytes)
		r.maxColumns = capPositive(maxColumns, DefaultMaxColumns)
	}
}

func NewRunner(pool *pgxpool.Pool, validator *Validator, maxRows int, timeout time.Duration, opts ...RunnerOption) *Runner {
	r := &Runner{
		pool:           pool,
		validator:      validator,
		maxRows:        capRowCount(maxRows),
		queryLimit:     timeout,
		maxResultBytes: DefaultMaxResultBytes,
		maxCellBytes:   DefaultMaxCellBytes,
		maxColumns:     DefaultMaxColumns,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// NewRunnerForConnection resolves the read-only pool lazily on each query.
func NewRunnerForConnection(resolver poolResolver, connectionID string, validator *Validator, maxRows int, timeout time.Duration, opts ...RunnerOption) *Runner {
	r := &Runner{
		poolResolver:   resolver,
		connectionID:   connectionID,
		validator:      validator,
		maxRows:        capRowCount(maxRows),
		queryLimit:     timeout,
		maxResultBytes: DefaultMaxResultBytes,
		maxCellBytes:   DefaultMaxCellBytes,
		maxColumns:     DefaultMaxColumns,
	}
	if sr, ok := resolver.(schemaResolver); ok {
		r.schemaResolver = sr
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// MaxRows returns the row cap this runner enforces on Run results.
func (r *Runner) MaxRows() int {
	return r.maxRows
}

// StatsPool returns the analytical pool used for catalog/stats queries on this runner.
func (r *Runner) StatsPool() *pgxpool.Pool {
	return r.activePool(context.Background())
}

// StatsPoolFor returns the analytical pool for the request context.
func (r *Runner) StatsPoolFor(ctx context.Context) *pgxpool.Pool {
	return r.activePool(ctx)
}

// SharesReadOnlyRole reports whether the pool for ctx is one several organizations use. It is true
// unless the resolver can say the organization has a dedicated login, so an unknown case counts as shared.
func (r *Runner) SharesReadOnlyRole(ctx context.Context) bool {
	if sr, ok := r.poolResolver.(sharedRoleResolver); ok && r.pool == nil {
		return sr.SharedReadOnlyRole(ctx, r.connectionID)
	}
	return true
}

func (r *Runner) activePool(ctx context.Context) *pgxpool.Pool {
	if r.pool != nil {
		return r.pool
	}
	if r.poolResolver != nil {
		return r.poolResolver.ReadOnly(ctx, r.connectionID)
	}
	return nil
}

func (r *Runner) activeValidator(ctx context.Context) *Validator {
	if r == nil || r.validator == nil {
		return nil
	}
	if r.schemaResolver == nil {
		return r.validator
	}
	schemas := r.schemaResolver.AllowedSchemas(ctx, r.connectionID)
	if schemas == nil {
		return r.validator
	}
	return r.validator.ValidateForSchemas(schemas)
}

func (r *Runner) queryReadOnlyRows(ctx context.Context, pool *pgxpool.Pool, sql string, args ...any) (pgx.Rows, pgx.Tx, error) {
	if pool == nil {
		return nil, nil, nil
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, nil, err
	}
	if err := r.applyReplay(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		_ = tx.Rollback(ctx)
		return nil, nil, err
	}
	return rows, tx, nil
}

func (r *Runner) Run(ctx context.Context, sql string, limit int) (*Result, error) {
	queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()

	pool := r.activePool(queryCtx)
	if pool == nil {
		return nil, fmt.Errorf("%w: read-only pool unavailable", apperrors.ErrQueryExecutionFailed)
	}
	tx, err := pool.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, executionFailed(err)
	}
	defer func() { _ = tx.Rollback(queryCtx) }()
	if err := r.applyReplay(queryCtx, tx); err != nil {
		return nil, executionFailed(err)
	}

	result, err := r.runRows(queryCtx, tx, sql, limit)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, apperrors.ErrQueryExecutionFailed
	}
	return result, nil
}

// runRows validates sql, wraps it with the row limit, runs it against the
// already-open tx, and reads the result respecting the runner's resource
// limits. It owns neither the transaction's lifecycle (begin/commit/rollback)
// nor replay context — every caller applies those exactly once for however
// many statements share that transaction (see Run and RunSnapshot).
func (r *Runner) runRows(ctx context.Context, tx pgx.Tx, sql string, limit int) (*Result, error) {
	if err := r.activeValidator(ctx).Validate(sql); err != nil {
		return nil, fmt.Errorf("query validation failed: %w", err)
	}

	cleanedSQL, wasExplain, err := ExtractReadOnlySQL(sql)
	if err != nil {
		return nil, fmt.Errorf("query validation failed: %w", err)
	}
	if wasExplain {
		return nil, fmt.Errorf("query validation failed: %w", apperrors.ErrExplainNotRunnable)
	}

	if limit <= 0 || limit > r.maxRows {
		limit = r.maxRows
	}
	rowCap := capRowCount(limit)

	wrappedSQL := fmt.Sprintf("SELECT * FROM (%s) AS pgqn_sub LIMIT $1", cleanedSQL)

	start := time.Now()
	rows, err := tx.Query(ctx, wrappedSQL, rowCap)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s: query exceeded timeout of %v", apperrors.ErrQueryTimeout, r.queryLimit)
		}
		// Only statement-caused errors carry PostgreSQL detail (see executionFailed).
		return nil, executionFailed(err)
	}
	defer rows.Close()

	fieldDescs := rows.FieldDescriptions()
	if r.maxColumns > 0 && len(fieldDescs) > r.maxColumns {
		return nil, fmt.Errorf("%w: result has %d columns, max is %d", apperrors.ErrQueryResultTooLarge, len(fieldDescs), r.maxColumns)
	}
	typeMap := pgtype.NewMap()
	columns := make([]ColumnInfo, len(fieldDescs))
	for i, field := range fieldDescs {
		typeName := fmt.Sprintf("oid:%d", field.DataTypeOID)
		if dt, ok := typeMap.TypeForOID(field.DataTypeOID); ok {
			typeName = dt.Name
		}
		columns[i] = ColumnInfo{
			Name: string(field.Name),
			Type: typeName,
		}
	}

	resultRows := make([][]interface{}, 0, rowCap)
	totalBytes := 0
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("failed to read row values: %w", err)
		}
		rowBytes := 0
		for _, value := range values {
			cellBytes := approximateCellBytes(value)
			if r.maxCellBytes > 0 && cellBytes > r.maxCellBytes {
				return nil, fmt.Errorf("%w: cell size %d exceeds max %d bytes", apperrors.ErrQueryResultTooLarge, cellBytes, r.maxCellBytes)
			}
			rowBytes += cellBytes
		}
		totalBytes += rowBytes
		if r.maxResultBytes > 0 && totalBytes > r.maxResultBytes {
			return nil, fmt.Errorf("%w: result size %d exceeds max %d bytes", apperrors.ErrQueryResultTooLarge, totalBytes, r.maxResultBytes)
		}
		resultRows = append(resultRows, values)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}

	executionTime := time.Since(start)
	debuglog.Log("query executed: %d rows in %s", len(resultRows), executionTime.Round(time.Millisecond))

	return &Result{
		Columns:          columns,
		Rows:             resultRows,
		RowCount:         len(resultRows),
		ExecutionTimeMs:  executionTime.Milliseconds(),
		RowLimitApplied:  rowCap,
		OriginalRowLimit: rowCap,
	}, nil
}

// SnapshotQuery is one statement to run inside RunSnapshot, paired with the
// row limit Run would otherwise take as its second argument.
type SnapshotQuery struct {
	SQL   string
	Limit int
}

// SnapshotResult is one query's outcome from RunSnapshot, in the same
// position as its SnapshotQuery.
type SnapshotResult struct {
	Result *Result
	Err    error
}

// RunSnapshot runs every query against one REPEATABLE READ, read-only
// transaction, so all of them see the exact same, single, consistent
// database state regardless of what any other session commits while this
// method runs. This matters whenever two or more queries must be compared
// against each other: Run alone opens one transaction per call, so two
// separate Run calls can straddle a concurrent write and each observe a
// different snapshot — e.g. comparing SELECT n and SELECT n-1 against a row
// that changes from n=1 to n=2 between the two reads can make every
// snapshot's true values (1 vs 0, or 2 vs 1) look equal (1 vs 1) by
// accident. Holding one REPEATABLE READ transaction open for every read
// closes that gap.
//
// Every query gets its own fresh per-statement timeout (the same r.queryLimit
// budget Run enforces) and goes through the same validation and resource
// limits Run does; replay context is applied once for the whole transaction,
// matching SET LOCAL's own scope (it would be redundant, not more correct, to
// re-apply it per statement).
//
// PostgreSQL aborts an entire transaction on any statement error, so once one
// query fails, no later query in queries is attempted — the returned slice is
// always the same length as queries, with a nil Result and a non-nil Err at
// the first failing index and every index after it (their real errors, had
// they run alone, are unknowable once the transaction is poisoned).
func (r *Runner) RunSnapshot(ctx context.Context, queries []SnapshotQuery) []SnapshotResult {
	out := make([]SnapshotResult, len(queries))
	fail := func(err error) []SnapshotResult {
		for i := range out {
			out[i].Err = err
		}
		return out
	}

	pool := r.activePool(ctx)
	if pool == nil {
		return fail(fmt.Errorf("%w: read-only pool unavailable", apperrors.ErrQueryExecutionFailed))
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return fail(executionFailed(err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := r.applyReplay(ctx, tx); err != nil {
		return fail(executionFailed(err))
	}

	for i, q := range queries {
		queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
		out[i].Result, out[i].Err = r.runRows(queryCtx, tx, q.SQL, q.Limit)
		cancel()
		if out[i].Err != nil {
			wrapped := fmt.Errorf("not run: an earlier query in the same snapshot failed: %w", out[i].Err)
			for j := i + 1; j < len(out); j++ {
				out[j].Err = wrapped
			}
			return out
		}
	}
	if err := tx.Commit(ctx); err != nil {
		// The transaction was read-only: every row above was already read
		// under the snapshot it held, and a failed COMMIT does not change
		// what was read. Nothing left to roll back into inconsistency.
		debuglog.Log("snapshot commit failed (read-only, results already valid): %v", err)
	}
	return out
}

// ValidateQuery checks SQL safety without executing it.
func (r *Runner) ValidateQuery(sql string) error {
	return r.activeValidator(context.Background()).Validate(sql)
}

// ValidateQueryWithContext checks SQL safety using request-scoped connection policy.
func (r *Runner) ValidateQueryWithContext(ctx context.Context, sql string) error {
	return r.activeValidator(ctx).Validate(sql)
}

// QueryTimeout returns the per-query execution timeout configured for this runner.
func (r *Runner) QueryTimeout() time.Duration {
	return r.queryLimit
}

func approximateCellBytes(value interface{}) int {
	if value == nil {
		return 0
	}
	switch v := value.(type) {
	case string:
		return len(v)
	case []byte:
		return len(v)
	case fmt.Stringer:
		return len(v.String())
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Array, reflect.Slice, reflect.Map:
		if rv.Len() > 0 {
			return len(fmt.Sprint(value))
		}
		return 0
	default:
		return len(fmt.Sprint(value))
	}
}
