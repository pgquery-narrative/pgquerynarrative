package integration

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// TestP1HeroPath_ServiceEndToEnd exercises the full differentiator path through
// the service layer: Create → SuggestRewrite → RankCandidates → ComparePlans
// (Equal equivalence) → GenerateReport.
func TestP1HeroPath_ServiceEndToEnd(t *testing.T) {
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

	// Enough rows for catalog-backed index advice; small enough for fast tests.
	_, err = pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-06-30', INTERVAL '1 day') AS d
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = pool.Exec(ctx, `ANALYZE demo.sales`)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesSvc := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	var llmClient llm.Client = noopLLM{}
	reportsSvc := service.NewReportsService(pool, appDB, runner, llmClient, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)

	reqCtx := auth.WithPrincipal(ctx, auth.Principal{
		UserID: "p1-hero-test",
		OrgID:  auth.DefaultOrganizationID,
		Role:   auth.RoleAdmin,
	})

	problem := `SELECT product_category, SUM(total_amount) AS revenue FROM demo.sales WHERE DATE_TRUNC('month', date) BETWEEN DATE '2025-01-01' AND DATE '2025-03-31' GROUP BY product_category ORDER BY revenue DESC`

	inv, err := invSvc.Create(reqCtx, &investigations.CreateInvestigationPayload{
		Title: "P1 hero DATE_TRUNC",
		SQL:   problem,
	})
	if err != nil {
		t.Fatalf("Create investigation: %v", err)
	}
	if inv.Explain == nil || len(inv.Explain.Findings) == 0 {
		t.Fatal("expected EXPLAIN findings on investigation")
	}

	suggestions, err := invSvc.SuggestRewrite(reqCtx, &investigations.SuggestRewritePayload{ID: inv.ID})
	if err != nil {
		t.Fatalf("SuggestRewrite: %v", err)
	}
	if len(suggestions.Candidates) == 0 {
		t.Fatal("expected rewrite suggestions")
	}
	rewriteSQL := suggestions.Candidates[0].SQL
	if strings.Contains(strings.ToLower(rewriteSQL), "date_trunc") {
		t.Fatalf("rewrite should unwrap DATE_TRUNC: %s", rewriteSQL)
	}

	ranked, err := invSvc.RankCandidates(reqCtx, &investigations.RankCandidatesPayload{
		ID:      inv.ID,
		Analyze: false,
	})
	if err != nil {
		t.Fatalf("RankCandidates: %v", err)
	}
	if len(ranked.Candidates) == 0 {
		t.Fatal("expected ranked candidates")
	}
	foundRewrite := false
	for _, c := range ranked.Candidates {
		if c.Kind == "sql_rewrite" && c.SQL != nil {
			foundRewrite = true
			break
		}
	}
	if !foundRewrite {
		t.Fatalf("expected at least one ranked sql_rewrite candidate: %#v", ranked.Candidates)
	}

	cmp, err := queriesSvc.ComparePlans(reqCtx, &queries.ComparePlansPayload{
		BeforeSQL: problem,
		AfterSQL:  rewriteSQL,
		Analyze:   false,
	})
	if err != nil {
		t.Fatalf("ComparePlans: %v", err)
	}
	if cmp.ResultEquivalenceStatus == nil || *cmp.ResultEquivalenceStatus != service.EquivalenceEqual {
		status := "nil"
		if cmp.ResultEquivalenceStatus != nil {
			status = *cmp.ResultEquivalenceStatus
		}
		t.Fatalf("expected Equal equivalence, got %q notes=%v", status, cmp.ResultEquivalenceNotes)
	}

	inv, err = invSvc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{
		ID:           inv.ID,
		CandidateSQL: rewriteSQL,
	})
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}
	if inv.Comparison == nil || inv.Comparison.ResultEquivalenceStatus == nil ||
		*inv.Comparison.ResultEquivalenceStatus != service.EquivalenceEqual {
		t.Fatalf("stored comparison should be Equal: %#v", inv.Comparison)
	}

	inv, err = invSvc.GenerateReport(reqCtx, &investigations.GenerateReportPayload{ID: inv.ID})
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if inv.ReportID == nil || *inv.ReportID == "" {
		t.Fatal("expected report_id on investigation after GenerateReport")
	}
	if inv.Status != "complete" {
		t.Fatalf("expected status complete, got %q", inv.Status)
	}
}

// TestP0HeroPath_GenerateReportBlocksNonEqual verifies API-level P0 gate:
// GenerateReport rejects investigations whose comparison is Different.
func TestP0HeroPath_GenerateReportBlocksNonEqual(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, connStr)

	migrationsPath, _ := filepath.Abs("../../app/db/migrations")
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-01-10', INTERVAL '1 day') AS d
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesSvc := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	var llmClient llm.Client = noopLLM{}
	reportsSvc := service.NewReportsService(pool, appDB, runner, llmClient, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)

	reqCtx := auth.WithPrincipal(ctx, auth.Principal{
		UserID: "p0-block-test",
		OrgID:  auth.DefaultOrganizationID,
		Role:   auth.RoleAdmin,
	})

	beforeSQL := `SELECT region FROM demo.sales WHERE region = 'North'`
	inv, err := invSvc.Create(reqCtx, &investigations.CreateInvestigationPayload{
		Title: "P0 block Different",
		SQL:   beforeSQL,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Deliberately different result set → equivalence Different.
	badCandidate := `SELECT region FROM demo.sales WHERE region = 'South'`
	inv, err = invSvc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{
		ID:           inv.ID,
		CandidateSQL: badCandidate,
	})
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}
	if inv.Comparison == nil || inv.Comparison.ResultEquivalenceStatus == nil ||
		*inv.Comparison.ResultEquivalenceStatus != service.EquivalenceDifferent {
		t.Fatalf("expected Different equivalence, got %#v", inv.Comparison)
	}

	_, err = invSvc.GenerateReport(reqCtx, &investigations.GenerateReportPayload{ID: inv.ID})
	if err == nil {
		t.Fatal("GenerateReport must reject Different equivalence")
	}
	var ve *investigations.ValidationError
	if !errors.As(err, &ve) || ve.Code == nil || *ve.Code != "EQUIVALENCE_NOT_EQUAL" {
		t.Fatalf("expected EQUIVALENCE_NOT_EQUAL validation error, got %T %v", err, err)
	}
}

// TestP1HeroPath_GoldenQueries runs additional golden queries on demo.sales.
func TestP1HeroPath_GoldenQueries(t *testing.T) {
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, connStr)

	migrationsPath, _ := filepath.Abs("../../app/db/migrations")
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatal(err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	_, err = pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date,
			CASE WHEN i % 3 = 0 THEN 'Electronics' WHEN i % 3 = 1 THEN 'Furniture' ELSE 'Office' END,
			'Item', 1 + (i % 5), 10, 10 * (1 + (i % 5)),
			CASE WHEN i % 2 = 0 THEN 'North' ELSE 'South' END, 'Rep'
		FROM generate_series(DATE '2025-01-01', DATE '2025-02-28', INTERVAL '1 day') WITH ORDINALITY AS t(d, i)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)

	cases := []struct {
		name     string
		sql      string
		category string
	}{
		{
			name:     "EXTRACT year month",
			sql:      `SELECT SUM(total_amount) FROM demo.sales WHERE EXTRACT(YEAR FROM date) = 2025 AND EXTRACT(MONTH FROM date) = 1`,
			category: "function_wrap",
		},
		{
			name:     "OR to union",
			sql:      `SELECT id FROM demo.sales WHERE region = 'North' OR product_category = 'Electronics'`,
			category: "or_to_union",
		},
		{
			name:     "IN to exists",
			sql:      `SELECT s.id FROM demo.sales s WHERE s.region IN (SELECT region FROM demo.sales WHERE product_category = 'Electronics')`,
			category: "in_to_exists",
		},
		{
			name:     "numeric cast",
			sql:      `SELECT id FROM demo.sales WHERE quantity::int = 3`,
			category: "implicit_cast",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cands := queryrunner.SuggestRewrites(tc.sql, nil)
			if len(cands) == 0 {
				t.Fatalf("expected rewrite for %s", tc.name)
			}
			found := false
			for _, c := range cands {
				if c.Category == tc.category {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("expected category %q in %#v", tc.category, cands)
			}
			rewrite := cands[0].SQL
			if _, err := runner.Explain(ctx, rewrite, false); err != nil {
				t.Fatalf("explain rewrite: %v", err)
			}
		})
	}
}
