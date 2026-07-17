package integration

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

type noopLLM struct{}

func (noopLLM) Generate(context.Context, string) (string, error) { return "", nil }
func (noopLLM) Name() string                                     { return "integration-test" }

// TestReportsServiceListAndGet verifies ReportsService List and Get against a real
// Postgres with migrations. A report row is inserted directly; List and Get are then called.
func TestReportsServiceListAndGet(t *testing.T) {
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
			t.Fatalf("postgres not ready after 15s: %v", err)
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

	narrativeJSON := []byte(`{"headline":"Integration test","takeaways":["A"],"drivers":[],"limitations":[],"recommendations":[]}`)
	metricsJSON := []byte(`{}`)

	var reportID string
	err = pool.QueryRow(ctx, `
		INSERT INTO app.reports (sql, narrative_md, narrative_json, metrics, llm_model, llm_provider, organization_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, "SELECT region FROM demo.sales", "Integration test", narrativeJSON, metricsJSON, "test", "integration", auth.DefaultOrganizationID).Scan(&reportID)
	if err != nil {
		t.Fatalf("failed to insert report: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	var client llm.Client = noopLLM{}
	appDB := db.NewOrgScoped(pool)
	reportsSvc := service.NewReportsService(pool, appDB, runner, client, config.MetricsConfig{})

	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "test", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	// List
	listRes, err := reportsSvc.List(reqCtx, &reports.ListPayload{Limit: 10, Offset: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listRes.Items) == 0 {
		t.Fatal("expected at least one report from List")
	}
	if listRes.Items[0].ID != reportID {
		t.Errorf("List first id = %q, want %q", listRes.Items[0].ID, reportID)
	}

	// Get
	getRes, err := reportsSvc.Get(reqCtx, &reports.GetPayload{ID: reportID})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if getRes.ID != reportID {
		t.Errorf("Get id = %q, want %q", getRes.ID, reportID)
	}
	if getRes.SQL != "SELECT region FROM demo.sales" {
		t.Errorf("Get sql = %q", getRes.SQL)
	}
	if getRes.Narrative == nil || getRes.Narrative.Headline != "Integration test" {
		t.Errorf("Get narrative = %+v", getRes.Narrative)
	}

	// Get non-existent -> not found
	_, err = reportsSvc.Get(reqCtx, &reports.GetPayload{ID: "00000000-0000-0000-0000-000000000000"})
	if err == nil {
		t.Fatal("expected error for missing report")
	}
	var notFound *reports.NotFoundError
	if !errors.As(err, &notFound) {
		t.Errorf("expected NotFoundError, got %T: %v", err, err)
	}
}
