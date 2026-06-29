package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	goahttp "goa.design/goa/v3/http"

	reportsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/reports/server"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
)

// mockLLMReports is used by reports E2E for List/Get when Generate is not called.
type mockLLMReports struct{}

func (m *mockLLMReports) Generate(ctx context.Context, prompt string) (string, error) {
	return "", nil
}
func (m *mockLLMReports) Name() string { return "test" }

var _ llm.Client = (*mockLLMReports)(nil)

func TestReportsListAndGetE2E(t *testing.T) {
	ctx := context.Background()
	container, connStr := StartPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	WaitPostgres(t, ctx, connStr)
	RunMigrations(t, connStr)
	pool := NewTestPool(t, ctx, connStr)
	defer pool.Close()

	// Insert one report for List/Get
	narrativeJSON := []byte(`{"headline":"E2E test report","takeaways":["One insight"],"drivers":[],"limitations":[],"recommendations":[]}`)
	metricsJSON := []byte(`{"aggregates":{},"data_quality":{},"time_series":{},"perf_suggestions":[]}`)
	var reportID string
	err := pool.QueryRow(ctx, `
		INSERT INTO app.reports (sql, narrative_md, narrative_json, metrics, llm_model, llm_provider)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id
	`, "SELECT 1", "E2E test report", narrativeJSON, metricsJSON, "test", "e2e").Scan(&reportID)
	if err != nil {
		t.Fatalf("insert report: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	reportsService := service.NewReportsService(pool, pool, runner, &mockLLMReports{}, config.MetricsConfig{})
	endpoints := reports.NewEndpoints(reportsService)

	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		_ = goahttp.ErrorEncoder(enc, nil)(ctx, w, err)
	}
	reportsServer.Mount(mux, reportsServer.New(endpoints, mux, dec, enc, errHandler, nil))
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)
	base := testServer.URL

	t.Run("List", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/reports?limit=10&offset=0")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list status: %d", resp.StatusCode)
		}
		var listResult reports.ReportList
		if err := json.NewDecoder(resp.Body).Decode(&listResult); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		if len(listResult.Items) == 0 {
			t.Fatal("expected at least one report")
		}
		if listResult.Items[0].ID != reportID {
			t.Errorf("list first id = %q, want %q", listResult.Items[0].ID, reportID)
		}
	})

	t.Run("List_Offset", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/reports?limit=5&offset=1")
		if err != nil {
			t.Fatalf("list offset: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list offset status: %d", resp.StatusCode)
		}
		var listResult reports.ReportList
		if err := json.NewDecoder(resp.Body).Decode(&listResult); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		// With offset=1 we may get 0 items (only one report inserted)
		_ = listResult
	})

	t.Run("Get", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/reports/" + reportID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("get status: %d", resp.StatusCode)
		}
		var report reports.Report
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
			t.Fatalf("decode get: %v", err)
		}
		if report.ID != reportID {
			t.Errorf("get id = %q, want %q", report.ID, reportID)
		}
		if report.SQL != "SELECT 1" {
			t.Errorf("get sql = %q, want SELECT 1", report.SQL)
		}
		if report.Narrative == nil || report.Narrative.Headline != "E2E test report" {
			t.Errorf("get narrative = %+v", report.Narrative)
		}
	})

	t.Run("Get_NotFound", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/reports/00000000-0000-0000-0000-000000000000")
		if err != nil {
			t.Fatalf("get missing: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("get missing: want 404 or 400, got %d", resp.StatusCode)
		}
	})

	t.Run("List_WithSavedQueryID", func(t *testing.T) {
		// Insert a saved query and a report linked to it, then list by saved_query_id
		var savedQueryID string
		err := pool.QueryRow(ctx, `
			INSERT INTO app.saved_queries (id, name, sql, tags)
			VALUES (gen_random_uuid(), 'Report E2E query', 'SELECT 1', ARRAY['e2e'])
			RETURNING id
		`).Scan(&savedQueryID)
		if err != nil {
			t.Fatalf("insert saved query: %v", err)
		}
		narrJSON := []byte(`{"headline":"Linked report","takeaways":["One"],"drivers":[],"limitations":[],"recommendations":[]}`)
		_, err = pool.Exec(ctx, `
			INSERT INTO app.reports (sql, narrative_md, narrative_json, metrics, llm_model, llm_provider, saved_query_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, "SELECT 1", "Linked", narrJSON, []byte(`{}`), "test", "e2e", savedQueryID)
		if err != nil {
			t.Fatalf("insert report with saved_query_id: %v", err)
		}
		resp, err := http.Get(base + "/api/v1/reports?saved_query_id=" + savedQueryID + "&limit=10&offset=0")
		if err != nil {
			t.Fatalf("list with saved_query_id: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list with saved_query_id status: %d", resp.StatusCode)
		}
		var listResult reports.ReportList
		if err := json.NewDecoder(resp.Body).Decode(&listResult); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		if len(listResult.Items) == 0 {
			t.Error("list with saved_query_id: expected at least one report")
		}
	})
}

func TestReportsGenerateE2E(t *testing.T) {
	ctx := context.Background()
	container, connStr := StartPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	WaitPostgres(t, ctx, connStr)
	RunMigrations(t, connStr)
	pool := NewTestPool(t, ctx, connStr)
	defer pool.Close()
	SeedDemoSales(t, ctx, pool)

	srv := BuildFullServer(t, ctx, pool, FullServerConfig{})
	base := srv.URL

	t.Run("Generate", func(t *testing.T) {
		payload := map[string]interface{}{
			"sql": "SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category",
		}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/reports/generate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("generate status: %d", resp.StatusCode)
		}
		var report reports.Report
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
			t.Fatalf("decode report: %v", err)
		}
		if report.ID == "" {
			t.Error("generate: expected report id")
		}
		if report.Narrative == nil || report.Narrative.Headline == "" {
			t.Error("generate: expected narrative headline")
		}
		if len(report.Narrative.Takeaways) == 0 {
			t.Error("generate: expected takeaways")
		}
	})

	t.Run("Generate_ValidationError", func(t *testing.T) {
		// Disallowed schema or invalid SQL should return 400
		payload := map[string]interface{}{"sql": "SELECT * FROM public.pg_database LIMIT 1"}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/reports/generate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("generate disallowed schema: want 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Generate_Cohorts", func(t *testing.T) {
		// Query with cohort dimension (column name contains "cohort") and period dimension -> metrics.cohorts populated.
		// Use text for both so profiler marks them as dimensions; one measure.
		payload := map[string]interface{}{
			"sql": "SELECT to_char(date_trunc('month', date), 'YYYY-MM') AS cohort_month, '0' AS period_label, SUM(total_amount) AS revenue FROM demo.sales GROUP BY date_trunc('month', date) ORDER BY cohort_month",
		}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/reports/generate", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("generate cohort: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("generate cohort status: %d", resp.StatusCode)
		}
		var report reports.Report
		if err := json.NewDecoder(resp.Body).Decode(&report); err != nil {
			t.Fatalf("decode cohort report: %v", err)
		}
		if report.Metrics == nil {
			t.Fatal("generate cohort: expected metrics")
		}
		if len(report.Metrics.Cohorts) == 0 {
			t.Error("generate cohort: expected at least one cohort in metrics.cohorts")
		}
	})
}
