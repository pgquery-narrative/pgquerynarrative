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

	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

const exampleMonthlySalesSQL = `SELECT date_trunc('month', date)::date AS month, SUM(total_amount) AS monthly_total, SUM(quantity) AS units_sold FROM demo.sales GROUP BY 1 ORDER BY 1`

func TestPeriodComparisonSQLMatchesGo_ExampleQuery(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
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
		t.Fatalf("migrations path: %v", err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	defer pool.Close()

	// Seed three months with known totals for deterministic comparison.
	_, err = pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		VALUES
		  (gen_random_uuid(), '2025-01-15', 'Electronics', 'A', 10, 100, 1000, 'North', 'Rep1'),
		  (gen_random_uuid(), '2025-02-10', 'Electronics', 'B', 12, 100, 1200, 'North', 'Rep1'),
		  (gen_random_uuid(), '2025-03-05', 'Electronics', 'C', 9, 100, 900, 'North', 'Rep1')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)

	result, err := runner.Run(ctx, exampleMonthlySalesSQL, 100)
	if err != nil {
		t.Fatalf("run example query: %v", err)
	}
	if result.RowCount < 2 {
		t.Fatalf("need at least 2 monthly rows, got %d", result.RowCount)
	}

	colNames := make([]string, len(result.Columns))
	for i, c := range result.Columns {
		colNames[i] = c.Name
	}

	profiles := metrics.ProfileColumns(colNames, result.Rows)
	timeCol, measureCols := queryrunner.PeriodColumnsFromProfiles(colNames, profiles)
	if timeCol == "" || len(measureCols) == 0 {
		t.Fatalf("expected time + measures from profiling, got time=%q measures=%v", timeCol, measureCols)
	}

	sqlOut, err := runner.PeriodComparison(ctx, exampleMonthlySalesSQL, timeCol, measureCols, 0.5)
	if err != nil {
		t.Fatalf("SQL period comparison: %v", err)
	}
	if sqlOut == nil {
		t.Fatal("expected SQL period comparison output")
	}

	goOut := periodComparisonFromGoMetrics(colNames, result.Rows)
	if goOut == nil {
		t.Fatal("expected Go metrics period comparison")
	}

	if goOut.CurrentPeriodLabel != sqlOut.CurrentPeriodLabel || goOut.PreviousPeriodLabel != sqlOut.PreviousPeriodLabel {
		t.Errorf("labels go=%q/%q sql=%q/%q",
			goOut.CurrentPeriodLabel, goOut.PreviousPeriodLabel,
			sqlOut.CurrentPeriodLabel, sqlOut.PreviousPeriodLabel,
		)
	}
	if !queryrunner.NearlyEqualPeriodComparison(goOut.Comparisons, sqlOut.Comparisons, 1e-6) {
		t.Errorf("comparisons differ:\ngo  = %+v\nsql = %+v", goOut.Comparisons, sqlOut.Comparisons)
	}
}

func periodComparisonFromGoMetrics(columnNames []string, rows [][]interface{}) *queryrunner.PeriodComparisonOutput {
	profiles := metrics.ProfileColumns(columnNames, rows)
	m := metrics.CalculateMetrics(columnNames, rows, profiles, nil)
	if len(m.TimeSeries) == 0 {
		return nil
	}
	out := &queryrunner.PeriodComparisonOutput{
		CurrentPeriodLabel:  m.CurrentPeriodLabel,
		PreviousPeriodLabel: m.PreviousPeriodLabel,
	}
	for measure, ts := range m.TimeSeries {
		pc := queryrunner.PeriodComparison{
			Measure: measure,
			Current: ts.CurrentPeriod,
			Trend:   ts.Trend,
		}
		if ts.PreviousPeriod != nil {
			pc.Previous = ts.PreviousPeriod
		}
		if ts.Change != nil {
			pc.Change = ts.Change
		}
		if ts.ChangePercentage != nil {
			pc.ChangePercentage = ts.ChangePercentage
		}
		out.Comparisons = append(out.Comparisons, pc)
	}
	return out
}
