package queryrunner

import "testing"

// A parameter anywhere in the statement makes it parameterized: EXPLAIN must
// then use GENERIC_PLAN, and substitution must leave none behind.
func TestQueryHasPositionalParamsInEveryClause(t *testing.T) {
	for _, sql := range []string{
		`SELECT id FROM api.big ORDER BY v LIMIT $1 OFFSET $2`,
		`WITH s AS (SELECT id FROM api.big ORDER BY v LIMIT $1) SELECT count(*) FROM s`,
		`SELECT $1::int + id FROM api.big`,
		`SELECT a.id FROM api.a a JOIN api.b b ON b.id = a.id AND b.k = $1`,
		`SELECT * FROM api.big_orders(min_total := $1::numeric)`,
		`SELECT id FROM api.big ORDER BY abs(id - $1)`,
		`SELECT v, count(*) FROM api.big GROUP BY v HAVING count(*) > $1`,
		`SELECT CASE WHEN id > $1 THEN 1 END FROM api.big`,
		`SELECT id FROM api.big WHERE id = ANY($1)`,
	} {
		if !queryHasPositionalParams(sql) {
			t.Errorf("parameter not detected: %s", sql)
		}
	}
	for _, sql := range []string{
		`SELECT '$1' AS literal FROM api.big`,
		`SELECT "col$1" FROM api.big`,
		`SELECT id FROM api.big WHERE id = 1`,
	} {
		if queryHasPositionalParams(sql) {
			t.Errorf("text that is not a parameter detected as one: %s", sql)
		}
	}
}
