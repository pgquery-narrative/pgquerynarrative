package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/httpmw"
)

func TestAuthMiddleware_ProtectsMetricsWhenEnabled(t *testing.T) {
	called := false
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})
	authn := auth.NewAuthenticator(true, "test-api-key-secret", "", "", nil)
	handler := httpmw.AuthMiddleware(inner, authn, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	if called {
		t.Fatal("inner handler should not run without auth")
	}

	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set("Authorization", "Bearer test-api-key-secret")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 with valid bearer", rec.Code)
	}
}

func TestAuthMiddleware_HealthAlwaysOpen(t *testing.T) {
	authn := auth.NewAuthenticator(true, "secret-key-at-least-16", "", "", nil)
	handler := httpmw.AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}), authn, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health should be open, got %d", rec.Code)
	}
}
