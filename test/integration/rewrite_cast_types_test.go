package integration

import (
	"context"
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
