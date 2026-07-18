package integration

import (
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestAuditBuffered_PersistAndReplay(t *testing.T) {
	pool, ctx := setupMigratedPool(t)
	store := audit.NewStore(pool, audit.ModeBuffered)
	t.Cleanup(store.Close)

	org := auth.DefaultOrganizationID
	entry := audit.Entry{
		EventType:  audit.EventRunQuery,
		EntityType: "query",
		UserID:     "audit-worker",
		OrgID:      org,
		HighRisk:   true,
		Details:    map[string]interface{}{"sql_hash": "abc"},
	}
	if err := store.Record(ctx, entry); err != nil {
		t.Fatalf("Record: %v", err)
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		var n int
		if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM app.audit_logs WHERE event_type = $1 AND user_id = $2`,
			audit.EventRunQuery, "audit-worker").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n >= 1 {
			break
		}
		if time.Now().After(deadline) {
			// Force replay in case the background ticker hasn't fired yet.
			replayed, _, err := store.ReplayBuffered(ctx, 10)
			if err != nil {
				t.Fatalf("ReplayBuffered: %v", err)
			}
			if replayed == 0 {
				t.Fatal("timed out waiting for buffered audit entry to land in audit_logs")
			}
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestAuditRequired_WritesSynchronously(t *testing.T) {
	pool, ctx := setupMigratedPool(t)
	store := audit.NewStore(pool, audit.ModeRequired)
	t.Cleanup(store.Close)

	if err := store.Record(ctx, audit.Entry{
		EventType: audit.EventGenerateReport,
		UserID:    "sync-auditor",
		OrgID:     auth.DefaultOrganizationID,
		HighRisk:  true,
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM app.audit_logs WHERE user_id = $1`, "sync-auditor").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 audit row, got %d", n)
	}
}
