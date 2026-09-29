package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

func TestRunnerIntegration(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	// Wait for Postgres to accept connections (container "ready" can be before DB is listening).
	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for attempt := 0; ; attempt++ {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				break
			}
		}
		if waitCtx.Err() != nil {
			t.Fatalf("postgres not ready after 15s: last error %v", pingErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	helpers.RunMigrations(t, connStr)

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

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)

	result, err := runner.Run(ctx, "SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category", 100)
	if err != nil {
		t.Fatalf("runner failed: %v", err)
	}
	if result.RowCount == 0 {
		t.Fatalf("expected rows, got 0")
	}
	if result.Columns[0].Name != "product_category" {
		t.Fatalf("unexpected column name: %s", result.Columns[0].Name)
	}
	if result.Columns[0].Type == "" {
		t.Fatalf("expected column type to be set")
	}
}
