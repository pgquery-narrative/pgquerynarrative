package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	goahttp "goa.design/goa/v3/http"

	schemaServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/schema/server"
	suggestionsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/suggestions/server"
	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	pkgsuggestions "github.com/pgquerynarrative/pgquerynarrative/app/suggestions"
)

func TestSchemaAndSuggestionsE2E(t *testing.T) {
	ctx := context.Background()
	container, connStr := StartPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	WaitPostgres(t, ctx, connStr)
	RunMigrations(t, connStr)
	pool := NewTestPool(t, ctx, connStr)
	defer pool.Close()
	SeedDemoSales(t, ctx, pool)

	loader := catalog.NewLoader(pool, []string{"demo"})
	schemaService := service.NewSchemaService(loader)
	suggester := pkgsuggestions.NewSuggester(pool)
	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	mockLLM := &e2eLLM{response: ""}
	reportsService := service.NewReportsService(pool, pool, runner, mockLLM, config.MetricsConfig{})
	askService := service.NewAskService(pool, loader, mockLLM, validator, reportsService)
	suggestionsService := &service.SuggestionsServiceWrapper{Suggester: suggester, AskSvc: askService}
	schemaEndpoints := schema.NewEndpoints(schemaService)
	suggestionsEndpoints := suggestions.NewEndpoints(suggestionsService)

	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		_ = goahttp.ErrorEncoder(enc, nil)(ctx, w, err)
	}
	schemaServer.Mount(mux, schemaServer.New(schemaEndpoints, mux, dec, enc, errHandler, nil))
	suggestionsServer.Mount(mux, suggestionsServer.New(suggestionsEndpoints, mux, dec, enc, errHandler, nil))
	testServer := httptest.NewServer(mux)
	t.Cleanup(testServer.Close)
	base := testServer.URL

	t.Run("Schema_Get", func(t *testing.T) {
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
		if len(result.Schemas) == 0 {
			t.Fatal("expected at least one schema (demo)")
		}
		var demoFound bool
		var salesTable *schema.TableInfo
		for _, s := range result.Schemas {
			if s.Name == "demo" {
				demoFound = true
				if len(s.Tables) == 0 {
					t.Fatal("expected demo to have tables")
				}
				for _, tbl := range s.Tables {
					if tbl.Name == "sales" {
						salesTable = tbl
						break
					}
				}
				break
			}
		}
		if !demoFound {
			t.Errorf("expected demo schema, got: %v", result.Schemas)
		}
		if salesTable == nil {
			t.Error("expected demo.sales table")
		} else if len(salesTable.Columns) == 0 {
			t.Error("expected demo.sales to have columns")
		}
	})

	t.Run("Suggestions_Queries", func(t *testing.T) {
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
			t.Fatal("expected at least one suggestion (curated)")
		}
	})

	t.Run("Suggestions_Queries_WithIntent", func(t *testing.T) {
		resp, err := http.Get(base + "/api/v1/suggestions/queries?limit=5&intent=sales")
		if err != nil {
			t.Fatalf("suggestions intent: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("suggestions intent status: %d", resp.StatusCode)
		}
		var result suggestions.SuggestedQueriesResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatalf("decode suggestions: %v", err)
		}
		if result.Suggestions == nil {
			t.Error("expected non-nil suggestions")
		}
	})

	t.Run("Suggestions_Similar", func(t *testing.T) {
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
		if result.Suggestions == nil {
			t.Error("similar: expected non-nil suggestions array")
		}
		// Without embeddings, similar may return empty list
		_ = result
	})
}
