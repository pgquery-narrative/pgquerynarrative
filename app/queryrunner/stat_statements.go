package queryrunner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// StatStatementRow is one row from pg_stat_statements for the current database.
type StatStatementRow struct {
	QueryID     string
	Query       string
	Calls       int64
	TotalTimeMs float64
	MeanTimeMs  float64
	Rows        int64
}

// StatStatementsResult holds top-N statement stats.
type StatStatementsResult struct {
	Items   []StatStatementRow
	OrderBy string
	Limit   int
}

var statStatementsOrderColumns = map[string]string{
	"total_time": "total_exec_time",
	"mean_time":  "mean_exec_time",
	"calls":      "calls",
}

// statsQuerier is satisfied by *pgxpool.Pool and db.OrgScoped.
type statsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const (
	// StatStatementsQueryMaxLen is the max characters returned for a pg_stat_statements
	// query text. Aligns with CreateInvestigation SQL MaxLength (10000).
	StatStatementsQueryMaxLen = 10000
)

// statStatementsSchema resolves the schema pg_stat_statements was installed
// into. pg_extension/pg_namespace are catalog tables, always visible via the
// implicit pg_catalog search path entry regardless of the caller's search_path.
func statStatementsSchema(ctx context.Context, statsPool statsQuerier) (string, error) {
	rows, err := statsPool.Query(ctx, `
		SELECT n.nspname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'pg_stat_statements'
	`)
	if err != nil {
		return "", fmt.Errorf("pg_stat_statements schema lookup: %w", err)
	}
	defer rows.Close()

	var schema string
	if rows.Next() {
		if err := rows.Scan(&schema); err != nil {
			return "", fmt.Errorf("scan pg_stat_statements schema: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if schema == "" {
		return "", fmt.Errorf("%w: extension not installed", apperrors.ErrStatStatementsUnavailable)
	}
	return schema, nil
}

// StatStatements queries pg_stat_statements on the selected analytical connection
// and filters to statements executed by filterRole when non-empty.
func StatStatements(ctx context.Context, statsPool statsQuerier, filterRole, orderBy string, limit int, timeout time.Duration) (*StatStatementsResult, error) {
	if statsPool == nil {
		return nil, fmt.Errorf("%w: stats pool not configured", apperrors.ErrStatStatementsUnavailable)
	}

	col, ok := statStatementsOrderColumns[strings.ToLower(strings.TrimSpace(orderBy))]
	if !ok {
		return nil, fmt.Errorf("%w: order_by must be total_time, mean_time, or calls", apperrors.ErrInvalidStatStatementsOrder)
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}

	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// pg_stat_statements lives wherever CREATE EXTENSION put it (public on stock
	// installs). The analytical role's search_path is deliberately locked to its
	// allowed data schemas (e.g. "demo" or "demo, opendata") and does not include
	// public, so an unqualified reference here resolves to nothing and every call
	// fails with "relation pg_stat_statements does not exist" even though the
	// view exists and the role can read it. Resolve and schema-qualify instead,
	// the same way hypopgSchema does for hypopg.
	schema, err := statStatementsSchema(queryCtx, statsPool)
	if err != nil {
		return nil, err
	}
	qualified := pgx.Identifier{schema}.Sanitize()

	roleFilter := ""
	args := []any{limit}
	if strings.TrimSpace(filterRole) != "" {
		roleFilter = " AND userid = (SELECT oid FROM pg_roles WHERE rolname = $2)"
		args = append(args, strings.TrimSpace(filterRole))
	}

	sql := fmt.Sprintf(`
SELECT
  queryid::text,
  LEFT(query, %d) AS query,
  calls,
  ROUND(total_exec_time::numeric, 3)::float8 AS total_time_ms,
  ROUND(mean_exec_time::numeric, 3)::float8 AS mean_time_ms,
  rows
FROM %s.pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())%s
ORDER BY %s DESC
LIMIT $1`, StatStatementsQueryMaxLen, qualified, roleFilter, col)

	rows, err := statsPool.Query(queryCtx, sql, args...)
	if err != nil {
		if strings.Contains(err.Error(), "pg_stat_statements") || strings.Contains(err.Error(), "permission denied") {
			return nil, fmt.Errorf("%w: %v", apperrors.ErrStatStatementsUnavailable, err)
		}
		return nil, fmt.Errorf("pg_stat_statements query failed: %w", err)
	}
	defer rows.Close()

	items := make([]StatStatementRow, 0, limit)
	for rows.Next() {
		var row StatStatementRow
		if err := rows.Scan(&row.QueryID, &row.Query, &row.Calls, &row.TotalTimeMs, &row.MeanTimeMs, &row.Rows); err != nil {
			return nil, fmt.Errorf("scan pg_stat_statements row: %w", err)
		}
		items = append(items, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return &StatStatementsResult{
		Items:   items,
		OrderBy: strings.ToLower(strings.TrimSpace(orderBy)),
		Limit:   limit,
	}, nil
}

// StatStatements on Runner delegates to StatStatements using the runner pool (legacy/tests).
func (r *Runner) StatStatements(ctx context.Context, orderBy string, limit int) (*StatStatementsResult, error) {
	return StatStatements(ctx, r.activePool(ctx), "", orderBy, limit, r.queryLimit)
}
