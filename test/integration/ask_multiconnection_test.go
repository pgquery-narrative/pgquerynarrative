package integration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/suggestions"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// fixedSQLLLM always returns the same SQL text regardless of prompt, so a test
// can assert exactly which connection's validator/execution path a request
// reached without depending on real model output.
type fixedSQLLLM struct{ sql string }

func (f fixedSQLLLM) Generate(context.Context, string) (string, error) { return f.sql, nil }
func (fixedSQLLLM) Name() string                                       { return "integration-test" }

// TestAskMultiConnection_ValidatesAgainstRequestedConnection is a regression
// test for a bug where Ask and Chat validated LLM-generated SQL against the
// default connection's schema allowlist regardless of which connection_id was
// requested (app/service/ask.go called connectionResolver.runnerFor(nil)
// instead of runnerFor(payload.ConnectionID)). Two connections are registered
// against the same database with different allowed schemas; a query that is
// only valid for the non-default connection must not be rejected by the
// default connection's policy.
func TestAskMultiConnection_ValidatesAgainstRequestedConnection(t *testing.T) {
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

	// A second schema, disjoint from "demo", so the two connections' allowed
	// schemas cannot be confused for one another.
	if _, err := pool.Exec(ctx, `CREATE SCHEMA IF NOT EXISTS conn_b_only`); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := pool.Exec(ctx, `CREATE TABLE IF NOT EXISTS conn_b_only.widgets (id int)`); err != nil {
		t.Fatalf("create table: %v", err)
	}

	defaultValidator := queryrunner.NewValidator([]string{"demo"}, 10000)
	connBValidator := queryrunner.NewValidator([]string{"conn_b_only"}, 10000)
	runners := map[string]*queryrunner.Runner{
		"default": queryrunner.NewRunner(pool, defaultValidator, 1000, 30*time.Second),
		"conn_b":  queryrunner.NewRunner(pool, connBValidator, 1000, 30*time.Second),
	}
	loaders := map[string]*catalog.Loader{
		"default": catalog.NewLoader(pool, []string{"demo"}),
		"conn_b":  catalog.NewLoader(pool, []string{"conn_b_only"}),
	}

	appDB := db.NewOrgScoped(pool)
	sql := "SELECT id FROM conn_b_only.widgets"
	var llmClient llm.Client = fixedSQLLLM{sql: sql}

	reportsSvc := service.NewReportsServiceMultiConnection(appDB, runners, "default", llmClient, config.MetricsConfig{}, nil, nil)
	askSvc := service.NewAskServiceMultiConnection(appDB, loaders, llmClient, defaultValidator, reportsSvc, "default")

	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "test", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})
	connB := "conn_b"

	t.Run("Ask", func(t *testing.T) {
		result, err := askSvc.Ask(reqCtx, &suggestions.AskPayload{Question: "widgets", ConnectionID: &connB})
		if err != nil {
			t.Fatalf("Ask against conn_b (schema conn_b_only) was rejected: %v; "+
				"pre-flight validation likely ran against the default connection's "+
				"schema policy (demo) instead of the requested connection's (conn_b_only)", err)
		}
		if !strings.Contains(result.SQL, "conn_b_only.widgets") {
			t.Fatalf("expected generated SQL to reference conn_b_only.widgets, got %q", result.SQL)
		}
	})

	t.Run("Chat", func(t *testing.T) {
		result, err := askSvc.Chat(reqCtx, &suggestions.ChatPayload{Question: "widgets", ConnectionID: &connB})
		if err != nil {
			t.Fatalf("Chat against conn_b (schema conn_b_only) was rejected: %v; "+
				"pre-flight validation likely ran against the default connection's "+
				"schema policy (demo) instead of the requested connection's (conn_b_only)", err)
		}
		if !strings.Contains(result.SQL, "conn_b_only.widgets") {
			t.Fatalf("expected generated SQL to reference conn_b_only.widgets, got %q", result.SQL)
		}
	})
}
