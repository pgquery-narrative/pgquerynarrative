package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func resetDiscoveryCache(t *testing.T) {
	t.Helper()
	globalDiscovery.mu.Lock()
	defer globalDiscovery.mu.Unlock()
	globalDiscovery.fetched = time.Time{}
	globalDiscovery.endpoints = oidcDiscovery{}
}

func TestOIDCEndpoints_httpIssuerAllowsHTTPJWKS(t *testing.T) {
	resetDiscoveryCache(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{
				"issuer":                 srv.URL,
				"authorization_endpoint": srv.URL + "/oauth/authorize",
				"token_endpoint":         srv.URL + "/oauth/token",
				"jwks_uri":               srv.URL + "/.well-known/jwks.json",
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	authURL, tokenURL, err := OIDCEndpoints(ctx, srv.URL, srv.Client())
	if err != nil {
		t.Fatalf("OIDCEndpoints: %v", err)
	}
	if !strings.HasSuffix(authURL, "/oauth/authorize") {
		t.Fatalf("authorize URL = %q", authURL)
	}
	if !strings.HasSuffix(tokenURL, "/oauth/token") {
		t.Fatalf("token URL = %q", tokenURL)
	}
}

func TestOIDCEndpoints_httpsIssuerRejectsHTTPJWKS(t *testing.T) {
	resetDiscoveryCache(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			// Claim an https issuer while serving over http so the https JWKS rule is exercised.
			"issuer":                 "https://idp.example.test",
			"authorization_endpoint": "https://idp.example.test/oauth/authorize",
			"token_endpoint":         "https://idp.example.test/oauth/token",
			"jwks_uri":               "http://idp.example.test/jwks",
		})
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// Pass the httptest URL as issuer so discovery HTTP GET succeeds; document issuer mismatch
	// is checked first — use matching issuer host by rewriting: call with https issuer but
	// client that redirects... Simpler: pass srv.URL and put srv.URL as issuer with http jwks
	// while checking the https-issuer path via a different approach:
	// Configure discovery doc issuer == configured issuer (http), already covered above.
	// For https rejection, configure issuer https://... but fetch from srv — issuer mismatch.
	// Instead encode the rule directly: http issuer was allowed above; here force https issuer
	// string with http jwks by making discovery return matching https issuer (won't match srv.URL).
	_, _, err := OIDCEndpoints(ctx, "https://idp.example.test", srv.Client())
	// Request goes to https://idp.example.test/.well-known/... which fails network → fallback, no error.
	// Build a transport that rewrites https://idp.example.test to the httptest URL.
	client := srv.Client()
	client.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = strings.TrimPrefix(srv.URL, "http://")
		return http.DefaultTransport.RoundTrip(req)
	})
	resetDiscoveryCache(t)
	_, _, err = OIDCEndpoints(ctx, "https://idp.example.test", client)
	if err == nil || !strings.Contains(err.Error(), "jwks_uri must use https") {
		t.Fatalf("expected https jwks enforcement, got err=%v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestValidatePrincipal_managedStoreRequiresCredentials(t *testing.T) {
	a := NewAuthenticator(true, "", "", "", nil)
	// Non-nil managed store (nil pool) counts as a credential source: no open system principal.
	a.SetManagedKeyStore(&ManagedKeyStore{})
	r := httptest.NewRequest(http.MethodGet, "/api/v1/queries", nil)
	if _, ok := a.ValidatePrincipal(r); ok {
		t.Fatal("expected rejection when managed keys are configured but no bearer token is presented")
	}
}
