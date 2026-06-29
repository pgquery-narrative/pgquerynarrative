package auth

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
)

// Role names for RBAC.
const (
	RoleAdmin   = "admin"
	RoleAnalyst = "analyst"
	RoleViewer  = "viewer"
)

// ContextKey is the type for auth identity in request context.
type ContextKey string

const (
	// IdentityContextKey is the context key for the authenticated identity (e.g. "api-key" or "").
	IdentityContextKey ContextKey = "auth_identity"
	// RoleContextKey is the context key for the authenticated role.
	RoleContextKey ContextKey = "auth_role"
)

// APIKeyEntry is one configured API key with identity and role.
type APIKeyEntry struct {
	Key  string `json:"key"`
	ID   string `json:"id"`
	Role string `json:"role"`
}

// Authenticator validates Bearer tokens (API keys and optional OIDC JWTs).
type Authenticator struct {
	keys    []APIKeyEntry
	oidc    *OIDCValidator
	enabled bool
}

// NewAuthenticator builds an authenticator from security configuration.
func NewAuthenticator(enabled bool, primaryKey string, keysJSON string, oidc *OIDCValidator) *Authenticator {
	keys := loadAPIKeys(primaryKey, keysJSON)
	return &Authenticator{keys: keys, oidc: oidc, enabled: enabled}
}

func loadAPIKeys(primaryKey string, keysJSON string) []APIKeyEntry {
	var out []APIKeyEntry
	raw := strings.TrimSpace(keysJSON)
	if raw != "" {
		var parsed []APIKeyEntry
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			for _, e := range parsed {
				if strings.TrimSpace(e.Key) == "" {
					continue
				}
				if e.Role == "" {
					e.Role = RoleAnalyst
				}
				if e.ID == "" {
					e.ID = "api-key"
				}
				out = append(out, e)
			}
		}
	}
	if pk := strings.TrimSpace(primaryKey); pk != "" {
		out = append(out, APIKeyEntry{Key: pk, ID: "api-key", Role: RoleAdmin})
	}
	return out
}

// AuthRequired reports whether requests on protected paths must be authenticated.
func (a *Authenticator) AuthRequired() bool {
	return a != nil && a.enabled && a.HasCredentials()
}

// HasCredentials reports whether any API keys or OIDC issuer are configured.
func (a *Authenticator) HasCredentials() bool {
	if a == nil {
		return false
	}
	return len(a.keys) > 0 || (a.oidc != nil && a.oidc.Enabled())
}

// ValidateRequest checks Bearer API key or OIDC JWT. Returns identity, role, ok.
func (a *Authenticator) ValidateRequest(r *http.Request) (identity, role string, ok bool) {
	if !a.enabled || len(a.keys) == 0 && (a.oidc == nil || !a.oidc.Enabled()) {
		return "", "", true
	}
	token := bearerToken(r)
	if token == "" {
		return "", "", false
	}
	if a.oidc != nil && a.oidc.Enabled() {
		if sub, roles, err := a.oidc.Validate(r.Context(), token); err == nil {
			role := RoleAnalyst
			if len(roles) > 0 {
				role = mapOIDCRole(roles[0])
			}
			return sub, role, true
		}
	}
	for _, entry := range a.keys {
		if subtle.ConstantTimeCompare([]byte(token), []byte(entry.Key)) == 1 {
			return entry.ID, normalizeRole(entry.Role), true
		}
	}
	return "", "", false
}

func bearerToken(r *http.Request) string {
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return ""
	}
	return strings.TrimSpace(auth[len(prefix):])
}

func normalizeRole(role string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case RoleAdmin, "administrator":
		return RoleAdmin
	case RoleViewer, "read", "readonly", "reader":
		return RoleViewer
	default:
		return RoleAnalyst
	}
}

func mapOIDCRole(claim string) string {
	return normalizeRole(claim)
}

// RoleFromContext returns the authenticated role or admin when auth is disabled.
func RoleFromContext(ctx context.Context) string {
	if v, ok := ctx.Value(RoleContextKey).(string); ok && v != "" {
		return v
	}
	return RoleAdmin
}

// AllowsMethod reports whether role may use HTTP method on API path.
func AllowsMethod(role, method, path string) bool {
	method = strings.ToUpper(method)
	if role == RoleAdmin {
		return true
	}
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/reports/shared/") {
		return true
	}
	if role == RoleViewer {
		return false
	}
	// analyst: run queries, generate reports, ask, explain
	if role == RoleAnalyst {
		return isAnalystWritePath(method, path)
	}
	return false
}

func isAnalystWritePath(method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	allowed := []string{
		"/api/v1/queries/run",
		"/api/v1/queries/explain",
		"/api/v1/reports/generate",
		"/api/v1/reports/rewrite",
		"/api/v1/suggestions/ask",
		"/api/v1/suggestions/chat",
		"/api/v1/suggestions/explain",
	}
	for _, p := range allowed {
		if path == p {
			return true
		}
	}
	return false
}

// LoadAPIKeysJSON reads SECURITY_API_KEYS_JSON from env when keysJSON is empty.
func LoadAPIKeysJSON(keysJSON string) string {
	if strings.TrimSpace(keysJSON) != "" {
		return keysJSON
	}
	return os.Getenv("SECURITY_API_KEYS_JSON")
}
