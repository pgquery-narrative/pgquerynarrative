package queryrunner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// StatStatements queries pg_stat_statements using the app pool (pg_read_all_stats) and
// filters to statements executed by filterRole when non-empty.
func StatStatements(ctx context.Context, statsPool *pgxpool.Pool, filterRole, orderBy string, limit int, timeout time.Duration) (*StatStatementsResult, error) {
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

	roleFilter := ""
	args := []any{limit}
	if strings.TrimSpace(filterRole) != "" {
		roleFilter = " AND userid = (SELECT oid FROM pg_roles WHERE rolname = $2)"
		args = append(args, strings.TrimSpace(filterRole))
	}

	sql := fmt.Sprintf(`
SELECT
  queryid::text,
  LEFT(query, 500) AS query,
  calls,
  ROUND(total_exec_time::numeric, 3)::float8 AS total_time_ms,
  ROUND(mean_exec_time::numeric, 3)::float8 AS mean_time_ms,
  rows
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())%s
ORDER BY %s DESC
LIMIT $1`, roleFilter, col)

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
	return StatStatements(ctx, r.pool, "", orderBy, limit, r.queryLimit)
}
