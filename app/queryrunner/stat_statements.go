package queryrunner

import (
	"context"
	"fmt"
	"strings"

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

// StatStatements returns top queries from pg_stat_statements for the current database.
func (r *Runner) StatStatements(ctx context.Context, orderBy string, limit int) (*StatStatementsResult, error) {
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

	queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()

	sql := fmt.Sprintf(`
SELECT
  queryid::text,
  LEFT(query, 500) AS query,
  calls,
  ROUND(total_exec_time::numeric, 3)::float8 AS total_time_ms,
  ROUND(mean_exec_time::numeric, 3)::float8 AS mean_time_ms,
  rows
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY %s DESC
LIMIT $1`, col)

	rows, err := r.pool.Query(queryCtx, sql, limit)
	if err != nil {
		if strings.Contains(err.Error(), "pg_stat_statements") {
			return nil, fmt.Errorf("%w: %v", apperrors.ErrStatStatementsUnavailable, err)
		}
		if strings.Contains(err.Error(), "permission denied") {
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
