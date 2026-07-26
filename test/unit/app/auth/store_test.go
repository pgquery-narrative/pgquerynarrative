package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestAuthenticator_AuthRequired(t *testing.T) {
	disabled := auth.NewAuthenticator(false, "secret-key-at-least-16", "", "", nil)
	if disabled.AuthRequired() {
		t.Fatal("auth should not be required when disabled")
	}
	enabled := auth.NewAuthenticator(true, "secret-key-at-least-16", "", "", nil)
	if !enabled.AuthRequired() {
		t.Fatal("auth should be required when enabled with API key")
	}
}

func TestAuthenticator_AuthEnabledWithoutCredentialsFailsClosed(t *testing.T) {
	a := auth.NewAuthenticator(true, "", "", "", nil)
	r := httptest.NewRequest(http.MethodGet, "/api/v1/queries", nil)
	if _, ok := a.ValidatePrincipal(r); ok {
		t.Fatal("auth enabled with no credential sources must not grant open-admin access")
	}
	disabled := auth.NewAuthenticator(false, "", "", "", nil)
	if p, ok := disabled.ValidatePrincipal(r); !ok || p.Role != auth.RoleAdmin {
		t.Fatal("auth disabled should still grant system admin for local development")
	}
}

func TestAllowsMethod_viewerCannotPost(t *testing.T) {
	if auth.AllowsMethod(auth.RoleViewer, http.MethodPost, "/api/v1/queries/run") {
		t.Fatal("viewer should not POST to run queries")
	}
	if !auth.AllowsMethod(auth.RoleViewer, http.MethodGet, "/api/v1/queries/history") {
		t.Fatal("viewer should GET read endpoints")
	}
}

func TestAllowsMethod_analystCanRun(t *testing.T) {
	if !auth.AllowsMethod(auth.RoleAnalyst, http.MethodPost, "/api/v1/queries/run") {
		t.Fatal("analyst should POST to run queries")
	}
	if !auth.AllowsMethod(auth.RoleAnalyst, http.MethodDelete, "/api/v1/schedules/abc") {
		t.Fatal("analyst should DELETE schedules")
	}
	if auth.AllowsMethod(auth.RoleAnalyst, http.MethodDelete, "/api/v1/reports/abc") {
		t.Fatal("analyst should not DELETE arbitrary reports")
	}
}
