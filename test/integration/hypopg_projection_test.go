package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// setupHypopgPool starts Postgres built from tools/docker/postgres-hypopg.Dockerfile
// (the same image docker-compose.yml uses for local dev) so these tests exercise
// the real planner-backed hypopg path in app/queryrunner/hypopg.go rather than
// only its labeled-heuristic fallback, which every other integration test is
// limited to since the plain testcontainers Postgres image never carries the
// extension. maxConns is forced to 1: hypopg's hypothetical-index registration
// is per-backend session state, not transactional, so a test asserting session
// hygiene after a failure needs the guarantee that the next call reuses the
// exact same physical connection.
func setupHypopgPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	ctx := context.Background()
	container := testhelpers.RunPostgresHypopgContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				break
			}
		}
		if waitCtx.Err() != nil {
			t.Fatalf("hypopg postgres not ready: %v", pingErr)
		}
		time.Sleep(300 * time.Millisecond)
	}

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	// Migration 000050 runs CREATE EXTENSION IF NOT EXISTS hypopg and 000051
	// grants execute rights to the analytical role.
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}

	connConfig, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		t.Fatal(err)
	}
	connConfig.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, connConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

func seedHypopgSales(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2024-01-01', DATE '2025-06-30', INTERVAL '1 day') AS d
	`); err != nil {
		t.Fatalf("seed demo.sales: %v", err)
	}
	if _, err := pool.Exec(ctx, `ANALYZE demo.sales`); err != nil {
		t.Fatalf("analyze demo.sales: %v", err)
	}
}

// TestProjectIndexCost_RealHypopg proves the planner-backed path actually
// works end to end against real Postgres+hypopg: before this test, hypopg vs.
// heuristic honesty (ScoreIndexProjection: Rankable=true only for
// Method==hypopg) was covered only by unit tests feeding in a synthetic
// IndexProjection — projectWithHypopg's real BeginTx / SET LOCAL
// transaction_read_only=off / hypopg_create_index / EXPLAIN / hypopg_reset
// sequence had never actually run in CI.
func TestProjectIndexCost_RealHypopg(t *testing.T) {
	pool, ctx := setupHypopgPool(t)
	seedHypopgSales(t, ctx, pool)

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)

	if !runner.HypopgAvailable(ctx) {
		t.Fatal("hypopg extension should be installed and CREATE EXTENSION'd by migration 000050 in this image")
	}

	sql := `SELECT * FROM demo.sales WHERE sales_rep = 'A'`
	explainResult, err := runner.Explain(ctx, sql, false)
	if err != nil {
		t.Fatalf("Explain: %v", err)
	}
	baselineCost := explainResult.TotalCost

	proj := runner.ProjectIndexCost(ctx, sql, `CREATE INDEX idx_sales_rep ON demo.sales (sales_rep)`, baselineCost)
	if proj.Method != queryrunner.IndexProjectionHypopg {
		t.Fatalf("expected Method=hypopg, got %q (rationale: %s, failure_reason: %s)", proj.Method, proj.Rationale, proj.FailureReason)
	}
	if !proj.Available {
		t.Fatalf("expected Available=true for a successful hypopg projection: %+v", proj)
	}
	if proj.HypotheticalOID == 0 {
		t.Fatalf("expected a nonzero hypothetical index OID: %+v", proj)
	}
	if proj.ProjectedCost >= baselineCost {
		t.Logf("projected cost %v was not lower than baseline %v for this plan shape — logged, not failed, since planner choices are environment-dependent", proj.ProjectedCost, baselineCost)
	}

	// The hypothetical index must not survive past the call: hypopg_reset()
	// runs inside projectWithHypopg's own transaction before it returns.
	var hidden int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hypopg_list_indexes`).Scan(&hidden); err != nil {
		t.Fatalf("hypopg_list_indexes: %v", err)
	}
	if hidden != 0 {
		t.Fatalf("expected no hypothetical indexes left registered after ProjectIndexCost, got %d", hidden)
	}
}

// TestProjectIndexCost_InvalidDDLLeavesSessionClean pins the fix for the
// finding that hypopg.go's create-index failure branch did not call
// hypopg_reset() before returning (unlike the EXPLAIN-failure branch right
// below it), which could leave a phantom hypothetical index registered on the
// pooled connection. hypopg's hypothetical-index state is backend-session
// state, not transactional, so the connection's own transaction rolling back
// does not undo it — only an explicit reset does. With MaxConns=1 the second
// call is guaranteed to reuse the exact same physical connection, so a leak
// from the first call would corrupt the second call's plan.
func TestProjectIndexCost_InvalidDDLLeavesSessionClean(t *testing.T) {
	pool, ctx := setupHypopgPool(t)
	seedHypopgSales(t, ctx, pool)

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)
	sql := `SELECT * FROM demo.sales WHERE sales_rep = 'A'`

	// hypopg_create_index fails server-side for a column that does not exist.
	badProj := runner.ProjectIndexCost(ctx, sql, `CREATE INDEX idx_bad ON demo.sales (nonexistent_column)`, 100)
	if badProj.Method == queryrunner.IndexProjectionHypopg {
		t.Fatalf("a failed hypopg_create_index must not report Method=hypopg: %+v", badProj)
	}

	// The same (only) pooled connection must come back clean: no leaked
	// hypothetical index, no aborted transaction blocking further queries.
	var hidden int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hypopg_list_indexes`).Scan(&hidden); err != nil {
		t.Fatalf("connection unusable after a failed hypopg_create_index (session likely left dirty): %v", err)
	}
	if hidden != 0 {
		t.Fatalf("expected no hypothetical indexes registered after a failed create, got %d", hidden)
	}

	// And a subsequent real projection on that same connection must still work.
	goodProj := runner.ProjectIndexCost(ctx, sql, `CREATE INDEX idx_sales_rep ON demo.sales (sales_rep)`, 100)
	if goodProj.Method != queryrunner.IndexProjectionHypopg {
		t.Fatalf("expected a valid hypopg projection to still succeed after the earlier failure, got Method=%q failure_reason=%q", goodProj.Method, goodProj.FailureReason)
	}
}

// A statement that fails in EXPLAIN after the hypothetical index was created aborts the transaction, so a
// reset issued inside it cannot run. hypopg's registration lives in the backend, not the transaction, so
// the index would stay on the pooled connection and colour the next plan taken on it.
func TestProjectIndexCost_FailedExplainLeavesSessionClean(t *testing.T) {
	pool, ctx := setupHypopgPool(t)
	seedHypopgSales(t, ctx, pool)

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)

	// 1/0 is folded while planning, so EXPLAIN fails after hypopg_create_index succeeded.
	proj := runner.ProjectIndexCost(ctx, `SELECT 1/0 FROM demo.sales WHERE sales_rep = 'A'`,
		`CREATE INDEX idx_sales_rep ON demo.sales (sales_rep)`, 100)
	if proj.Method == queryrunner.IndexProjectionHypopg {
		t.Fatalf("a failed EXPLAIN must not report Method=hypopg: %+v", proj)
	}

	var hidden int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM hypopg_list_indexes`).Scan(&hidden); err != nil {
		t.Fatalf("connection unusable after a failed EXPLAIN: %v", err)
	}
	if hidden != 0 {
		t.Fatalf("%d hypothetical index(es) left on the pooled connection after a failed EXPLAIN", hidden)
	}
}
