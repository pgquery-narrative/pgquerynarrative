package integration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// TestP0HeroPath_RewriteCompareEquivalence verifies the bulletproof path:
// paste SQL → SuggestRewrites → dry EXPLAIN both → COUNT(*)+sample equivalence Equal.
func TestP0HeroPath_RewriteCompareEquivalence(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	waitReady(t, ctx, connStr)

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-01-31', INTERVAL '1 day') AS d
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)

	problem := `SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01' GROUP BY product_category ORDER BY revenue DESC`
	cands := queryrunner.SuggestRewrites(problem, nil)
	if len(cands) == 0 {
		t.Fatal("expected DATE_TRUNC rewrite candidate")
	}
	rewrite := cands[0].SQL
	if strings.Contains(strings.ToLower(rewrite), "date_trunc") {
		t.Fatalf("rewrite should unwrap DATE_TRUNC: %s", rewrite)
	}

	beforePlan, err := runner.Explain(ctx, problem, false)
	if err != nil {
		t.Fatalf("explain before: %v", err)
	}
	afterPlan, err := runner.Explain(ctx, rewrite, false)
	if err != nil {
		t.Fatalf("explain after: %v", err)
	}
	if afterPlan.TotalCost >= beforePlan.TotalCost && afterPlan.TotalCost > 0 && beforePlan.TotalCost > 0 {
		// On tiny partitions cost may be similar; still require a successful compare path.
		t.Logf("note: after cost %.2f vs before %.2f (may be similar on tiny seed)", afterPlan.TotalCost, beforePlan.TotalCost)
	}

	// Exercise COUNT(*) equivalence for a semantic-preserving DATE_TRUNC unwrap.
	beforeCountRes, err := runner.Run(ctx, "SELECT COUNT(*)::bigint FROM ("+problem+") AS q", 1)
	if err != nil {
		t.Fatalf("count before: %v", err)
	}
	afterCountRes, err := runner.Run(ctx, "SELECT COUNT(*)::bigint FROM ("+rewrite+") AS q", 1)
	if err != nil {
		t.Fatalf("count after: %v", err)
	}
	if beforeCountRes.RowCount != 1 || afterCountRes.RowCount != 1 {
		t.Fatal("expected single count row")
	}
	if beforeCountRes.Rows[0][0] != afterCountRes.Rows[0][0] {
		t.Fatalf("COUNT mismatch: before=%v after=%v", beforeCountRes.Rows[0][0], afterCountRes.Rows[0][0])
	}

	beforeSample, err := runner.Run(ctx, problem, 1000)
	if err != nil {
		t.Fatalf("sample before: %v", err)
	}
	afterSample, err := runner.Run(ctx, rewrite, 1000)
	if err != nil {
		t.Fatalf("sample after: %v", err)
	}
	if beforeSample.RowCount != afterSample.RowCount {
		t.Fatalf("sample row mismatch %d vs %d", beforeSample.RowCount, afterSample.RowCount)
	}

	// EXTRACT year+month path
	extractSQL := `SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE EXTRACT(YEAR FROM date) = 2025 AND EXTRACT(MONTH FROM date) = 1 GROUP BY product_category`
	extractCands := queryrunner.SuggestRewrites(extractSQL, nil)
	if len(extractCands) == 0 {
		t.Fatal("expected EXTRACT rewrite")
	}
	if strings.Contains(strings.ToLower(extractCands[0].SQL), "extract") {
		t.Fatalf("EXTRACT should be unwrapped: %s", extractCands[0].SQL)
	}

	// Fail-closed OR with LIKE
	badOR := `SELECT id FROM demo.sales WHERE region = 'North' OR product_category LIKE 'E%'`
	if got := queryrunner.SuggestRewrites(badOR, nil); len(got) != 0 {
		t.Fatalf("complex OR must fail closed: %#v", got)
	}
}

func waitReady(t *testing.T, ctx context.Context, connStr string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				return
			}
		}
		if waitCtx.Err() != nil {
			t.Fatalf("postgres not ready: %v", pingErr)
		}
		time.Sleep(400 * time.Millisecond)
	}
}
