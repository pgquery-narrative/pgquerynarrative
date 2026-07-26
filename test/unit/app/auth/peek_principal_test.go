package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestPeekPrincipal_Disabled(t *testing.T) {
	a := auth.NewAuthenticator(false, "secret", "", "", nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer secret")
	if _, ok := a.PeekPrincipal(r); ok {
		t.Error("PeekPrincipal() ok = true, want false when disabled")
	}
}

func TestPeekPrincipal_NoToken(t *testing.T) {
	a := auth.NewAuthenticator(true, "secret", "", "", nil)
	r := httptest.NewRequest("GET", "/", nil)
	if _, ok := a.PeekPrincipal(r); ok {
		t.Error("PeekPrincipal() ok = true, want false with no Authorization header")
	}
}

func TestPeekPrincipal_ValidAPIKey(t *testing.T) {
	a := auth.NewAuthenticator(true, "", "", `[{"key":"k1","id":"user-1","role":"analyst","org_id":"org-1"}]`, nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer k1")
	p, ok := a.PeekPrincipal(r)
	if !ok {
		t.Fatal("PeekPrincipal() ok = false, want true for valid key")
	}
	if p.UserID != "user-1" || p.OrgID != "org-1" || p.Role != auth.RoleAnalyst {
		t.Errorf("PeekPrincipal() = %+v, want user-1/org-1/analyst", p)
	}
}

func TestPeekPrincipal_APIKeyOrgFallsBackToHeaderThenDefault(t *testing.T) {
	a := auth.NewAuthenticator(true, "", "", `[{"key":"k1","id":"user-1","role":"viewer"}]`, nil)

	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer k1")
	r.Header.Set(auth.OrganizationHeader, "org-header")
	p, ok := a.PeekPrincipal(r)
	if !ok || p.OrgID != "org-header" {
		t.Errorf("PeekPrincipal() = %+v, ok=%v, want org-header", p, ok)
	}

	r2 := httptest.NewRequest("GET", "/", nil)
	r2.Header.Set("Authorization", "Bearer k1")
	p2, ok2 := a.PeekPrincipal(r2)
	if !ok2 || p2.OrgID != auth.DefaultOrgID() {
		t.Errorf("PeekPrincipal() = %+v, ok=%v, want default org", p2, ok2)
	}
}

func TestPeekPrincipal_RevokedAndExpiredKeysDenied(t *testing.T) {
	a := auth.NewAuthenticator(true, "", "", `[
		{"key":"revoked-key","id":"u1","revoked":true},
		{"key":"expired-key","id":"u2","expires_at":"2000-01-01T00:00:00Z"}
	]`, nil)

	for _, key := range []string{"revoked-key", "expired-key"} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Authorization", "Bearer "+key)
		if _, ok := a.PeekPrincipal(r); ok {
			t.Errorf("PeekPrincipal() with %s ok = true, want false", key)
		}
	}
}

func TestPeekPrincipal_UnknownTokenDenied(t *testing.T) {
	a := auth.NewAuthenticator(true, "", "", `[{"key":"k1","id":"user-1"}]`, nil)
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer not-a-known-key")
	if _, ok := a.PeekPrincipal(r); ok {
		t.Error("PeekPrincipal() ok = true, want false for unknown token")
	}
}

func TestPeekPrincipal_NilAuthenticator(t *testing.T) {
	var a *auth.Authenticator
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer anything")
	if _, ok := a.PeekPrincipal(r); ok {
		t.Error("nil authenticator PeekPrincipal() ok = true, want false")
	}
}

func TestRoleFromContext(t *testing.T) {
	if got := auth.RoleFromContext(context.Background()); got != auth.RoleAdmin {
		t.Errorf("RoleFromContext() with no value = %q, want %q", got, auth.RoleAdmin)
	}
	ctx := context.WithValue(context.Background(), auth.RoleContextKey, auth.RoleViewer)
	if got := auth.RoleFromContext(ctx); got != auth.RoleViewer {
		t.Errorf("RoleFromContext() = %q, want %q", got, auth.RoleViewer)
	}
}

func TestAllowsMethod_PublicSharedReportsAlwaysAllowed(t *testing.T) {
	if !auth.AllowsMethod(auth.RoleViewer, http.MethodPost, "/api/v1/reports/shared/abc") {
		t.Error("expected shared report path to be allowed regardless of role")
	}
}

func TestAllowsMethod_AnalystWritePaths(t *testing.T) {
	allowed := []string{
		"/api/v1/queries/run",
		"/api/v1/queries/explain",
		"/api/v1/queries/saved",
		"/api/v1/reports/generate",
		"/api/v1/reports/rewrite",
		"/api/v1/schedules",
		"/api/v1/suggestions/ask",
		"/api/v1/suggestions/chat",
		"/api/v1/suggestions/explain",
	}
	for _, p := range allowed {
		if !auth.AllowsMethod(auth.RoleAnalyst, http.MethodPost, p) {
			t.Errorf("AllowsMethod(analyst, POST, %q) = false, want true", p)
		}
	}
	if !auth.AllowsMethod(auth.RoleAnalyst, http.MethodDelete, "/api/v1/queries/saved/abc") {
		t.Error("AllowsMethod(analyst, DELETE, /api/v1/queries/saved/abc) = false, want true")
	}
	if !auth.AllowsMethod(auth.RoleAnalyst, http.MethodPost, "/api/v1/schedules/abc/run") {
		t.Error("AllowsMethod(analyst, POST, /api/v1/schedules/abc/run) = false, want true")
	}
	if !auth.AllowsMethod(auth.RoleAnalyst, http.MethodPut, "/api/v1/schedules/abc") {
		t.Error("AllowsMethod(analyst, PUT, /api/v1/schedules/abc) = false, want true")
	}
	if auth.AllowsMethod(auth.RoleAnalyst, http.MethodDelete, "/api/v1/queries/run") {
		t.Error("AllowsMethod(analyst, DELETE, ...) = true, want false (only POST allowed)")
	}
}

func TestLoadAPIKeysJSON(t *testing.T) {
	old, had := os.LookupEnv("SECURITY_API_KEYS_JSON")
	defer func() {
		if had {
			os.Setenv("SECURITY_API_KEYS_JSON", old)
		} else {
			os.Unsetenv("SECURITY_API_KEYS_JSON")
		}
	}()

	if got := auth.LoadAPIKeysJSON(`[{"key":"explicit"}]`); got != `[{"key":"explicit"}]` {
		t.Errorf("LoadAPIKeysJSON() = %q, want explicit value passed through", got)
	}

	os.Setenv("SECURITY_API_KEYS_JSON", `[{"key":"from-env"}]`)
	if got := auth.LoadAPIKeysJSON(""); got != `[{"key":"from-env"}]` {
		t.Errorf("LoadAPIKeysJSON(\"\") = %q, want env fallback", got)
	}
}

func TestPreferredOrgFromRequest(t *testing.T) {
	if got := auth.PreferredOrgFromRequest(nil); got != "" {
		t.Errorf("PreferredOrgFromRequest(nil) = %q, want empty", got)
	}
	r := httptest.NewRequest("GET", "/", nil)
	if got := auth.PreferredOrgFromRequest(r); got != "" {
		t.Errorf("PreferredOrgFromRequest() with no header = %q, want empty", got)
	}
	r.Header.Set(auth.OrganizationHeader, "  org-x  ")
	if got := auth.PreferredOrgFromRequest(r); got != "org-x" {
		t.Errorf("PreferredOrgFromRequest() = %q, want trimmed org-x", got)
	}
}
