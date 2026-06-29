package queryrunner_test

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/errors"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

func TestValidator(t *testing.T) {
	validator := queryrunner.NewValidator([]string{"demo"}, 10000)

	tests := []struct {
		name    string
		sql     string
		wantErr error
	}{
		{"valid_select", "SELECT * FROM demo.sales", nil},
		{"valid_with", "WITH cte AS (SELECT * FROM demo.sales) SELECT * FROM cte", nil},
		{"identifier_created_at", "SELECT created_at FROM demo.sales", nil},
		{"identifier_updated_at", "SELECT updated_at, created_by FROM demo.sales", nil},
		{"identifier_alter_substring", "SELECT alteration, analyze_flag, copy_count FROM demo.sales", nil},
		{"mixed_case_select", "SeLeCt * FrOm demo.sales", nil},
		{"block_comment_dangerous_keywords", "SELECT /* DROP TABLE demo.sales; DELETE FROM demo.sales */ id FROM demo.sales", nil},
		{"line_comment_dangerous_keywords", "-- DELETE FROM demo.sales\nSELECT id FROM demo.sales", nil},
		{"dollar_quote_literal_create", "SELECT $$create table x$$ AS msg FROM demo.sales LIMIT 1", nil},
		{"tagged_dollar_quote_alter", "SELECT $tag$alter table demo.sales$tag$ AS msg FROM demo.sales LIMIT 1", nil},
		{"dollar_quote_in_cte", "WITH literals AS (SELECT $q$copy from stdin$q$ AS payload FROM demo.sales LIMIT 1) SELECT payload FROM literals", nil},
		{"mixed_case_explain", "ExPlAiN (FoRmAt JsOn) SeLeCt 1 FROM demo.sales", nil},
		{"explain_select", "EXPLAIN SELECT * FROM demo.sales", nil},
		{"explain_format_json", "EXPLAIN (FORMAT JSON) SELECT product_category, SUM(total_amount) FROM demo.sales GROUP BY product_category", nil},
		{"explain_format_json_buffers", "EXPLAIN (FORMAT JSON, BUFFERS) SELECT * FROM demo.sales WHERE date >= CURRENT_DATE - INTERVAL '30 days'", nil},
		{"explain_non_select", "EXPLAIN DELETE FROM demo.sales", errors.ErrOnlySelectAllowed},
		{"explain_drop", "EXPLAIN DROP TABLE demo.sales", errors.ErrOnlySelectAllowed},
		{"empty_query", "", errors.ErrOnlySelectAllowed},
		{"whitespace_only", "   \n\t  ", errors.ErrOnlySelectAllowed},
		{"too_long", "SELECT * FROM demo.sales WHERE '" + strings.Repeat("a", 20000) + "' = 'x'", errors.ErrQueryTooLong},
		{"non_select", "UPDATE demo.sales SET quantity = 1", errors.ErrOnlySelectAllowed},
		{"mixed_case_delete", "DeLeTe FrOm demo.sales", errors.ErrOnlySelectAllowed},
		{"mixed_case_explain_delete", "ExPlAiN DeLeTe FrOm demo.sales", errors.ErrOnlySelectAllowed},
		{"write_inside_cte_with_comment", "WITH changed AS (/* read-only */ DELETE FROM demo.sales WHERE quantity = 0 RETURNING id) SELECT * FROM changed", errors.ErrDisallowedKeyword},
		{"insert_not_select", "INSERT INTO demo.sales (id) SELECT id FROM demo.sales", errors.ErrOnlySelectAllowed},
		{"disallowed_keyword_drop", "SELECT * FROM demo.sales; DROP TABLE demo.sales", errors.ErrMultipleStatements},
		{"write_inside_cte", "WITH changed AS (DELETE FROM demo.sales WHERE quantity = 0 RETURNING id) SELECT * FROM changed", errors.ErrDisallowedKeyword},
		{"schema_not_allowed", "SELECT * FROM public.users", errors.ErrSchemaNotAllowed},
		{"unqualified_table", "SELECT * FROM sales", errors.ErrUnqualifiedTable},
		{"schema_allowed_in_join", "SELECT * FROM demo.sales s JOIN demo.inventory i ON i.id = s.id", nil},
		{"semicolon_in_string_literal", "SELECT ';' AS literal_value", nil},
		{"alias_column_allowed", "WITH cte AS (SELECT region AS r FROM demo.sales) SELECT c.r FROM cte c", nil},
		{"complex_cte_with_aliases", `WITH regional_totals AS (SELECT region, product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE date >= CURRENT_DATE - INTERVAL '365 days' GROUP BY region, product_category),
  regional_ranked AS (SELECT region, product_category, revenue, RANK() OVER (PARTITION BY region ORDER BY revenue DESC) AS cat_rank_in_region FROM regional_totals),
  sales_rep_perf AS (SELECT sales_rep, region, SUM(total_amount) AS rep_revenue FROM demo.sales GROUP BY sales_rep, region)
SELECT rr.region, rr.revenue, sr.rep_revenue
FROM regional_ranked rr
LEFT JOIN LATERAL (SELECT sales_rep, rep_revenue FROM sales_rep_perf WHERE sales_rep_perf.region = rr.region ORDER BY rep_revenue DESC LIMIT 1) sr ON true
WHERE rr.cat_rank_in_region <= 3`, nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.Validate(tt.sql)
			if tt.wantErr == nil && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.wantErr != nil && err == nil {
				t.Fatalf("expected error %v", tt.wantErr)
			}
			if tt.wantErr != nil && err != nil && err != tt.wantErr {
				t.Fatalf("expected error %v, got %v", tt.wantErr, err)
			}
		})
	}
}
