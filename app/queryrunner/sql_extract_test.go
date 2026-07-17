package queryrunner

import (
	"errors"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

func TestExtractReadOnlySQL(t *testing.T) {
	tests := []struct {
		name        string
		sql         string
		want        string
		wantExplain bool
		wantErr     error
	}{
		{
			name: "bare select",
			sql:  "SELECT 1 FROM demo.sales",
			want: "SELECT 1 FROM demo.sales",
		},
		{
			name: "trailing semicolon",
			sql:  "SELECT 1 FROM demo.sales;",
			want: "SELECT 1 FROM demo.sales",
		},
		{
			name:        "explain wrapper",
			sql:         "EXPLAIN SELECT 1 FROM demo.sales",
			want:        "SELECT 1 FROM demo.sales",
			wantExplain: true,
		},
		{
			name:        "explain format json wrapper",
			sql:         "EXPLAIN (FORMAT JSON) SELECT 1 FROM demo.sales",
			want:        "SELECT 1 FROM demo.sales",
			wantExplain: true,
		},
		{
			name: "with cte",
			sql:  "WITH cte AS (SELECT 1) SELECT * FROM cte",
			want: "WITH cte AS (SELECT 1) SELECT * FROM cte",
		},
		{
			name:        "explain with cte",
			sql:         "EXPLAIN WITH cte AS (SELECT 1) SELECT * FROM cte",
			want:        "WITH cte AS (SELECT 1) SELECT * FROM cte",
			wantExplain: true,
		},
		{
			name:        "string literal containing select is not sliced",
			sql:         "EXPLAIN SELECT 'select me' AS c FROM demo.sales",
			want:        "SELECT 'select me' AS c FROM demo.sales",
			wantExplain: true,
		},
		{
			name:        "explain analyze rejected",
			sql:         "EXPLAIN (ANALYZE) SELECT 1 FROM demo.sales",
			wantExplain: true,
			wantErr:     apperrors.ErrExplainOptionsNotAllowed,
		},
		{
			name:        "explain buffers rejected",
			sql:         "EXPLAIN (BUFFERS) SELECT 1 FROM demo.sales",
			wantExplain: true,
			wantErr:     apperrors.ErrExplainOptionsNotAllowed,
		},
		{
			name:        "explain format text rejected",
			sql:         "EXPLAIN (FORMAT TEXT) SELECT 1 FROM demo.sales",
			wantExplain: true,
			wantErr:     apperrors.ErrExplainOptionsNotAllowed,
		},
		{
			name:        "explain verbose rejected",
			sql:         "EXPLAIN (VERBOSE) SELECT 1 FROM demo.sales",
			wantExplain: true,
			wantErr:     apperrors.ErrExplainOptionsNotAllowed,
		},
		{
			name:        "explain of insert rejected",
			sql:         "EXPLAIN INSERT INTO demo.sales VALUES (1)",
			wantExplain: true,
			wantErr:     apperrors.ErrOnlySelectAllowed,
		},
		{
			name:    "insert rejected",
			sql:     "INSERT INTO demo.sales VALUES (1)",
			wantErr: apperrors.ErrOnlySelectAllowed,
		},
		{
			name:    "empty rejected",
			sql:     "   ",
			wantErr: apperrors.ErrOnlySelectAllowed,
		},
		{
			name:    "multiple statements rejected",
			sql:     "SELECT 1; SELECT 2",
			wantErr: apperrors.ErrMultipleStatements,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, wasExplain, err := ExtractReadOnlySQL(tt.sql)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("expected error %v, got %v", tt.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if wasExplain != tt.wantExplain {
				t.Fatalf("wasExplain: got %v want %v", wasExplain, tt.wantExplain)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func FuzzExtractReadOnlySQL(f *testing.F) {
	seeds := []string{
		"SELECT 1 FROM demo.sales",
		"EXPLAIN SELECT 1 FROM demo.sales",
		"EXPLAIN (FORMAT JSON) SELECT * FROM demo.sales WHERE region = 'North'",
		"WITH cte AS (SELECT 1) SELECT * FROM cte",
		"INSERT INTO demo.sales VALUES (1)",
		"EXPLAIN (ANALYZE, BUFFERS) SELECT 1",
		"SELECT 1; DROP TABLE demo.sales",
		"select 'explain select 1'",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		inner, _, err := ExtractReadOnlySQL(sql)
		if err != nil {
			return
		}
		// Whatever comes out must still be a single read-only statement.
		if _, _, err2 := ExtractReadOnlySQL(inner); err2 != nil {
			t.Fatalf("extracted SQL %q not re-extractable: %v", inner, err2)
		}
	})
}
