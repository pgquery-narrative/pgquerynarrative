package integration

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
)

// TestColumnCastRewriteMatchesRealCatalogTypes is #200's truth-table check:
// a col::<type> = const predicate is rewritten to drop the cast only when
// the catalog says the column's own real type already equals the cast's
// target — and when it is rewritten, the rewrite must return the exact same
// rows as the original against real stored data, for every supported type
// pair. It also pins the #197 danger cases (a numeric column cast to a
// different numeric type, a cast across the text/varchar family) as still
// declined even though a real resolver is now available — catalog access
// alone is not enough; the resolved type must equal the cast's target.
func TestColumnCastRewriteMatchesRealCatalogTypes(t *testing.T) {
	ctx := context.Background()
	pool, _ := pilotPostgres(t, ctx)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS castrw;
		CREATE TABLE castrw.t (
			id int PRIMARY KEY,
			amount numeric(10,2),
			qty integer,
			big bigint,
			code text,
			label varchar(20)
		);
		INSERT INTO castrw.t VALUES
			(1, 1.40, 5, 9000000000, 'abc', 'xyz'),
			(2, 12.00, 12, 12, '12', '12'),
			(3, 5.00, 5, 5, '5', '5'),
			(4, NULL, NULL, NULL, NULL, NULL);
	`); err != nil {
		t.Fatal(err)
	}

	validator := queryrunner.NewValidator([]string{"castrw"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	resolver := runner.ResolveColumnType

	suggest := func(sql string) []queryrunner.RewriteCandidate {
		return queryrunner.SuggestRewritesForInvestigationWithTypes(ctx, sql, nil, false, resolver)
	}

	// Supported type pairs: the cast's target already equals the column's
	// real catalog type, so the engine must offer the rewrite and it must
	// return identical rows to the original, including the NULL row (a cast
	// comparison against NULL is NULL either way, so both queries return it
	// the same number of times: zero).
	matches := []struct {
		name string
		sql  string
	}{
		{"numeric column, numeric cast, fractional value", `SELECT id FROM castrw.t WHERE amount::numeric = 1.40`},
		{"numeric column, decimal alias cast", `SELECT id FROM castrw.t WHERE amount::decimal = 12.00`},
		{"integer column, integer cast", `SELECT id FROM castrw.t WHERE qty::integer = 5`},
		{"integer column, int alias cast", `SELECT id FROM castrw.t WHERE qty::int = 12`},
		{"bigint column, bigint cast", `SELECT id FROM castrw.t WHERE big::bigint = 9000000000`},
		{"text column, text cast", `SELECT id FROM castrw.t WHERE code::text = '12'`},
		{"varchar column, varchar cast", `SELECT id FROM castrw.t WHERE label::varchar = '12'`},
	}
	for _, tc := range matches {
		t.Run(tc.name, func(t *testing.T) {
			cands := suggest(tc.sql)
			var rewrite *queryrunner.RewriteCandidate
			for i := range cands {
				if cands[i].Category == "implicit_cast" {
					rewrite = &cands[i]
					break
				}
			}
			if rewrite == nil {
				t.Fatalf("expected an implicit_cast candidate for %s, got %#v", tc.sql, cands)
			}
			if strings.Contains(rewrite.SQL, "::") {
				t.Fatalf("cast must be dropped, got: %s", rewrite.SQL)
			}
			want, err := rowsOf(ctx, pool, tc.sql)
			if err != nil {
				t.Fatalf("original does not run: %v", err)
			}
			got, err := rowsOf(ctx, pool, rewrite.SQL)
			if err != nil {
				t.Fatalf("rewrite does not run: %s: %v", rewrite.SQL, err)
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Fatalf("rewrite returns different rows\n  original: %s -> %v\n  rewrite:  %s -> %v", tc.sql, want, rewrite.SQL, got)
			}
		})
	}

	// #197 danger cases: declined even with a real, working resolver,
	// because the resolved type never equals the cast's target. amount is
	// numeric(10,2) holding 1.40 — the exact value #197 cited where
	// amount::integer = 1 is TRUE but amount = 1 is FALSE, so this also
	// doubles as a regression check that the engine would have produced a
	// wrong-results rewrite here if the safety check were broken.
	declines := []string{
		`SELECT id FROM castrw.t WHERE amount::integer = 1`,
		`SELECT id FROM castrw.t WHERE amount::text = '12'`,
		`SELECT id FROM castrw.t WHERE code::varchar = '12'`,
		`SELECT id FROM castrw.t WHERE label::text = '12'`,
		// Same base type, but the typmod makes the cast a real conversion:
		// varchar(1) truncates 'xyz' to 'x', so dropping it loses row 1.
		`SELECT id FROM castrw.t WHERE label::varchar(1) = 'x'`,
		`SELECT id FROM castrw.t WHERE amount::numeric(10,1) = 1.4`,
		// A CTE named t shadows castrw.t; its amount column is text, not
		// castrw.t's numeric, so the catalog type must not be trusted.
		`WITH t AS (SELECT id, code AS amount FROM castrw.t) SELECT id FROM t WHERE amount::numeric = 12`,
	}
	for _, sql := range declines {
		t.Run("declines: "+sql, func(t *testing.T) {
			for _, c := range suggest(sql) {
				if c.Category == "implicit_cast" {
					t.Fatalf("a mismatched-type cast must not be offered as a rewrite, got: %s", c.SQL)
				}
			}
		})
	}
}

// Every implicit_cast rewrite the engine offers against a real catalog must
// return exactly the original query's rows. These shapes each fooled an
// earlier version: a nested cast whose inner step rounds, a column alias list
// that renames columns, a same-named domain in another schema, and an
// unqualified table whose search_path resolution differs from the
// alphabetically first allowed schema. The last two queries are controls
// that must still be rewritten.
func TestColumnCastRewriteNeverChangesResults(t *testing.T) {
	ctx := context.Background()
	pool, _ := pilotPostgres(t, ctx)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA audit;
		CREATE TABLE audit.t (id integer, amount numeric, code text);
		INSERT INTO audit.t VALUES (1, 1.4, '01'), (2, 1.0, '1');
		CREATE DOMAIN audit.numeric AS integer;
		CREATE SCHEMA aa;
		CREATE TABLE aa.same (id integer, amount numeric);
		INSERT INTO aa.same VALUES (1, 1.4);
		CREATE TABLE public.same (id integer, amount text);
		INSERT INTO public.same VALUES (1, '01');
	`); err != nil {
		t.Fatal(err)
	}
	runner := queryrunner.NewRunner(pool, queryrunner.NewValidator([]string{"audit", "aa", "public"}, 10000), 1000, 30*time.Second)

	ids := func(t *testing.T, sql string) []int {
		t.Helper()
		rows, err := pool.Query(ctx, sql)
		if err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		defer rows.Close()
		var out []int
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				t.Fatal(err)
			}
			out = append(out, id)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	for _, tc := range []struct {
		sql         string
		wantRewrite bool
	}{
		{`SELECT id FROM audit.t WHERE (amount::integer)::numeric = 1 ORDER BY id`, false},
		{`SELECT id FROM audit.t WHERE (amount::numeric(10,0))::numeric = 1 ORDER BY id`, false},
		{`SELECT id FROM audit.t AS x(id, old_amount, amount) WHERE amount::numeric = '1' ORDER BY id`, false},
		{`SELECT id FROM audit.t WHERE amount::audit.numeric = 1 ORDER BY id`, false},
		{`SELECT id FROM same WHERE amount::numeric = '1' ORDER BY id`, false},
		{`SELECT id FROM audit.t WHERE amount::numeric = 1 ORDER BY id`, true},
		{`SELECT id FROM same WHERE amount::text = '01' ORDER BY id`, true},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			var offered []string
			for _, c := range queryrunner.SuggestRewritesForInvestigationWithTypes(ctx, tc.sql, nil, false, runner.ResolveColumnType) {
				if c.Category == "implicit_cast" {
					offered = append(offered, c.SQL)
				}
			}
			if tc.wantRewrite != (len(offered) > 0) {
				t.Fatalf("rewrite offered=%v, want %v: %v", len(offered) > 0, tc.wantRewrite, offered)
			}
			before := ids(t, tc.sql)
			for _, cand := range offered {
				if after := ids(t, cand); !slices.Equal(before, after) {
					t.Fatalf("rewrite changed results: %s -> %v, %s -> %v", tc.sql, before, cand, after)
				}
			}
		})
	}
}
