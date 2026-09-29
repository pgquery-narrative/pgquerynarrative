package integration

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/internal/audit"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// TestAuditStoreRecord verifies that audit.Store.Record writes entries to app.audit_logs
// and they can be read back (integration with real Postgres).
func TestAuditStoreRecord(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
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

	helpers.RunMigrations(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatalf("new pool: %v", err)
	}
	defer pool.Close()

	store := audit.NewStore(pool, audit.ModeBestEffort)
	t.Cleanup(store.Close)

	if err := store.Record(ctx, audit.Entry{
		EventType: audit.EventAPIRequest,
		Details:   map[string]interface{}{"path": "/api/v1/queries/saved", "status_code": 200},
		UserID:    "api-key",
		IP:        "127.0.0.1",
		UserAgent: "integration-test",
	}); err != nil {
		t.Fatalf("record API_REQUEST: %v", err)
	}
	if err := store.Record(ctx, audit.Entry{
		EventType: audit.EventAuthFailure,
		Details:   map[string]interface{}{"path": "/api/v1/reports"},
		IP:        "10.0.0.1",
		UserAgent: "curl",
	}); err != nil {
		t.Fatalf("record AUTH_FAILURE: %v", err)
	}

	var count int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM app.audit_logs WHERE event_type = $1`, audit.EventAPIRequest).Scan(&count)
	if err != nil {
		t.Fatalf("count API_REQUEST: %v", err)
	}
	if count < 1 {
		t.Errorf("expected at least one API_REQUEST row, got %d", count)
	}

	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM app.audit_logs WHERE event_type = $1`, audit.EventAuthFailure).Scan(&count)
	if err != nil {
		t.Fatalf("count AUTH_FAILURE: %v", err)
	}
	if count < 1 {
		t.Errorf("expected at least one AUTH_FAILURE row, got %d", count)
	}

	var userID string
	err = pool.QueryRow(ctx, `SELECT user_id FROM app.audit_logs WHERE event_type = $1 LIMIT 1`, audit.EventAPIRequest).Scan(&userID)
	if err != nil {
		t.Fatalf("select user_id: %v", err)
	}
	if userID != "api-key" {
		t.Errorf("user_id = %q, want api-key", userID)
	}
}
