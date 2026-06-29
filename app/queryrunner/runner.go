package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"strings"
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

type Runner struct {
	pool                *pgxpool.Pool
	validator           *Validator
	maxRows             int
	queryLimit          time.Duration
	allowExplainAnalyze bool
}

// RunnerOption configures optional Runner behavior.
type RunnerOption func(*Runner)

// WithExplainAnalyze sets whether EXPLAIN ANALYZE (query execution) is permitted.
func WithExplainAnalyze(enabled bool) RunnerOption {
	return func(r *Runner) {
		r.allowExplainAnalyze = enabled
	}
}

func NewRunner(pool *pgxpool.Pool, validator *Validator, maxRows int, timeout time.Duration, opts ...RunnerOption) *Runner {
	r := &Runner{
		pool:       pool,
		validator:  validator,
		maxRows:    maxRows,
		queryLimit: timeout,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

func (r *Runner) Run(ctx context.Context, sql string, limit int) (*Result, error) {
	if err := r.ValidateQuery(sql); err != nil {
		return nil, fmt.Errorf("query validation failed: %w", err)
	}

	cleanedSQL := strings.TrimSpace(sql)
	cleanedSQL = strings.TrimSuffix(cleanedSQL, ";")
	cleanedSQL = strings.TrimSpace(cleanedSQL)

	if limit <= 0 || limit > r.maxRows {
		limit = r.maxRows
	}

	queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()

	wrappedSQL := fmt.Sprintf("SELECT * FROM (%s) AS pgqn_sub LIMIT $1", cleanedSQL)

	start := time.Now()
	rows, err := r.pool.Query(queryCtx, wrappedSQL, limit)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s: query exceeded timeout of %v", apperrors.ErrQueryTimeout, r.queryLimit)
		}
		if strings.Contains(err.Error(), "connection") || strings.Contains(err.Error(), "network") {
			return nil, fmt.Errorf("%w: database connection error - %v", apperrors.ErrQueryExecutionFailed, err)
		}
		return nil, fmt.Errorf("%w: %v", apperrors.ErrQueryExecutionFailed, err)
	}
	defer rows.Close()

	fieldDescs := rows.FieldDescriptions()
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

	resultRows := make([][]interface{}, 0, limit)
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("failed to read row values: %w", err)
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
		RowLimitApplied:  limit,
		OriginalRowLimit: limit,
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
