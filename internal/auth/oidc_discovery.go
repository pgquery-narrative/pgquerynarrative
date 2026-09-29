package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

type oidcDiscovery struct {
	Issuer                string `json:"issuer"`
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	JWKSURI               string `json:"jwks_uri"`
}

type discoveryCache struct {
	mu        sync.RWMutex
	fetched   time.Time
	endpoints oidcDiscovery
}

var globalDiscovery discoveryCache

// OIDCEndpoints returns authorize and token URLs from issuer discovery when available.
func OIDCEndpoints(ctx context.Context, issuer string, client *http.Client) (authorizeURL, tokenURL string, err error) {
	issuer = strings.TrimRight(strings.TrimSpace(issuer), "/")
	if issuer == "" {
		return "", "", fmt.Errorf("issuer required")
	}
	if strings.Contains(issuer, "accounts.google.com") || strings.Contains(issuer, "google") {
		return "https://accounts.google.com/o/oauth2/v2/auth", "https://oauth2.googleapis.com/token", nil
	}

	globalDiscovery.mu.RLock()
	if time.Since(globalDiscovery.fetched) < 6*time.Hour && globalDiscovery.endpoints.AuthorizationEndpoint != "" {
		ep := globalDiscovery.endpoints
		globalDiscovery.mu.RUnlock()
		return ep.AuthorizationEndpoint, ep.TokenEndpoint, nil
	}
	globalDiscovery.mu.RUnlock()

	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/.well-known/openid-configuration", nil)
	if err != nil {
		return issuer + "/oauth2/v1/authorize", issuer + "/oauth2/v1/token", nil
	}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode >= 300 {
		if resp != nil {
			resp.Body.Close() // #nosec G104 -- close error on a body we're discarding is not actionable.
		}
		return issuer + "/oauth2/v1/authorize", issuer + "/oauth2/v1/token", nil
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return issuer + "/oauth2/v1/authorize", issuer + "/oauth2/v1/token", nil
	}
	var doc oidcDiscovery
	if err := json.Unmarshal(body, &doc); err != nil || doc.AuthorizationEndpoint == "" {
		return issuer + "/oauth2/v1/authorize", issuer + "/oauth2/v1/token", nil
	}
	if doc.Issuer != "" && strings.TrimRight(doc.Issuer, "/") != issuer {
		return "", "", fmt.Errorf("discovered issuer %q does not match configured issuer %q", doc.Issuer, issuer)
	}
	if doc.JWKSURI != "" && !strings.HasPrefix(strings.ToLower(doc.JWKSURI), "https://") {
		// Allow http JWKS only when the configured issuer is itself http (local mock IdP / dev).
		if !strings.HasPrefix(strings.ToLower(issuer), "http://") {
			return "", "", fmt.Errorf("discovered jwks_uri must use https")
		}
	}
	globalDiscovery.mu.Lock()
	globalDiscovery.endpoints = doc
	globalDiscovery.fetched = time.Now()
	globalDiscovery.mu.Unlock()
	return doc.AuthorizationEndpoint, doc.TokenEndpoint, nil
}

// DiscoverJWKSURI returns the JWKS URI from OIDC discovery when available.
func DiscoverJWKSURI(ctx context.Context, issuer string, client *http.Client) (string, error) {
	_, _, err := OIDCEndpoints(ctx, issuer, client)
	if err != nil {
		return "", err
	}
	globalDiscovery.mu.RLock()
	defer globalDiscovery.mu.RUnlock()
	return globalDiscovery.endpoints.JWKSURI, nil
}
