package narrative

import (
	"context"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	apperrors "github.com/pgquerynarrative/pgquerynarrative/internal/errors"
)

// ErrEmptyQuery is an alias kept for backward compatibility; prefer apperrors.ErrEmptyQuery.
var ErrEmptyQuery = apperrors.ErrEmptyQuery

func validateQueryInput(sql string) error {
	if strings.TrimSpace(sql) == "" {
		return apperrors.ErrEmptyQuery
	}
	return nil
}

// RunQuery executes a read-only SQL query and returns columns, rows, and
// metadata (chart suggestions, period comparison when applicable). Context
// cancellation is propagated to the execution. The result is the same type
// as the API run endpoint. limit is the maximum number of rows to return;
// if <= 0, DefaultRunQueryLimit is used.
func (c *Client) RunQuery(ctx context.Context, sql string, limit int) (*queries.RunQueryResult, error) {
	if err := validateQueryInput(sql); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultRunQueryLimit
	}
	payload := &queries.RunQueryPayload{
		SQL:   sql,
		Limit: int32(limit),
	}
	return c.queriesService.Run(ctx, payload)
}

// RunQueryWithOptions executes a read-only SQL query with the given options.
// If opts is nil or opts.Limit <= 0, DefaultRunQueryLimit is used.
func (c *Client) RunQueryWithOptions(ctx context.Context, sql string, opts *RunQueryOptions) (*queries.RunQueryResult, error) {
	limit := DefaultRunQueryLimit
	if opts != nil && opts.Limit > 0 {
		limit = opts.Limit
	}
	return c.RunQuery(ctx, sql, limit)
}
