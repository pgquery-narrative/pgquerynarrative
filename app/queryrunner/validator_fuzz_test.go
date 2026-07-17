package queryrunner

import (
	"strings"
	"testing"
)

// FuzzValidatorReadOnly asserts the validator never panics and never accepts
// obvious write statements, regardless of input shape.
func FuzzValidatorReadOnly(f *testing.F) {
	seeds := []string{
		"SELECT * FROM demo.sales",
		"WITH cte AS (SELECT 1) SELECT * FROM cte",
		"EXPLAIN (FORMAT JSON) SELECT * FROM demo.sales",
		"INSERT INTO demo.sales VALUES (1)",
		"UPDATE demo.sales SET amount = 0",
		"DELETE FROM demo.sales",
		"DROP TABLE demo.sales",
		"SELECT * FROM demo.sales; DROP TABLE demo.sales",
		"EXPLAIN (ANALYZE) DELETE FROM demo.sales",
		"WITH x AS (INSERT INTO demo.sales VALUES (1) RETURNING *) SELECT * FROM x",
		"SELECT 'insert into t values (1)'",
		"COPY demo.sales TO '/tmp/out'",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	v := NewValidator([]string{"demo"}, 10000)
	writeVerbs := []string{"insert into", "update ", "delete from", "drop ", "truncate ", "alter ", "create table", "grant "}

	f.Fuzz(func(t *testing.T, sql string) {
		err := v.Validate(sql) // must never panic
		if err != nil {
			return
		}
		// Accepted queries must not start with a write verb.
		lower := strings.ToLower(strings.TrimSpace(sql))
		for _, verb := range writeVerbs {
			if strings.HasPrefix(lower, verb) {
				t.Fatalf("validator accepted write statement: %q", sql)
			}
		}
	})
}
