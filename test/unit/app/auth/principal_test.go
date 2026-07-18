package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestDefaultOrgID(t *testing.T) {
	old, hadOld := os.LookupEnv("DEFAULT_ORGANIZATION_ID")
	defer func() {
		if hadOld {
			os.Setenv("DEFAULT_ORGANIZATION_ID", old)
		} else {
			os.Unsetenv("DEFAULT_ORGANIZATION_ID")
		}
	}()

	os.Unsetenv("DEFAULT_ORGANIZATION_ID")
	if got := auth.DefaultOrgID(); got != auth.DefaultOrganizationID {
		t.Errorf("DefaultOrgID() = %q, want bootstrap constant %q", got, auth.DefaultOrganizationID)
	}

	os.Setenv("DEFAULT_ORGANIZATION_ID", "  ")
	if got := auth.DefaultOrgID(); got != auth.DefaultOrganizationID {
		t.Errorf("DefaultOrgID() with blank env = %q, want bootstrap constant", got)
	}

	os.Setenv("DEFAULT_ORGANIZATION_ID", "org-custom")
	if got := auth.DefaultOrgID(); got != "org-custom" {
		t.Errorf("DefaultOrgID() = %q, want %q", got, "org-custom")
	}
}

func TestPrincipalFromContext_Defaults(t *testing.T) {
	p := auth.PrincipalFromContext(context.Background())
	if p.UserID != "system" {
		t.Errorf("UserID = %q, want %q", p.UserID, "system")
	}
	if p.Role != auth.RoleAdmin {
		t.Errorf("Role = %q, want %q", p.Role, auth.RoleAdmin)
	}
	if p.OrgID != auth.DefaultOrgID() {
		t.Errorf("OrgID = %q, want %q", p.OrgID, auth.DefaultOrgID())
	}
}

func TestWithPrincipal_RoundTrip(t *testing.T) {
	want := auth.Principal{UserID: "user-1", OrgID: "org-1", Role: "viewer"}
	ctx := auth.WithPrincipal(context.Background(), want)

	got := auth.PrincipalFromContext(ctx)
	if got != want {
		t.Errorf("PrincipalFromContext() = %+v, want %+v", got, want)
	}

	if orgID := auth.OrgIDFromContext(ctx); orgID != want.OrgID {
		t.Errorf("OrgIDFromContext() = %q, want %q", orgID, want.OrgID)
	}
}

func TestWithPrincipal_EmptyFieldsDoNotOverrideDefaults(t *testing.T) {
	// Setting an empty UserID/Role should not clobber the defaults applied by
	// PrincipalFromContext, since it only overrides when the context value is non-empty.
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{OrgID: "org-only"})
	got := auth.PrincipalFromContext(ctx)
	if got.UserID != "system" {
		t.Errorf("UserID = %q, want default %q", got.UserID, "system")
	}
	if got.Role != auth.RoleAdmin {
		t.Errorf("Role = %q, want default %q", got.Role, auth.RoleAdmin)
	}
	if got.OrgID != "org-only" {
		t.Errorf("OrgID = %q, want %q", got.OrgID, "org-only")
	}
}

func TestOrgIDFromContext_MissingFailsClosed(t *testing.T) {
	if id := auth.OrgIDFromContext(context.Background()); id != "" {
		t.Errorf("OrgIDFromContext() with no context value = %q, want empty string", id)
	}

	ctx := context.WithValue(context.Background(), auth.OrgContextKey, "   ")
	if id := auth.OrgIDFromContext(ctx); id != "" {
		t.Errorf("OrgIDFromContext() with blank org = %q, want empty string", id)
	}
}

func TestWriteForbidden(t *testing.T) {
	rec := httptest.NewRecorder()
	auth.WriteForbidden(rec)

	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	if rec.Body.String() != auth.ForbiddenJSON {
		t.Errorf("body = %q, want %q", rec.Body.String(), auth.ForbiddenJSON)
	}
}

func TestPKCEStore_NilSafety(t *testing.T) {
	if s := auth.NewPKCEStore(nil); s != nil {
		t.Errorf("NewPKCEStore(nil) = %v, want nil", s)
	}

	var nilStore *auth.PKCEStore
	if err := nilStore.Save(context.Background(), "state", "verifier", 0); err != nil {
		t.Errorf("nil store Save() = %v, want nil", err)
	}
	if _, ok := nilStore.Consume(context.Background(), "state"); ok {
		t.Error("nil store Consume() ok = true, want false")
	}
	nilStore.CleanupExpired(context.Background()) // must not panic
}
