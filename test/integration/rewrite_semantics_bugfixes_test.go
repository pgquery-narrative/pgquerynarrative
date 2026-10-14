package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
)

// assertRewriteOutcome runs sql for its true rows, then for every
// SuggestRewrites candidate of category asserts its rows equal sql's rows.
// The rewriter may legitimately respond to an unsafe shape in either of two
// ways: decline it outright (no candidate of that category), or rewrite it
// while preserving its exact semantics (a candidate whose rows match). Both
// count as fixed; only a candidate whose rows differ is a bug. wantCandidate
// additionally requires at least one matching candidate to exist — used for
// positive controls, to prove the fix did not over-broadly decline a shape
// that was always safe.
func assertRewriteOutcome(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql, category string, wantCandidate bool) {
	t.Helper()
	want, err := rowsOf(ctx, pool, sql)
	if err != nil {
		t.Fatalf("original does not run: %s: %v", sql, err)
	}
	found := false
	for _, c := range queryrunner.SuggestRewrites(sql, nil) {
		if c.Category != category {
			continue
		}
		found = true
		got, err := rowsOf(ctx, pool, c.SQL)
		if err != nil {
			t.Fatalf("[%s] rewrite does not run\n  original: %s\n  rewrite:  %s\n  %v", category, sql, c.SQL, err)
		}
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("[%s] rewrite changes the result\n  original: %s\n    -> %v\n  rewrite:  %s\n    -> %v", category, sql, want, c.SQL, got)
		}
	}
	if wantCandidate && !found {
		t.Fatalf("[%s] expected a candidate for a safe shape, got none: %s", category, sql)
	}
	if !wantCandidate && found {
		t.Logf("[%s] candidate was offered and matched (semantics preserved rather than declined): %s", category, sql)
	}
}

// Bug: IN (SELECT i.id::int ...) rewritten to EXISTS dropped the subquery's
// cast, comparing the uncast value instead (rewriter_subquery.go,
// unwrapColumnRefNode(target) in tryRewriteInSubLink / subqueryEligibleForExists).
// Outer id=1 against inner numeric id=1.2: the original's cast rounds 1.2 to
// 1 and matches; the generated EXISTS compared 1.2 = 1 directly and did not.
func TestInToExistsPreservesSubqueryCastSemantics(t *testing.T) {
	ctx := context.Background()
	pool, _ := pilotPostgres(t, ctx)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA cast_in;
		CREATE TABLE cast_in.outer_ids(id int);
		INSERT INTO cast_in.outer_ids VALUES (1);
		CREATE TABLE cast_in.inner_numeric(id numeric);
		INSERT INTO cast_in.inner_numeric VALUES (1.2);
		CREATE TABLE cast_in.inner_int(id int);
		INSERT INTO cast_in.inner_int VALUES (1), (NULL);
	`); err != nil {
		t.Fatal(err)
	}

	// The exact reported reproduction: a numeric cast changes which outer rows
	// match, so dropping it must not happen.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT o.id FROM cast_in.outer_ids o WHERE o.id IN (SELECT i.id::int FROM cast_in.inner_numeric i)`,
		"in_to_exists", false)

	// Same cast, NOT IN form.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT o.id FROM cast_in.outer_ids o WHERE o.id NOT IN (SELECT i.id::int FROM cast_in.inner_numeric i)`,
		"not_in_to_exists", false)

	// Safe bare-column control (no cast): the rewrite must still fire and match.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT o.id FROM cast_in.outer_ids o WHERE o.id IN (SELECT i.id FROM cast_in.inner_int i)`,
		"in_to_exists", true)

	// Safe bare-column control with a NULL in the subquery: NOT IN against a
	// subquery containing NULL is NULL (never true) for every outer row, so
	// the correct result is zero rows; the NULL-safe NOT EXISTS translation
	// must reproduce that, not just skip the cast case.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT o.id FROM cast_in.outer_ids o WHERE o.id NOT IN (SELECT i.id FROM cast_in.inner_int i)`,
		"not_in_to_exists", true)
}

// Bug: splitting an OR into UNION ALL branches changes a window function's
// result, because each branch computes it over only that branch's rows
// (rewriter_or_union.go, selectIsSimpleForUnion checked SelectStmt.WindowClause,
// which only catches a named WINDOW declaration, not the far more common inline
// `fn(...) OVER (...)`). row_number() OVER (ORDER BY id) over id=1,2 numbers
// them 1,2; split into two UNION ALL branches by a=1 OR b=2, each branch's
// row_number() restarts at 1.
func TestOrToUnionDeclinesAnyWindowFunctionShape(t *testing.T) {
	ctx := context.Background()
	pool, _ := pilotPostgres(t, ctx)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA win;
		CREATE TABLE win.window_rows(id int, a int, b int);
		INSERT INTO win.window_rows VALUES (1,1,0),(2,0,2);
	`); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		// The exact reported reproduction: inline window in the target list.
		`SELECT id, row_number() OVER (ORDER BY id) AS rn FROM win.window_rows WHERE a=1 OR b=2 ORDER BY id`,
		// Named window (already caught by the pre-existing WindowClause check;
		// pinned here so the new check does not regress it).
		`SELECT id, row_number() OVER w AS rn FROM win.window_rows WHERE a=1 OR b=2 WINDOW w AS (ORDER BY id) ORDER BY id`,
		// Window function nested inside an arithmetic expression in the target list.
		`SELECT id, (row_number() OVER (ORDER BY id)) + 1 AS rn FROM win.window_rows WHERE a=1 OR b=2 ORDER BY id`,
		// Window function used only in ORDER BY, not projected.
		`SELECT id FROM win.window_rows WHERE a=1 OR b=2 ORDER BY row_number() OVER (ORDER BY id)`,
	}
	for _, sql := range cases {
		assertRewriteOutcome(t, ctx, pool, sql, "or_to_union", false)
	}

	// Ordinary eligible OR query, no window anywhere: the rewrite must still
	// fire and match, so the new check is not overly broad.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT id FROM win.window_rows WHERE a=1 OR b=2 ORDER BY id`,
		"or_to_union", true)
}

// Bug: the anti-join rewrite lowercased parsed identifier names before
// comparing them, conflating a quoted "ID" with an unrelated unquoted id
// (rewriter_antijoin.go, strings.ToLower/EqualFold on column and alias names).
// PostgreSQL already folds unquoted identifiers to lowercase at parse time, so
// b.id and b."ID" are two distinct columns; lowercasing again treated them as
// the same join key, incorrectly peeling `b."ID" IS NULL` into the EXISTS
// correlation that is actually keyed on b.id.
func TestLeftJoinAntiJoinUsesExactIdentifierIdentity(t *testing.T) {
	ctx := context.Background()
	pool, _ := pilotPostgres(t, ctx)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA aj;
		CREATE TABLE aj.left_ids(id int);
		INSERT INTO aj.left_ids VALUES (1);
		CREATE TABLE aj.right_ids(id int, "ID" int);
		INSERT INTO aj.right_ids VALUES (1, NULL);
	`); err != nil {
		t.Fatal(err)
	}

	// The exact reported reproduction: the IS NULL test is on a distinct
	// quoted column "ID", unrelated to the join key b.id.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT a.id FROM aj.left_ids a LEFT JOIN aj.right_ids b ON b.id=a.id WHERE b."ID" IS NULL`,
		"left_join_antijoin_to_not_exists", false)

	// A quoted alias on the right side, still consistently used — must not be
	// rejected just for being quoted.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT a.id FROM aj.left_ids a LEFT JOIN aj.right_ids "B" ON "B".id=a.id WHERE "B".id IS NULL`,
		"left_join_antijoin_to_not_exists", true)

	// A quoted-but-identical-case column name (quoting "id" still names the
	// same lowercase column) — must still rewrite and match.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT a.id FROM aj.left_ids a LEFT JOIN aj.right_ids b ON b."id"=a.id WHERE b."id" IS NULL`,
		"left_join_antijoin_to_not_exists", true)

	// Normal unquoted anti-join control — must still rewrite and match.
	assertRewriteOutcome(t, ctx, pool,
		`SELECT a.id FROM aj.left_ids a LEFT JOIN aj.right_ids b ON b.id=a.id WHERE b.id IS NULL`,
		"left_join_antijoin_to_not_exists", true)
}
