package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/debuglog"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
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

type Runner struct {
	pool                *pgxpool.Pool
	poolResolver        poolResolver
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
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// StatsPool returns the analytical pool used for catalog/stats queries on this runner.
func (r *Runner) StatsPool() *pgxpool.Pool {
	return r.activePool(context.Background())
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

func (r *Runner) Run(ctx context.Context, sql string, limit int) (*Result, error) {
	if err := r.ValidateQuery(sql); err != nil {
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

	queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()

	wrappedSQL := fmt.Sprintf("SELECT * FROM (%s) AS pgqn_sub LIMIT $1", cleanedSQL)

	start := time.Now()
	pool := r.activePool(queryCtx)
	if pool == nil {
		return nil, fmt.Errorf("%w: read-only pool unavailable", apperrors.ErrQueryExecutionFailed)
	}
	rows, err := pool.Query(queryCtx, wrappedSQL, rowCap)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s: query exceeded timeout of %v", apperrors.ErrQueryTimeout, r.queryLimit)
		}
		// Do not embed driver/Postgres detail in the returned error (relation names, SQLSTATE).
		return nil, apperrors.ErrQueryExecutionFailed
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

// ValidateQuery checks SQL safety without executing it.
func (r *Runner) ValidateQuery(sql string) error {
	return r.validator.Validate(sql)
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
