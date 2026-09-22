package integration

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// rowsOf returns every row as text, sorted, so two result sets compare as multisets.
func rowsOf(ctx context.Context, pool *pgxpool.Pool, sql string) ([]string, error) {
	rows, err := pool.Query(ctx, sql)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprint(vals...))
	}
	sort.Strings(out)
	return out, rows.Err()
}

// Every rewrite the engine proposes must return the same rows as the statement it came from, on data
// that includes NULLs, empty subqueries and duplicates. Shapes the rewriter cannot prove are declined.
func TestRewritesReturnTheSameRowsAsTheOriginal(t *testing.T) {
	ctx := context.Background()
	pool, _ := pilotPostgres(t, ctx)
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS rw;
		CREATE TABLE rw.t (id int PRIMARY KEY, parent_id int, v int, k int);
		CREATE TABLE rw.s (id int PRIMARY KEY, t_id int, v int);
		INSERT INTO rw.t VALUES (1,NULL,1,1),(2,1,2,1),(3,1,NULL,2),(4,2,3,NULL),(5,NULL,NULL,NULL),(6,3,1,2);
		INSERT INTO rw.s VALUES (1,1,1),(2,1,NULL),(3,2,2),(4,9,7),(5,NULL,2);
		CREATE TABLE rw.s_clean (id int PRIMARY KEY, t_id int, v int);
		INSERT INTO rw.s_clean VALUES (1,1,1),(2,2,2),(3,4,3);
		CREATE TABLE rw.s_empty (id int PRIMARY KEY, t_id int, v int);
		SET search_path = rw, public;`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `ALTER DATABASE `+dbName(t, ctx, pool)+` SET search_path = rw, public`); err != nil {
		t.Fatal(err)
	}
	pool.Close()
	pool = reopen(t, ctx, pool)

	for _, q := range []string{
		`SELECT id FROM t WHERE v NOT IN (SELECT v FROM s)`,
		`SELECT id FROM t WHERE v NOT IN (SELECT v FROM s_clean)`,
		`SELECT id FROM t WHERE v NOT IN (SELECT v FROM s_empty)`,
		`SELECT id FROM t WHERE v NOT IN (SELECT v FROM s WHERE id < 0)`,
		`SELECT id FROM t WHERE v IN (SELECT v FROM s)`,
		`SELECT id FROM t WHERE NOT (v IN (SELECT v FROM s_clean))`,
		`SELECT id FROM t WHERE NOT (v IN (SELECT v FROM s_clean) OR k = 2)`,
		`SELECT id FROM t WHERE NOT (v NOT IN (SELECT v FROM s_clean))`,
		`SELECT id FROM t WHERE NOT (k = 2 AND v NOT IN (SELECT v FROM s_clean))`,
		`SELECT id FROM t WHERE k = 1 OR v IN (SELECT v FROM s_clean)`,
		`SELECT id FROM t WHERE id IN (SELECT parent_id FROM t)`,
		`SELECT id FROM t WHERE id NOT IN (SELECT parent_id FROM t WHERE parent_id IS NOT NULL)`,
		`SELECT id FROM t a WHERE id IN (SELECT parent_id FROM t a)`,
		`SELECT a.id FROM t a WHERE a.id IN (SELECT parent_id FROM t b)`,
		`SELECT id FROM t WHERE v > ANY (SELECT v FROM s_clean)`,
		`SELECT id FROM t WHERE v < ANY (SELECT v FROM s)`,
		`SELECT id FROM t WHERE v = ANY (SELECT v FROM s_clean)`,
		`SELECT id FROM t WHERE v <> ALL (SELECT v FROM s_clean)`,
		`SELECT id FROM t WHERE v IN (SELECT v FROM s WHERE s.t_id = t.id)`,
		`SELECT id FROM t WHERE k IN (SELECT t_id FROM s_clean WHERE s_clean.v = t.v)`,
		`SELECT t.id FROM t LEFT JOIN s ON s.t_id = t.id WHERE s.id IS NULL`,
		`SELECT t.id FROM t LEFT JOIN s ON s.t_id = t.id WHERE s.v IS NULL`,
		`SELECT t.id FROM t LEFT JOIN t p ON p.id = t.parent_id WHERE p.id IS NULL`,
		`SELECT id FROM t WHERE v = 1 OR k = 1`,
		`SELECT id FROM t WHERE v = 1 OR v = 2 OR v = 3`,
	} {
		want, err := rowsOf(ctx, pool, q)
		if err != nil {
			t.Fatalf("original does not run: %s: %v", q, err)
		}
		for _, c := range queryrunner.SuggestRewrites(q, nil) {
			got, err := rowsOf(ctx, pool, c.SQL)
			if err != nil {
				t.Errorf("[%s] the rewrite does not run\n  original: %s\n  rewrite:  %s\n  %v", c.Category, q, c.SQL, err)
				continue
			}
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("[%s] the rewrite returns different rows\n  original: %s\n    -> %v\n  rewrite:  %s\n    -> %v", c.Category, q, want, c.SQL, got)
			}
		}
	}
}

func dbName(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var n string
	if err := pool.QueryRow(ctx, `SELECT current_database()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return `"` + n + `"`
}

func reopen(t *testing.T, ctx context.Context, old *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	cfg := old.Config()
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

var _ = testhelpers.RunPostgresContainer
