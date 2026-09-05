package service

import (
	"context"
	"testing"

	reports "github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

// applySQLVisibility is the last gate between a stored query and whoever is
// reading the report, so every role's outcome is pinned here. auditStore is left
// nil throughout: the recording path is exercised by the integration suite, and
// leaving it nil keeps these assertions about redaction alone.
func TestApplySQLVisibility(t *testing.T) {
	const rawSQL = "SELECT customer_email FROM demo.sales WHERE internal_margin > 0.4"

	ctxAs := func(role, userID string) context.Context {
		return auth.WithPrincipal(context.Background(), auth.Principal{
			UserID: userID, OrgID: auth.DefaultOrganizationID, Role: role,
		})
	}
	report := func() *reports.Report {
		return &reports.Report{ID: "rep-1", SQL: rawSQL}
	}

	t.Run("a nil report is a no-op", func(t *testing.T) {
		svc := &ReportsService{}
		svc.applySQLVisibility(ctxAs(auth.RoleAdmin, "u1"), nil) // must not panic
	})

	t.Run("admins and analysts keep the SQL", func(t *testing.T) {
		for _, role := range []string{auth.RolePlatformAdmin, auth.RoleTenantAdmin, auth.RoleAnalyst} {
			r := report()
			svc := &ReportsService{}
			svc.applySQLVisibility(ctxAs(role, "u1"), r)
			if r.SQL != rawSQL {
				t.Errorf("%s should retain the SQL, got %q", role, r.SQL)
			}
		}
	})

	t.Run("viewers and unknown roles have the SQL stripped", func(t *testing.T) {
		for _, role := range []string{auth.RoleViewer, "some-future-role"} {
			r := report()
			svc := &ReportsService{}
			svc.applySQLVisibility(ctxAs(role, "u1"), r)
			if r.SQL != "" {
				t.Errorf("role %q should not see the raw SQL, got %q", role, r.SQL)
			}
		}
	})

	// A context with no role at all is the single-tenant/dev default, which
	// PrincipalFromContext resolves to admin. Pinning it here so the fail-open is a
	// deliberate, visible decision rather than something a reader has to infer.
	t.Run("an absent role resolves to the admin default", func(t *testing.T) {
		r := report()
		svc := &ReportsService{}
		svc.applySQLVisibility(context.Background(), r)
		if r.SQL != rawSQL {
			t.Errorf("an unauthenticated context defaults to admin, got %q", r.SQL)
		}
	})

	t.Run("role matching ignores case and surrounding whitespace", func(t *testing.T) {
		r := report()
		svc := &ReportsService{}
		svc.applySQLVisibility(ctxAs("  ANALYST  ", "u1"), r)
		if r.SQL != rawSQL {
			t.Errorf("an analyst should retain the SQL regardless of casing, got %q", r.SQL)
		}
	})

	t.Run("share-token principals are left to the share policy", func(t *testing.T) {
		// GetShared applies shareLinkExposeSQL itself; this function must not
		// pre-empt it by stripping (or keeping) the SQL for a share reader.
		r := report()
		svc := &ReportsService{}
		svc.applySQLVisibility(ctxAs(auth.RoleViewer, "share-token"), r)
		if r.SQL != rawSQL {
			t.Errorf("the share path owns this decision; SQL should be untouched here, got %q", r.SQL)
		}
	})

	t.Run("a sealed SQL column is unsealed for a permitted role", func(t *testing.T) {
		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(i + 1)
		}
		svc := &ReportsService{}
		svc.SetDataEncryptionKey(key)

		sealed, err := sealProductSQL(key, rawSQL)
		if err != nil {
			t.Fatalf("seal failed: %v", err)
		}
		if sealed == rawSQL {
			t.Fatal("expected the SQL to be sealed with a configured key")
		}

		r := &reports.Report{ID: "rep-1", SQL: sealed}
		svc.applySQLVisibility(ctxAs(auth.RoleAnalyst, "u1"), r)
		if r.SQL != rawSQL {
			t.Errorf("expected the unsealed SQL, got %q", r.SQL)
		}

		// The same sealed value must still be withheld from a viewer.
		r2 := &reports.Report{ID: "rep-1", SQL: sealed}
		svc.applySQLVisibility(ctxAs(auth.RoleViewer, "u1"), r2)
		if r2.SQL != "" {
			t.Errorf("a viewer must not receive the unsealed SQL, got %q", r2.SQL)
		}
	})

	t.Run("a sealed column with no key yields nothing rather than ciphertext", func(t *testing.T) {
		key := make([]byte, 32)
		for i := range key {
			key[i] = byte(i + 1)
		}
		sealed, err := sealProductSQL(key, rawSQL)
		if err != nil {
			t.Fatalf("seal failed: %v", err)
		}
		r := &reports.Report{ID: "rep-1", SQL: sealed}
		svc := &ReportsService{} // no data encryption key configured
		svc.applySQLVisibility(ctxAs(auth.RoleAnalyst, "u1"), r)
		if r.SQL != "" {
			t.Errorf("ciphertext must never reach the API, got %q", r.SQL)
		}
	})
}
