package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
)

// TestFullStackE2E runs all API areas against a single Postgres container and one server
// with queries, reports, schema, and suggestions mounted.
func TestFullStackE2E(t *testing.T) {
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

	t.Run("Health", func(t *testing.T) {
		resp, err := http.Get(base + "/health")
		if err != nil {
			t.Fatalf("health: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("health status: %d", resp.StatusCode)
		}
	})

	t.Run("Ready", func(t *testing.T) {
		resp, err := http.Get(base + "/ready")
		if err != nil {
			t.Fatalf("ready: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("ready status: %d", resp.StatusCode)
		}
	})

	t.Run("RunQuery", func(t *testing.T) {
		payload := map[string]interface{}{"sql": "SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category", "limit": 100}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/queries/run", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post run: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("run status: %d", resp.StatusCode)
		}
		var result queries.RunQueryResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode run: %v", err)
		}
		if len(result.Columns) == 0 {
			t.Error("run result: expected columns")
		}
		if result.RowCount < 0 {
			t.Errorf("run result: row_count %d", result.RowCount)
		}
	})

	t.Run("RunQuery_InvalidSQL", func(t *testing.T) {
		payload := map[string]interface{}{"sql": "SELECT * FROM nonexistent.foo", "limit": 100}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/queries/run", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post run: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("run invalid sql: want 400, got %d", resp.StatusCode)
		}
	})

	t.Run("SaveListGetDelete", func(t *testing.T) {
		savePayload := map[string]interface{}{"name": "Full stack saved", "sql": "SELECT * FROM demo.sales LIMIT 1", "tags": []string{}}
		saveBody, _ := json.Marshal(savePayload)
		resp, err := http.Post(base+"/api/v1/queries/saved", "application/json", bytes.NewReader(saveBody))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("save status: %d", resp.StatusCode)
		}
		var saved queries.SavedQuery
		if err := json.NewDecoder(resp.Body).Decode(&saved); err != nil {
			t.Fatalf("decode save: %v", err)
		}
		if saved.ID == "" {
			t.Fatal("save: expected id")
		}

		listResp, err := http.Get(base + "/api/v1/queries/saved")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		listResp.Body.Close()
		if listResp.StatusCode != http.StatusOK {
			t.Fatalf("list status: %d", listResp.StatusCode)
		}

		getResp, err := http.Get(base + "/api/v1/queries/saved/" + saved.ID)
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer getResp.Body.Close()
		if getResp.StatusCode != http.StatusOK {
			t.Fatalf("get status: %d", getResp.StatusCode)
		}
		var got queries.SavedQuery
		if err := json.NewDecoder(getResp.Body).Decode(&got); err != nil {
			t.Fatalf("decode get: %v", err)
		}
		if got.ID != saved.ID {
			t.Errorf("get id: %q want %q", got.ID, saved.ID)
		}

		delReq, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/queries/saved/"+saved.ID, nil)
		delResp, err := http.DefaultClient.Do(delReq)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		delResp.Body.Close()
		if delResp.StatusCode != http.StatusOK && delResp.StatusCode != http.StatusNoContent {
			t.Fatalf("delete status: %d", delResp.StatusCode)
		}

		getAgain, err := http.Get(base + "/api/v1/queries/saved/" + saved.ID)
		if err != nil {
			t.Fatalf("get after delete: %v", err)
		}
		getAgain.Body.Close()
		if getAgain.StatusCode != http.StatusNotFound {
			t.Errorf("get after delete: want 404, got %d", getAgain.StatusCode)
		}
	})

	t.Run("QueriesGetSaved_NotFound", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/queries/saved/00000000-0000-0000-0000-000000000000")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("get non-existent saved: want 404 or 400, got %d", resp.StatusCode)
		}
	})

	t.Run("QueriesDeleteSaved_NotFound", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/queries/saved/00000000-0000-0000-0000-000000000000", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("delete: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("delete non-existent saved: want 404 or 400, got %d", resp.StatusCode)
		}
	})

	t.Run("QueriesList_WithTags", func(t *testing.T) {
		savePayload := map[string]interface{}{"name": "FS Tagged", "sql": "SELECT 1 FROM demo.sales LIMIT 1", "tags": []string{"fullstack-tag"}}
		saveBody, _ := json.Marshal(savePayload)
		saveResp, err := http.Post(base+"/api/v1/queries/saved", "application/json", bytes.NewReader(saveBody))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		saveResp.Body.Close()
		if saveResp.StatusCode != http.StatusOK {
			t.Fatalf("save status: %d", saveResp.StatusCode)
		}
		resp, err := http.Get(base + "/api/v1/queries/saved?tags=fullstack-tag&limit=10&offset=0")
		if err != nil {
			t.Fatalf("list with tags: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list with tags status: %d", resp.StatusCode)
		}
		var list queries.SavedQueryList
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		if len(list.Items) == 0 {
			t.Error("list with tags: expected at least one item")
		}
	})

	t.Run("Schema", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/schema")
		if err != nil {
			t.Fatalf("schema: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("schema status: %d", resp.StatusCode)
		}
		var result schema.SchemaResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode schema: %v", err)
		}
		var demoFound bool
		for _, s := range result.Schemas {
			if s.Name == "demo" {
				demoFound = true
				if len(s.Tables) == 0 {
					t.Error("demo schema: expected tables")
				}
				break
			}
		}
		if !demoFound {
			t.Error("schema: expected demo schema")
		}
	})

	t.Run("SuggestionsQueries", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/suggestions/queries?limit=5")
		if err != nil {
			t.Fatalf("suggestions: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("suggestions status: %d", resp.StatusCode)
		}
		var result suggestions.SuggestedQueriesResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode suggestions: %v", err)
		}
		if len(result.Suggestions) == 0 {
			t.Error("suggestions: expected at least one")
		}
	})

	t.Run("SuggestionsSimilar", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/suggestions/similar?text=sales%20by%20category&limit=5")
		if err != nil {
			t.Fatalf("similar: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("similar status: %d", resp.StatusCode)
		}
		var result suggestions.SuggestedQueriesResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode similar: %v", err)
		}
		// Similar may be empty when embeddings are not configured
		if result.Suggestions == nil {
			t.Error("similar: expected non-nil suggestions array")
		}
	})

	t.Run("ReportsListAndGet", func(t *testing.T) {
		listResp, err := http.Get(base + "/api/v1/reports?limit=10&offset=0")
		if err != nil {
			t.Fatalf("list reports: %v", err)
		}
		defer listResp.Body.Close()
		if listResp.StatusCode != http.StatusOK {
			t.Fatalf("list reports status: %d", listResp.StatusCode)
		}
		var listResult reports.ReportList
		if err := json.NewDecoder(listResp.Body).Decode(&listResult); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		_ = listResult
	})

	t.Run("ReportsList_WithSavedQueryID", func(t *testing.T) {
		// Create saved query and report via DB, then list by saved_query_id
		var savedQueryID string
		err := pool.QueryRow(ctx, `
			INSERT INTO app.saved_queries (id, name, sql, tags)
			VALUES (gen_random_uuid(), 'FS report query', 'SELECT 1', ARRAY['fs'])
			RETURNING id
		`).Scan(&savedQueryID)
		if err != nil {
			t.Fatalf("insert saved query: %v", err)
		}
		_, err = pool.Exec(ctx, `
			INSERT INTO app.reports (sql, narrative_md, narrative_json, metrics, llm_model, llm_provider, saved_query_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
		`, "SELECT 1", "FS Linked", []byte(`{"headline":"FS","takeaways":["One"],"drivers":[],"limitations":[],"recommendations":[]}`), []byte(`{}`), "test", "e2e", savedQueryID)
		if err != nil {
			t.Fatalf("insert report: %v", err)
		}
		resp, err := http.Get(base + "/api/v1/reports?saved_query_id=" + savedQueryID + "&limit=10&offset=0")
		if err != nil {
			t.Fatalf("list: %v", err)
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

	t.Run("ReportsGenerate", func(t *testing.T) {
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

	t.Run("ReportsGenerate_ValidationError", func(t *testing.T) {
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

	t.Run("ReportNotFound", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/reports/00000000-0000-0000-0000-000000000000")
		if err != nil {
			t.Fatalf("get missing: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("get missing report: want 404 or 400, got %d", resp.StatusCode)
		}
	})
}
