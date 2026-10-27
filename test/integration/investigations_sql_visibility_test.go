package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/internal/config"
	"github.com/pgquerynarrative/pgquerynarrative/internal/db"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// Bug: InvestigationsService.Get and List applied no role-based shaping at
// all — every same-organization caller, including a Viewer, received the raw
// SQL, captured bind values, candidate SQL, and any literal value embedded in
// a plan finding's Message/Evidence (see planFindingMessage/seqScanEvidence
// in queryrunner) verbatim, unlike the equivalent reports.go path
// (applySQLVisibility) which already redacts for ReportsService.
//
// This creates a real investigation (and a real candidate comparison) whose
// filtered column's literal contains an em dash — the exact shape that used
// to defeat RedactLiteralFilterText's boundary scan (see
// TestRedactLiteralFilterTextCannotBeTrickedByEmDashInLiteral in
// internal/story) — and checks every reader's serialized JSON response.
func TestInvestigationsViewerRedactionOnGetAndList(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitReady(t, ctx, connStr)
	helpers.RunMigrations(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	if _, err := pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		VALUES (gen_random_uuid(), '2025-01-01', 'Electronics', 'Widget', 1, 10, 10, 'North', 'A')
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// A literal containing " — " in the filtered column: the exact shape that
	// used to leak its own suffix through RedactLiteralFilterText's old,
	// trickable boundary scan.
	const secretEmail = "public — bearer-secret@example.test"
	const sourceSQL = "SELECT region, sales_rep FROM demo.sales WHERE sales_rep = '" + secretEmail + "'"
	const candidateSQL = "SELECT region, sales_rep FROM demo.sales WHERE sales_rep = '" + secretEmail + "' AND 1=1"

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesSvc := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	reportsSvc := service.NewReportsService(pool, appDB, runner, noopLLM{}, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)

	adminCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "admin-1", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})
	analystCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "analyst-1", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAnalyst})
	viewerCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "viewer-1", OrgID: auth.DefaultOrganizationID, Role: auth.RoleViewer})
	otherOrgCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "other-org", OrgID: "11111111-2222-3333-4444-555555555555", Role: auth.RoleAdmin})

	inv, err := invSvc.Create(adminCtx, &investigations.CreateInvestigationPayload{Title: "viewer redaction", SQL: sourceSQL})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := invSvc.AddCandidate(adminCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: candidateSQL}); err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}

	// Sanity check: the fixture really does carry the secret and both SQL
	// texts somewhere in the unredacted (admin) record before any redaction
	// runs — otherwise the leak assertions below would pass for the wrong
	// reason.
	fixture, err := invSvc.Get(adminCtx, &investigations.GetPayload{ID: inv.ID})
	if err != nil {
		t.Fatalf("fixture Get: %v", err)
	}
	fixtureJSON, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{secretEmail, sourceSQL, candidateSQL} {
		if !strings.Contains(string(fixtureJSON), marker) {
			t.Fatalf("fixture setup is broken: %q missing from unredacted investigation: %s", marker, fixtureJSON)
		}
	}

	assertNoMarkers := func(t *testing.T, label string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", label, err)
		}
		for _, marker := range []string{secretEmail, sourceSQL, candidateSQL} {
			if strings.Contains(string(b), marker) {
				t.Errorf("%s: leaked marker %q in response: %s", label, marker, b)
			}
		}
	}
	assertHasMarkers := func(t *testing.T, label string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", label, err)
		}
		for _, marker := range []string{secretEmail, sourceSQL, candidateSQL} {
			if !strings.Contains(string(b), marker) {
				t.Errorf("%s: expected marker %q present for authorized access, missing from: %s", label, marker, b)
			}
		}
	}

	t.Run("viewer Get is redacted", func(t *testing.T) {
		got, err := invSvc.Get(viewerCtx, &investigations.GetPayload{ID: inv.ID})
		if err != nil {
			t.Fatal(err)
		}
		assertNoMarkers(t, "Get as viewer", got)
		if got.SQL != "" || got.CandidateSQL != nil {
			t.Fatalf("viewer must not receive SQL/CandidateSQL fields at all: sql=%q candidateSQL=%v", got.SQL, got.CandidateSQL)
		}
	})

	t.Run("viewer List is redacted", func(t *testing.T) {
		got, err := invSvc.List(viewerCtx, &investigations.ListPayload{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Items) == 0 {
			t.Fatal("expected at least one investigation in the list")
		}
		assertNoMarkers(t, "List as viewer", got)
	})

	t.Run("analyst Get keeps full evidence", func(t *testing.T) {
		got, err := invSvc.Get(analystCtx, &investigations.GetPayload{ID: inv.ID})
		if err != nil {
			t.Fatal(err)
		}
		assertHasMarkers(t, "Get as analyst", got)
	})

	t.Run("analyst List keeps full evidence", func(t *testing.T) {
		got, err := invSvc.List(analystCtx, &investigations.ListPayload{Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		assertHasMarkers(t, "List as analyst", got)
	})

	t.Run("admin keeps full evidence", func(t *testing.T) {
		got, err := invSvc.Get(adminCtx, &investigations.GetPayload{ID: inv.ID})
		if err != nil {
			t.Fatal(err)
		}
		assertHasMarkers(t, "Get as admin", got)
	})

	t.Run("cross-organization access is still a 404, not merely redacted", func(t *testing.T) {
		if _, err := invSvc.Get(otherOrgCtx, &investigations.GetPayload{ID: inv.ID}); err == nil {
			t.Fatal("another organization must not read the investigation at all")
		}
	})
}
