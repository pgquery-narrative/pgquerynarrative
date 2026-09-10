package queryrunner

import (
	"errors"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// A read-only transaction stops writes but not every side effect. These cases
// pin the application-level policy that closes the gap.
func TestValidator_FunctionPolicy(t *testing.T) {
	v := NewValidator([]string{"demo"}, 100000)

	denied := []struct {
		sql  string
		want error
	}{
		// Session advisory locks outlive COMMIT on a pooled connection.
		{"SELECT pg_advisory_lock(1)", apperrors.ErrFunctionNotAllowed},
		{"SELECT pg_try_advisory_lock(1)", apperrors.ErrFunctionNotAllowed},
		{"SELECT pg_catalog.pg_advisory_lock(1)", apperrors.ErrFunctionNotAllowed},
		{"SELECT pg_advisory_xact_lock(1)", apperrors.ErrFunctionNotAllowed},
		// Session GUC mutation on a pooled connection.
		{"SELECT set_config('work_mem', '1GB', false)", apperrors.ErrFunctionNotAllowed},
		// Connection holding.
		{"SELECT pg_sleep(10)", apperrors.ErrFunctionNotAllowed},
		// Server-side file access.
		{"SELECT pg_read_file('/etc/passwd')", apperrors.ErrFunctionNotAllowed},
		{"SELECT pg_ls_dir('/')", apperrors.ErrFunctionNotAllowed},
		// Sequence mutation.
		{"SELECT nextval('demo.s')", apperrors.ErrFunctionNotAllowed},
		// Backend control.
		{"SELECT pg_terminate_backend(1)", apperrors.ErrFunctionNotAllowed},
		// Executes a second, unvalidated query.
		{"SELECT query_to_xml('SELECT 1', false, false, '')", apperrors.ErrFunctionNotAllowed},
		// External systems.
		{"SELECT dblink('host=x', 'SELECT 1')", apperrors.ErrFunctionNotAllowed},
		{"SELECT pg_notify('c', 'p')", apperrors.ErrFunctionNotAllowed},
		// Nested in a subquery / CTE, not just the target list.
		{"SELECT * FROM demo.sales WHERE id = (SELECT pg_advisory_lock(1))", apperrors.ErrFunctionNotAllowed},
		{"WITH c AS (SELECT pg_sleep(1)) SELECT * FROM c", apperrors.ErrFunctionNotAllowed},
		// Function in a schema outside the allowlist.
		{"SELECT other_schema.some_func(1)", apperrors.ErrFunctionSchemaNotAllowed},
		// Operators and types are backed by functions in the same schema, so a
		// qualified reference to either reaches code the allowlist excludes
		// without ever producing a FuncCall node.
		{"SELECT 1 OPERATOR(other_schema.+) 2", apperrors.ErrSchemaNotAllowed},
		{"SELECT id::other_schema.mytype FROM demo.sales", apperrors.ErrSchemaNotAllowed},
		{"SELECT CAST(id AS other_schema.mytype) FROM demo.sales", apperrors.ErrSchemaNotAllowed},
		{"SELECT * FROM demo.sales WHERE id OPERATOR(other_schema.=) 1", apperrors.ErrSchemaNotAllowed},
		// Statement shapes that are not read-only.
		{"SELECT * INTO demo.t2 FROM demo.sales", apperrors.ErrSelectIntoNotAllowed},
		{"SELECT * FROM demo.sales FOR UPDATE", apperrors.ErrLockingClauseNotAllowed},
		{"SELECT * FROM demo.sales FOR SHARE", apperrors.ErrLockingClauseNotAllowed},
	}
	for _, tc := range denied {
		got := v.Validate(tc.sql)
		if !errors.Is(got, tc.want) {
			t.Errorf("Validate(%q) = %v, want %v", tc.sql, got, tc.want)
		}
	}

	// Ordinary analytical SQL must keep working.
	allowed := []string{
		"SELECT count(*) FROM demo.sales",
		"SELECT sum(amount), avg(amount) FROM demo.sales",
		"SELECT now(), current_date FROM demo.sales",
		"SELECT coalesce(region, 'x') FROM demo.sales",
		"SELECT date_trunc('month', date) FROM demo.sales",
		"SELECT pg_catalog.count(*) FROM demo.sales",
		"SELECT demo.my_helper(1) FROM demo.sales",
		"SELECT to_char(date, 'YYYY-MM') FROM demo.sales",
		"SELECT row_number() OVER (ORDER BY id) FROM demo.sales",
		// Built-in casts parse as pg_catalog-qualified type names, and ordinary
		// operators are unqualified — neither may be caught by the rule above.
		"SELECT id::int, amount::numeric, name::text FROM demo.sales",
		"SELECT CAST(id AS bigint) FROM demo.sales",
		"SELECT * FROM demo.sales WHERE amount > 10 AND region = 'North'",
		"SELECT id::demo.mytype FROM demo.sales",
	}
	for _, sql := range allowed {
		if err := v.Validate(sql); err != nil {
			t.Errorf("Validate(%q) = %v, want nil", sql, err)
		}
	}
}

// The equivalence fingerprint wrapper is itself run through the validator, so
// the policy must not break it.
func TestValidator_AllowsEquivalenceFingerprintWrapper(t *testing.T) {
	v := NewValidator([]string{"demo"}, 100000)
	sql := `SELECT count(*)::bigint AS pgqn_n,
       coalesce(sum(hashtextextended(pgqn_eq::text, 0)::numeric), 0)::text AS pgqn_s,
       coalesce(bit_xor(hashtextextended(pgqn_eq::text, 0)), 0)::bigint AS pgqn_x
FROM (SELECT * FROM demo.sales) AS pgqn_eq`
	if err := v.Validate(sql); err != nil {
		t.Fatalf("fingerprint wrapper must validate, got: %v", err)
	}
}

// The function/operator/type policy is a denylist plus a schema rule applied to
// every user SELECT, so its most likely failure mode is not a missed attack but
// a false positive that breaks legitimate analytical SQL. This sweep pins the
// shapes that must never be rejected — extension types and operators in
// particular, which users write unqualified and which no allowlist knows about.
func TestValidator_NoFalsePositivesOnAnalyticalSQL(t *testing.T) {
	v := NewValidator([]string{"demo"}, 100000)
	for _, sql := range []string{
		`SELECT embedding::vector FROM demo.docs`,      // pgvector
		`SELECT geom::geography FROM demo.places`,      // postgis
		`SELECT name::citext FROM demo.users`,          // citext
		`SELECT data->>'k' FROM demo.events`,           // jsonb operators
		`SELECT a <-> b FROM demo.docs`,                // pgvector distance
		`SELECT tsv @@ to_tsquery('x') FROM demo.docs`, // full-text search
		`SELECT array_agg(x ORDER BY y) FROM demo.t`,   // ordered aggregate
		`SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY x) FROM demo.t`,
		`SELECT generate_series(1,10)`, // set-returning
		`SELECT jsonb_array_elements(data) FROM demo.events`,
		`SELECT now() - interval '1 day'`,
		`SELECT string_agg(name, ',') FROM demo.users`,
		`SELECT demo.sales.total_amount::numeric(10,2) FROM demo.sales`,
		`SELECT * FROM demo.sales WHERE date BETWEEN '2025-01-01' AND '2025-02-01'`,
		`SELECT count(*) FILTER (WHERE region = 'North') FROM demo.sales`,
		`SELECT lag(x) OVER (PARTITION BY y ORDER BY z) FROM demo.t`,
	} {
		if err := v.Validate(sql); err != nil {
			t.Errorf("legitimate analytical SQL must not be rejected:\n  %s\n  -> %v", sql, err)
		}
	}
}
