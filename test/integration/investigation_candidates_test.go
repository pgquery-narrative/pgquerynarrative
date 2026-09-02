package integration

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// Every candidate tested for an investigation is kept, not overwritten.
func TestInvestigationCandidateHistory(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-01-20', INTERVAL '1 day') AS d
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesSvc := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	var llmClient llm.Client = noopLLM{}
	reportsSvc := service.NewReportsService(pool, appDB, runner, llmClient, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "cand-hist", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	inv, err := invSvc.Create(reqCtx, &investigations.CreateInvestigationPayload{
		Title: "candidate history",
		SQL:   `SELECT region, count(*) FROM demo.sales WHERE region = 'North' GROUP BY region`,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	cand1 := `SELECT region, count(*) FROM demo.sales WHERE region = 'North' GROUP BY 1`
	cand2 := `SELECT region, count(*) FROM demo.sales WHERE region = 'North' GROUP BY region ORDER BY region`

	if _, err := invSvc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: cand1}); err != nil {
		t.Fatalf("AddCandidate 1: %v", err)
	}
	got, err := invSvc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: cand2})
	if err != nil {
		t.Fatalf("AddCandidate 2: %v", err)
	}

	if len(got.Candidates) != 2 {
		t.Fatalf("expected 2 candidates in history, got %d", len(got.Candidates))
	}
	// Newest first, and cand2 is current.
	if got.Candidates[0].CandidateSQL != cand2 || got.Candidates[0].IsCurrent == nil || !*got.Candidates[0].IsCurrent {
		t.Fatalf("newest candidate should be cand2 and current: %+v", got.Candidates[0])
	}
	if got.Candidates[1].IsCurrent != nil && *got.Candidates[1].IsCurrent {
		t.Fatalf("older candidate must not be marked current")
	}
	for _, c := range got.Candidates {
		if c.EquivalenceStatus == nil || *c.EquivalenceStatus == "" {
			t.Errorf("candidate %s missing equivalence status", c.ID)
		}
	}

	// Re-testing cand1 updates the existing row, not a third entry, and makes it current.
	reGot, err := invSvc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: cand1})
	if err != nil {
		t.Fatalf("AddCandidate re-test: %v", err)
	}
	if len(reGot.Candidates) != 2 {
		t.Fatalf("re-testing an existing candidate must not add a row, got %d", len(reGot.Candidates))
	}
	if reGot.Candidates[0].CandidateSQL != cand1 || !*reGot.Candidates[0].IsCurrent {
		t.Fatalf("re-tested candidate should be current: %+v", reGot.Candidates[0])
	}
}
