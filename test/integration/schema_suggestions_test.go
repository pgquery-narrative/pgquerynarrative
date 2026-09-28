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

	suggestionsgen "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/internal/catalog"
	"github.com/pgquerynarrative/pgquerynarrative/internal/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// TestCatalogAndSuggestionsIntegration verifies schema (catalog) and query
// suggestions against a real Postgres with migrations. Used by Phase 4 – MCP
// backend API coverage.
func TestCatalogAndSuggestionsIntegration(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("failed to get connection string: %v", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var lastErr error
	for {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				break
			}
			lastErr = pingErr
		} else {
			lastErr = pingErr
		}
		if waitCtx.Err() != nil {
			t.Fatalf("postgres not ready after 15s: %v", lastErr)
		}
		time.Sleep(500 * time.Millisecond)
	}

	migrationsPath, err := filepath.Abs("../../internal/db/migrations")
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

	// --- Catalog: schema discovery (allowed schemas = demo)
	loader := catalog.NewLoader(pool, []string{"demo"})
	schemaRes, err := loader.Load(ctx)
	if err != nil {
		t.Fatalf("catalog Load: %v", err)
	}
	if len(schemaRes.Schemas) == 0 {
		t.Fatal("expected at least one schema (demo)")
	}
	var demoFound bool
	for _, s := range schemaRes.Schemas {
		if s.Name == "demo" {
			demoFound = true
			if len(s.Tables) == 0 {
				t.Fatal("expected demo schema to have tables")
			}
			var salesFound bool
			for _, tbl := range s.Tables {
				if tbl.Name == "sales" {
					salesFound = true
					if len(tbl.Columns) == 0 {
						t.Fatal("expected sales table to have columns")
					}
					break
				}
			}
			if !salesFound {
				t.Fatalf("expected demo.sales table, got tables: %v", s.Tables)
			}
			break
		}
	}
	if !demoFound {
		t.Fatalf("expected demo schema, got: %v", schemaRes.Schemas)
	}

	// --- Suggestions: curated only (no saved queries yet)
	suggester := suggestions.NewSuggester(pool)
	payload := &suggestionsgen.QueriesPayload{Limit: 5}
	res, err := suggester.Queries(ctx, payload)
	if err != nil {
		t.Fatalf("suggestions Queries: %v", err)
	}
	if len(res.Suggestions) == 0 {
		t.Fatal("expected at least one curated suggestion")
	}
	for _, s := range res.Suggestions {
		if s.Source != "curated" {
			t.Errorf("expected curated only (no saved yet), got source %q", s.Source)
		}
	}

	// --- Insert a saved query and verify intent match
	_, err = pool.Exec(ctx, `
		INSERT INTO app.saved_queries (name, sql, description)
		VALUES ('Regional sales', 'SELECT region, SUM(total_amount) FROM demo.sales GROUP BY region', 'Sales by region')
	`)
	if err != nil {
		t.Fatalf("insert saved query: %v", err)
	}
	res2, err := suggester.Queries(ctx, &suggestionsgen.QueriesPayload{Intent: strPtr("region"), Limit: 5})
	if err != nil {
		t.Fatalf("suggestions Queries with intent: %v", err)
	}
	var hasSaved bool
	for _, s := range res2.Suggestions {
		if s.Source == "saved" {
			hasSaved = true
			break
		}
	}
	if !hasSaved {
		t.Error("expected at least one suggestion with source=saved when intent matches saved query")
	}
}

func strPtr(s string) *string { return &s }
