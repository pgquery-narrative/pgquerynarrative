package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	goahttp "goa.design/goa/v3/http"

	investigationsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/investigations/server"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
)

// TestInvestigationsE2E_CreateFromRegressionConcurrent hits
// POST /api/v1/investigations/from-regression over real HTTP against a real
// Postgres, exercising the compare-and-swap alert-to-investigation claim
// (app/service/investigations.go CreateFromRegression) end to end. Prior to
// this test the CAS logic was only unit-tested within package service — never
// through the HTTP API, and never under real concurrency.
func TestInvestigationsE2E_CreateFromRegressionConcurrent(t *testing.T) {
	ctx := context.Background()
	container, connStr := StartPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	WaitPostgres(t, ctx, connStr)
	RunMigrations(t, connStr)
	pool := NewTestPool(t, ctx, connStr)
	defer pool.Close()
	SeedDemoSales(t, ctx, pool)

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesService := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	llmClient := &e2eLLM{}
	reportsService := service.NewReportsService(pool, appDB, runner, llmClient, config.MetricsConfig{})
	investigationsService := service.NewInvestigationsService(appDB, queriesService, reportsService)
	endpoints := investigations.NewEndpoints(investigationsService)

	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		_ = goahttp.ErrorEncoder(enc, nil)(ctx, w, err)
	}
	investigationsServer.Mount(mux, investigationsServer.New(endpoints, mux, dec, enc, errHandler, nil))
	testServer := httptest.NewServer(withTestPrincipal(mux))
	t.Cleanup(testServer.Close)
	base := testServer.URL

	// Seed one regression alert directly (the poller's own detection logic is
	// covered elsewhere; this test is about the claim, not detection). The
	// test pool connects as the Postgres superuser (testhelpers.RunPostgresContainer),
	// which bypasses the table's RLS policy, so no SET LOCAL org context is
	// needed for the raw insert.
	var alertID string
	err := pool.QueryRow(ctx, `
		INSERT INTO app.regression_alerts (
			organization_id, title, query_text, change_type, change_summary, impact, connection_id
		) VALUES (
			$1::uuid, 'concurrent-claim-test',
			'SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category',
			'latency', 'mean time up 250%', 'high', 'default'
		) RETURNING id
	`, auth.DefaultOrganizationID).Scan(&alertID)
	if err != nil {
		t.Fatalf("seed regression alert: %v", err)
	}

	// Fire two concurrent from-regression requests for the same alert — the
	// scenario the CAS claim in CreateFromRegression exists to handle: two
	// callers (e.g. two browser tabs, or a retry racing the original request)
	// both see investigation_id IS NULL and both start creating an
	// investigation before either commits its claim.
	const concurrency = 2
	type result struct {
		id     string
		status int
		err    error
	}
	results := make([]result, concurrency)
	var wg sync.WaitGroup
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func(i int) {
			defer wg.Done()
			body, _ := json.Marshal(map[string]string{"regression_alert_id": alertID})
			resp, err := http.Post(base+"/api/v1/investigations/from-regression", "application/json", bytes.NewReader(body))
			if err != nil {
				results[i] = result{err: err}
				return
			}
			defer resp.Body.Close()
			var inv investigations.Investigation
			decodeErr := json.NewDecoder(resp.Body).Decode(&inv)
			results[i] = result{id: inv.ID, status: resp.StatusCode, err: decodeErr}
		}(i)
	}
	wg.Wait()

	seenIDs := map[string]bool{}
	for i, r := range results {
		if r.err != nil {
			t.Fatalf("request %d: transport/decode error: %v", i, r.err)
		}
		if r.status != http.StatusOK {
			t.Fatalf("request %d: status = %d, want 200", i, r.status)
		}
		if r.id == "" {
			t.Fatalf("request %d: empty investigation id", i)
		}
		seenIDs[r.id] = true
	}
	if len(seenIDs) != 1 {
		t.Fatalf("expected both concurrent from-regression calls to resolve to the same investigation, got %d distinct ids: %v", len(seenIDs), seenIDs)
	}

	// Exactly one investigation row must exist for this alert's title — the
	// loser's investigation must have been discarded (CreateFromRegression's
	// DELETE ... WHERE report_id IS NULL), not left orphaned. The link lives
	// on app.regression_alerts.investigation_id, not the other direction.
	var investigationCount int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM app.investigations WHERE title = 'concurrent-claim-test'
	`).Scan(&investigationCount); err != nil {
		t.Fatalf("count investigations for alert: %v", err)
	}
	if investigationCount != 1 {
		t.Fatalf("expected exactly 1 investigation for the alert (loser must be discarded, not orphaned), got %d", investigationCount)
	}

	var linkedInvestigationID string
	if err := pool.QueryRow(ctx, `
		SELECT investigation_id FROM app.regression_alerts WHERE id = $1
	`, alertID).Scan(&linkedInvestigationID); err != nil {
		t.Fatalf("read alert link: %v", err)
	}
	for id := range seenIDs {
		if linkedInvestigationID != id {
			t.Fatalf("alert linked investigation %s does not match the id returned to callers %s", linkedInvestigationID, id)
		}
	}
}
