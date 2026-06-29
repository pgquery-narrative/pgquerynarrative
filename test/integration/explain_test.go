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

func TestExplainIntegration(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
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
			t.Fatalf("postgres not ready: %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatalf("failed to resolve migrations path: %v", err)
	}

	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("failed to create migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("failed to run migrations: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("failed to create pool: %v", err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		VALUES (gen_random_uuid(), CURRENT_DATE, 'Electronics', 'Alpha', 5, 10.00, 50.00, 'North', 'A. Lee')
	`)
	if err != nil {
		t.Fatalf("failed to seed data: %v", err)
	}

	// Stabilize planner output across PostgreSQL versions (partitioned demo.sales has region indexes).
	_, err = pool.Exec(ctx, `
		SET enable_indexscan = off;
		SET enable_bitmapscan = off;
		SET max_parallel_workers_per_gather = 0;
	`)
	if err != nil {
		t.Fatalf("failed to set planner GUCs: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `
			RESET enable_indexscan;
			RESET enable_bitmapscan;
			RESET max_parallel_workers_per_gather;
		`)
	})

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)

	// Unindexed filter on region tends to seq-scan on small demo data.
	sql := "SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = 'North' GROUP BY product_category"
	result, err := runner.Explain(ctx, sql, false)
	if err != nil {
		t.Fatalf("explain failed: %v", err)
	}
	if result.TotalCost <= 0 {
		t.Fatalf("expected positive total cost, got %v", result.TotalCost)
	}
	if len(result.Plan) == 0 {
		t.Fatal("expected raw plan json")
	}
	if result.SQL != sql {
		t.Fatalf("expected normalized sql %q, got %q", sql, result.SQL)
	}

	if len(result.Findings) == 0 {
		t.Fatalf("expected at least one plan finding, got empty findings")
	}
	if !strings.Contains(strings.ToLower(string(result.Plan)), "sales") {
		t.Fatalf("expected plan to reference demo.sales, plan=%s", string(result.Plan))
	}

	foundSeqScan := false
	for _, f := range result.Findings {
		if f.IsSeqScan {
			foundSeqScan = true
			if f.Relation != "" && f.Relation != "sales" && !strings.HasPrefix(f.Relation, "sales_") {
				t.Fatalf("expected seq scan on sales partition, got %+v", f)
			}
		}
	}
	if !foundSeqScan {
		t.Fatalf("expected at least one sequential scan finding after disabling index scans, got %+v", result.Findings)
	}
}
