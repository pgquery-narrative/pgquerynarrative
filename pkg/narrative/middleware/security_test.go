package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/internal/auth/mockoidc"
)

// TestNewOIDCValidator_RuntimeBehavior runs a real mock IdP (JWKS + signed JWTs, the same
// infrastructure the repo's own OIDC tests use), builds an Authenticator with NewOIDCValidator
// wired in, and proves a real bearer token round-trips through WrapSecured to a 200 — guarding
// against NewAuthenticator regressing back to always passing a nil OIDC validator.
func TestNewOIDCValidator_RuntimeBehavior(t *testing.T) {
	idp, err := mockoidc.Start(":0", "pgquerynarrative", "test-client")
	if err != nil {
		t.Fatalf("start mock IdP: %v", err)
	}
	defer func() { _ = idp.Close() }()

	oidc := NewOIDCValidator(OIDCConfig{
		Issuer:   idp.Issuer,
		Audience: idp.Audience,
	})
	if oidc == nil {
		t.Fatal("NewOIDCValidator returned nil for a configured issuer")
	}

	authr := NewAuthenticator(true, "", "", "", oidc)
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := WrapSecured(next, SecurityConfig{Authenticator: authr})
	srv := httptest.NewServer(handler)
	defer srv.Close()

	get := func(bearer string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/schema", nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("do request: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := get(""); code != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", code)
	}

	token, err := idp.IssueBearerToken("alice@example.com", []string{"admin"})
	if err != nil {
		t.Fatalf("issue token: %v", err)
	}
	if code := get(token); code != http.StatusOK {
		t.Errorf("valid OIDC bearer token: got %d, want 200 (real OIDC validation should let it through)", code)
	}
	if code := get(token + "tampered"); code != http.StatusUnauthorized {
		t.Errorf("tampered token: got %d, want 401", code)
	}
}
