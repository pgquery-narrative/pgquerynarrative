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

	queriesServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/queries/server"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
)

func TestQueriesE2E(t *testing.T) {
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
	queriesService := service.NewQueriesService(pool, pool, runner, config.MetricsConfig{})
	endpoints := queries.NewEndpoints(queriesService)

	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		_ = goahttp.ErrorEncoder(enc, nil)(ctx, w, err)
	}
	queriesServer.Mount(mux, queriesServer.New(endpoints, mux, dec, enc, errHandler, nil))
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)
	base := testServer.URL

	t.Run("Run_Ok", func(t *testing.T) {
		payload := map[string]interface{}{
			"sql":   "SELECT product_category, SUM(total_amount) AS total FROM demo.sales GROUP BY product_category",
			"limit": 100,
		}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/queries/run", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("run request: %v", err)
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
			t.Error("run: expected columns")
		}
		if result.RowCount < 0 {
			t.Errorf("run: row_count %d", result.RowCount)
		}
		if len(result.Rows) > 0 && result.RowCount > 0 && len(result.Rows) != int(result.RowCount) {
			t.Errorf("run: len(rows)=%d row_count=%d", len(result.Rows), result.RowCount)
		}
	})

	t.Run("Run_InvalidSQL", func(t *testing.T) {
		payload := map[string]interface{}{"sql": "SELECT * FROM nonexistent.foo", "limit": 100}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/queries/run", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("run request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("run invalid sql: want 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Run_DisallowedSchema", func(t *testing.T) {
		// Validator allows only "demo"; public schema is disallowed when not in list
		payload := map[string]interface{}{"sql": "SELECT * FROM public.pg_database LIMIT 1", "limit": 10}
		body, _ := json.Marshal(payload)
		resp, err := http.Post(base+"/api/v1/queries/run", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("run request: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("run disallowed schema: want 400, got %d", resp.StatusCode)
		}
	})

	t.Run("SaveListGetDelete", func(t *testing.T) {
		savePayload := map[string]interface{}{
			"name": "Sample Query",
			"sql":  "SELECT * FROM demo.sales",
			"tags": []string{"demo"},
		}
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
		if got.ID != saved.ID || got.SQL != saved.SQL {
			t.Errorf("get: id=%q sql=%q, want id=%q sql=%q", got.ID, got.SQL, saved.ID, saved.SQL)
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

	t.Run("List_LimitOffset", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/queries/saved?limit=5&offset=0")
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("list limit/offset status: %d", resp.StatusCode)
		}
		var list queries.SavedQueryList
		if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
			t.Fatalf("decode list: %v", err)
		}
		if list.Items == nil {
			t.Error("list: expected non-nil items")
		}
	})

	t.Run("List_WithTags", func(t *testing.T) {
		// Save a query with a specific tag, then list by that tag
		savePayload := map[string]interface{}{"name": "Tagged Query", "sql": "SELECT 1 FROM demo.sales LIMIT 1", "tags": []string{"e2e-tag"}}
		saveBody, _ := json.Marshal(savePayload)
		saveResp, err := http.Post(base+"/api/v1/queries/saved", "application/json", bytes.NewReader(saveBody))
		if err != nil {
			t.Fatalf("save: %v", err)
		}
		saveResp.Body.Close()
		if saveResp.StatusCode != http.StatusOK {
			t.Fatalf("save status: %d", saveResp.StatusCode)
		}
		resp, err := http.Get(base + "/api/v1/queries/saved?tags=e2e-tag&limit=10&offset=0")
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
			t.Error("list with tags: expected at least one item when filtering by e2e-tag")
		}
	})

	t.Run("GetSaved_NotFound", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/queries/saved/00000000-0000-0000-0000-000000000000")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusBadRequest {
			t.Errorf("get non-existent saved: want 404 or 400, got %d", resp.StatusCode)
		}
	})

	t.Run("DeleteSaved_NotFound", func(t *testing.T) {
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
}
