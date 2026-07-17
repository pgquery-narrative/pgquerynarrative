// Package e2e provides end-to-end tests against the real HTTP API with a real Postgres (testcontainers).
// setup.go holds shared helpers: start Postgres, run migrations, seed data, and build the full API server.
package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	goahttp "goa.design/goa/v3/http"

	queriesServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/queries/server"
	reportsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/reports/server"
	schemaServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/schema/server"
	suggestionsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/suggestions/server"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
	suggestions "github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	pkgsuggestions "github.com/pgquerynarrative/pgquerynarrative/app/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
)

const (
	postgresWaitTimeout = 15 * time.Second
	postgresPollSleep   = 500 * time.Millisecond
)

// e2eLLM is a mock LLM that returns valid narrative JSON for report generation.
type e2eLLM struct {
	response string
}

func (m *e2eLLM) Generate(ctx context.Context, prompt string) (string, error) {
	if m.response != "" {
		return m.response, nil
	}
	return `{"headline":"E2E test headline","takeaways":["Takeaway one","Takeaway two"],"drivers":[],"limitations":[],"recommendations":[]}`, nil
}

func (m *e2eLLM) Name() string { return "e2e" }

func (m *e2eLLM) Model() string { return "e2e-test-model" }

var _ llm.Client = (*e2eLLM)(nil)

// StartPostgres starts a Postgres container and returns it and the connection string.
// Caller must Terminate the container (e.g. in t.Cleanup).
func StartPostgres(t *testing.T, ctx context.Context) (container *postgres.PostgresContainer, connStr string) {
	t.Helper()
	container = testhelpers.RunPostgresContainer(t, ctx)
	var err error
	connStr, err = container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		_ = container.Terminate(ctx)
		t.Fatalf("connection string: %v", err)
	}
	return container, connStr
}

// WaitPostgres blocks until Postgres at connStr accepts connections or timeout.
func WaitPostgres(t *testing.T, ctx context.Context, connStr string) {
	t.Helper()
	waitCtx, cancel := context.WithTimeout(ctx, postgresWaitTimeout)
	defer cancel()
	for attempt := 0; ; attempt++ {
		pool, pingErr := pgxpool.New(waitCtx, connStr)
		if pingErr == nil {
			pingErr = pool.Ping(waitCtx)
			pool.Close()
			if pingErr == nil {
				return
			}
		}
		if waitCtx.Err() != nil {
			t.Fatalf("postgres not ready after %v: last error %v", postgresWaitTimeout, pingErr)
		}
		time.Sleep(postgresPollSleep)
	}
}

// RunMigrations runs all up migrations from app/db/migrations against connStr.
func RunMigrations(t *testing.T, connStr string) {
	t.Helper()
	absPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatalf("migrations path: %v", err)
	}
	// Enable pgvector so migration 000007_pgvector_embeddings.up.sql can run (E2E uses pgvector/pgvector image).
	extPool, err := pgxpool.New(context.Background(), connStr)
	if err != nil {
		t.Fatalf("pool for extension: %v", err)
	}
	_, _ = extPool.Exec(context.Background(), "CREATE EXTENSION IF NOT EXISTS vector")
	extPool.Close()

	m, err := migrate.New("file://"+absPath, connStr)
	if err != nil {
		t.Fatalf("migrate new: %v", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate up: %v", err)
	}
}

// SeedDemoSales inserts one row into demo.sales for query tests.
func SeedDemoSales(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	_, err := pool.Exec(ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		VALUES (gen_random_uuid(), CURRENT_DATE, 'Electronics', 'Alpha', 5, 10.00, 50.00, 'North', 'A. Lee')
	`)
	if err != nil {
		t.Fatalf("seed demo.sales: %v", err)
	}
}

// NewTestPool creates a pgxpool from connStr. Caller must Close.
func NewTestPool(t *testing.T, ctx context.Context, connStr string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	return pool
}

// FullServerConfig configures the mock LLM response for report generation.
type FullServerConfig struct {
	// MockLLMResponse is the raw string returned by the LLM (valid narrative JSON if empty uses default).
	MockLLMResponse string
}

// withTestPrincipal injects a default-org admin principal so org-scoped services
// and RLS policies work without mounting full auth middleware in E2E.
func withTestPrincipal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auth.WithPrincipal(r.Context(), auth.Principal{
			UserID: "e2e",
			OrgID:  auth.DefaultOrganizationID,
			Role:   auth.RoleAdmin,
		})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// BuildFullServer builds an HTTP test server with all four API services mounted (queries, reports, schema, suggestions).
// Uses a single pool for both read-only and app (testcontainers postgres). Reports use e2eLLM mock.
func BuildFullServer(t *testing.T, ctx context.Context, pool *pgxpool.Pool, cfg FullServerConfig) *httptest.Server {
	t.Helper()

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)

	queriesService := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	llmClient := &e2eLLM{response: cfg.MockLLMResponse}
	reportsService := service.NewReportsService(pool, appDB, runner, llmClient, config.MetricsConfig{})
	loader := catalog.NewLoader(pool, []string{"demo"})
	schemaService := service.NewSchemaService(loader)
	suggester := pkgsuggestions.NewSuggester(appDB)
	askService := service.NewAskService(appDB, loader, llmClient, validator, reportsService)
	suggestionsService := &service.SuggestionsServiceWrapper{Suggester: suggester, AskSvc: askService}

	queriesEndpoints := queries.NewEndpoints(queriesService)
	reportsEndpoints := reports.NewEndpoints(reportsService)
	schemaEndpoints := schema.NewEndpoints(schemaService)
	suggestionsEndpoints := suggestions.NewEndpoints(suggestionsService)

	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		_ = goahttp.ErrorEncoder(enc, nil)(ctx, w, err)
	}

	queriesHTTP := queriesServer.New(queriesEndpoints, mux, dec, enc, errHandler, nil)
	queriesServer.Mount(mux, queriesHTTP)
	reportsServer.Mount(mux, reportsServer.New(reportsEndpoints, mux, dec, enc, errHandler, nil))
	schemaServer.Mount(mux, schemaServer.New(schemaEndpoints, mux, dec, enc, errHandler, nil))
	suggestionsServer.Mount(mux, suggestionsServer.New(suggestionsEndpoints, mux, dec, enc, errHandler, nil))

	combined := http.NewServeMux()
	combined.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	combined.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	combined.Handle("/", withTestPrincipal(mux))

	srv := httptest.NewServer(combined)
	t.Cleanup(srv.Close)
	return srv
}
