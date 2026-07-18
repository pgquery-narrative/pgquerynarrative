package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestIsUniqueViolation covers the report-reuse race path: when two workers concurrently
// try to store the first report for the same schedule_run_id, the partial unique index on
// app.reports(schedule_run_id) rejects the loser with a 23505 error, and storeReport must
// recognize that specific error so it can fall back to reading the winner's report_id
// instead of surfacing a spurious failure.
func TestIsUniqueViolation(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil error is not a unique violation",
			err:  nil,
			want: false,
		},
		{
			name: "pgError 23505 is a unique violation",
			err:  &pgconn.PgError{Code: "23505", ConstraintName: "idx_reports_schedule_run_unique"},
			want: true,
		},
		{
			name: "wrapped pgError 23505 is still detected via errors.As",
			err:  fmt.Errorf("insert report: %w", &pgconn.PgError{Code: "23505"}),
			want: true,
		},
		{
			name: "pgError with a different code is not a unique violation",
			err:  &pgconn.PgError{Code: "23503"}, // foreign_key_violation
			want: false,
		},
		{
			name: "generic non-pg error is not a unique violation",
			err:  errors.New("connection reset"),
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUniqueViolation(tt.err); got != tt.want {
				t.Errorf("isUniqueViolation(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
