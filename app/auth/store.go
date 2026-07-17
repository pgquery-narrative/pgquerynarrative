package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
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
// Prefer key_hash (SHA-256 hex of the secret) over plain key in production configs.
type APIKeyEntry struct {
	Key       string    `json:"key"`
	KeyHash   string    `json:"key_hash"`
	ID        string    `json:"id"`
	Role      string    `json:"role"`
	OrgID     string    `json:"org_id"`
	ExpiresAt time.Time `json:"expires_at"`
	Scopes    []string  `json:"scopes"`
	Revoked   bool      `json:"revoked"`
}

// Authenticator validates Bearer tokens (API keys and optional OIDC JWTs).
type Authenticator struct {
	keys       []APIKeyEntry
	oidc       *OIDCValidator
	membership *MembershipStore
	enabled    bool
}

// NewAuthenticator builds an authenticator from security configuration.
func NewAuthenticator(enabled bool, primaryKey string, primaryKeyHash string, keysJSON string, oidc *OIDCValidator) *Authenticator {
	keys := loadAPIKeys(primaryKey, primaryKeyHash, keysJSON)
	return &Authenticator{keys: keys, oidc: oidc, enabled: enabled}
}

// SetMembershipStore attaches organization membership resolution for OIDC and API keys without org_id.
func (a *Authenticator) SetMembershipStore(store *MembershipStore) {
	if a != nil {
		a.membership = store
	}
}

func loadAPIKeys(primaryKey string, primaryKeyHash string, keysJSON string) []APIKeyEntry {
	var out []APIKeyEntry
	raw := strings.TrimSpace(keysJSON)
	if raw != "" {
		var parsed []struct {
			Key       string   `json:"key"`
			KeyHash   string   `json:"key_hash"`
			ID        string   `json:"id"`
			Role      string   `json:"role"`
			OrgID     string   `json:"org_id"`
			ExpiresAt string   `json:"expires_at"`
			Scopes    []string `json:"scopes"`
			Revoked   bool     `json:"revoked"`
		}
		if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
			for _, e := range parsed {
				if strings.TrimSpace(e.Key) == "" && strings.TrimSpace(e.KeyHash) == "" {
					continue
				}
				entry := APIKeyEntry{
					Key:     strings.TrimSpace(e.Key),
					KeyHash: strings.TrimSpace(e.KeyHash),
					ID:      e.ID,
					Role:    e.Role,
					OrgID:   e.OrgID,
					Scopes:  append([]string(nil), e.Scopes...),
					Revoked: e.Revoked,
				}
				if entry.Role == "" {
					entry.Role = RoleAnalyst
				}
				if entry.ID == "" {
					entry.ID = "api-key"
				}
				if ts := strings.TrimSpace(e.ExpiresAt); ts != "" {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						entry.ExpiresAt = t.UTC()
					}
				}
				out = append(out, entry)
			}
		}
	}
	if pk := strings.TrimSpace(primaryKey); pk != "" {
		out = append(out, APIKeyEntry{Key: pk, ID: "api-key", Role: RoleAdmin})
	}
	if ph := strings.TrimSpace(primaryKeyHash); ph != "" {
		out = append(out, APIKeyEntry{KeyHash: ph, ID: "api-key", Role: RoleAdmin})
	}
	return out
}

// AuthRequired reports whether requests on protected paths must be authenticated.
func (a *Authenticator) AuthRequired() bool {
	return a != nil && a.enabled && a.HasCredentials()
}

// HasCredentials reports whether any API keys, OIDC issuer, or browser session support is configured.
func (a *Authenticator) HasCredentials() bool {
	if a == nil {
		return false
	}
	return len(a.keys) > 0 || (a.oidc != nil && a.oidc.Enabled())
}

// ValidateRequest checks Bearer API key or OIDC JWT. Returns identity, role, ok.
func (a *Authenticator) ValidateRequest(r *http.Request) (identity, role string, ok bool) {
	p, ok := a.ValidatePrincipal(r)
	if !ok {
		return "", "", false
	}
	return p.UserID, p.Role, true
}

// ValidatePrincipal validates bearer credentials and returns the authenticated principal.
func (a *Authenticator) ValidatePrincipal(r *http.Request) (Principal, bool) {
	if !a.enabled || (len(a.keys) == 0 && (a.oidc == nil || !a.oidc.Enabled())) {
		return Principal{UserID: "system", OrgID: DefaultOrgID(), Role: RoleAdmin}, true
	}
	token := bearerToken(r)
	if token == "" {
		return Principal{}, false
	}
	preferredOrg := PreferredOrgFromRequest(r)
	if a.oidc != nil && a.oidc.Enabled() {
		if sub, roles, err := a.oidc.Validate(r.Context(), token); err == nil && strings.TrimSpace(sub) != "" {
			fallbackRole := RoleAnalyst
			if len(roles) > 0 {
				fallbackRole = mapOIDCRole(roles[0])
			}
			if a.membership != nil {
				p, resolveErr := a.membership.ResolvePrincipal(r.Context(), sub, preferredOrg, fallbackRole)
				if resolveErr != nil {
					return Principal{}, false
				}
				return p, true
			}
			return Principal{UserID: sub, OrgID: DefaultOrgID(), Role: fallbackRole}, true
		}
	}
	now := time.Now().UTC()
	for _, entry := range a.keys {
		if !matchesAPIKey(token, entry) {
			continue
		}
		if entry.Revoked {
			return Principal{}, false
		}
		if entryExpired(entry, now) {
			return Principal{}, false
		}
		orgID := entry.OrgID
		role := normalizeRole(entry.Role)
		if orgID == "" && a.membership != nil {
			p, resolveErr := a.membership.ResolvePrincipal(r.Context(), entry.ID, preferredOrg, role)
			if resolveErr != nil {
				return Principal{}, false
			}
			if len(entry.Scopes) > 0 && !entryAllowsRequest(entry, r.Method, r.URL.Path) {
				return Principal{}, false
			}
			return p, true
		}
		if orgID == "" {
			orgID = DefaultOrgID()
		}
		if preferredOrg != "" && preferredOrg != orgID {
			return Principal{}, false
		}
		if len(entry.Scopes) > 0 && !entryAllowsRequest(entry, r.Method, r.URL.Path) {
			return Principal{}, false
		}
		return Principal{UserID: entry.ID, OrgID: orgID, Role: role}, true
	}
	return Principal{}, false
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
