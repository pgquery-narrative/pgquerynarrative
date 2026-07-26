package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
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
// Prefix is a short, non-secret display fragment (e.g. the key's first few
// characters) set at issuance time so operators can identify a key in logs
// or an admin UI without ever storing or displaying the full secret.
type APIKeyEntry struct {
	Key       string    `json:"key"`
	KeyHash   string    `json:"key_hash"`
	ID        string    `json:"id"`
	Prefix    string    `json:"prefix"`
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
	managed    *ManagedKeyStore
	enabled    bool
	lastUsed   sync.Map // entry ID (string) -> time.Time
	keyUsage   *KeyUsageStore
	usageWarm  sync.Once
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

// SetManagedKeyStore attaches durable hashed API keys (CLI/MCP) for authentication.
func (a *Authenticator) SetManagedKeyStore(store *ManagedKeyStore) {
	if a != nil {
		a.managed = store
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
			Prefix    string   `json:"prefix"`
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
					Prefix:  strings.TrimSpace(e.Prefix),
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
				if entry.Prefix == "" && entry.Key != "" {
					entry.Prefix = keyPrefix(entry.Key)
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
	if !a.enabled {
		return Principal{UserID: "system", OrgID: DefaultOrgID(), Role: RoleAdmin}, true
	}
	// Auth enabled with no credential sources must fail closed (never open-admin).
	// Managed keys count as a credential source so CLI/MCP keys work without env API keys.
	// For local open access set SECURITY_AUTH_ENABLED=false instead.
	if len(a.keys) == 0 && (a.oidc == nil || !a.oidc.Enabled()) && a.managed == nil {
		return Principal{}, false
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
		a.recordUsage(entry.ID)
		return Principal{UserID: entry.ID, OrgID: orgID, Role: role}, true
	}
	if a.managed != nil {
		entry, found, lookupErr := a.managed.LookupBySecret(r.Context(), token)
		if lookupErr == nil && found {
			if entry.Revoked || entryExpired(entry, now) {
				return Principal{}, false
			}
			orgID := entry.OrgID
			role := normalizeRole(entry.Role)
			if preferredOrg != "" && preferredOrg != orgID {
				return Principal{}, false
			}
			if len(entry.Scopes) > 0 && !entryAllowsRequest(entry, r.Method, r.URL.Path) {
				return Principal{}, false
			}
			a.recordUsage(entry.ID)
			return Principal{UserID: entry.ID, OrgID: orgID, Role: role}, true
		}
	}
	return Principal{}, false
}

// recordUsage records the current time as the last-used timestamp for the API key entry ID.
func (a *Authenticator) recordUsage(id string) {
	if a == nil || strings.TrimSpace(id) == "" {
		return
	}
	now := time.Now().UTC()
	a.lastUsed.Store(id, now)
	if a.keyUsage != nil {
		// Best-effort durable write; do not block auth on ledger latency.
		go a.keyUsage.Touch(context.Background(), id)
	}
}

// LastUsedAt returns the last time the named API key entry successfully authenticated a
// request, for display in an admin UI or audit log. ok is false if the key has never
// been used (or was never observed by this process instance and has no durable row).
func (a *Authenticator) LastUsedAt(id string) (t time.Time, ok bool) {
	if a == nil {
		return time.Time{}, false
	}
	a.warmLastUsed()
	v, found := a.lastUsed.Load(id)
	if found {
		t, ok = v.(time.Time)
		if ok {
			return t, true
		}
	}
	if a.keyUsage != nil {
		return a.keyUsage.LastUsedAt(context.Background(), id)
	}
	return time.Time{}, false
}

// KeyMetadata describes a configured API key without exposing its secret, for admin listings.
type KeyMetadata struct {
	ID         string    `json:"id"`
	Prefix     string    `json:"prefix"`
	Role       string    `json:"role"`
	OrgID      string    `json:"org_id"`
	Scopes     []string  `json:"scopes"`
	ExpiresAt  time.Time `json:"expires_at,omitzero"`
	Revoked    bool      `json:"revoked"`
	LastUsedAt time.Time `json:"last_used_at,omitzero"`
}

// KeyMetadataList returns non-secret metadata for all configured API keys, suitable for
// display in an admin UI or audit endpoint.
func (a *Authenticator) KeyMetadataList() []KeyMetadata {
	if a == nil {
		return nil
	}
	out := make([]KeyMetadata, 0, len(a.keys))
	for _, e := range a.keys {
		meta := KeyMetadata{
			ID:        e.ID,
			Prefix:    e.Prefix,
			Role:      normalizeRole(e.Role),
			OrgID:     e.OrgID,
			Scopes:    append([]string(nil), e.Scopes...),
			ExpiresAt: e.ExpiresAt,
			Revoked:   e.Revoked,
		}
		if lu, ok := a.LastUsedAt(e.ID); ok {
			meta.LastUsedAt = lu
		}
		out = append(out, meta)
	}
	return out
}

// keyPrefix derives a short, non-secret display fragment from a raw key secret.
func keyPrefix(key string) string {
	const n = 8
	key = strings.TrimSpace(key)
	if len(key) <= n {
		return key
	}
	return key[:n]
}

// PeekPrincipal performs a lightweight, DB-free identity check intended only for rate-limit
// keying. It validates bearer credentials locally (API key match, or OIDC signature/JWKS
// check) but does not resolve organization membership, so the returned Principal.OrgID may
// be an approximation (preferred-org header or default org) rather than the authorized
// membership. Callers MUST NOT use the result for authorization decisions; use
// ValidatePrincipal for that. Safe to call before or in addition to ValidatePrincipal.
func (a *Authenticator) PeekPrincipal(r *http.Request) (Principal, bool) {
	if a == nil || !a.enabled || (len(a.keys) == 0 && (a.oidc == nil || !a.oidc.Enabled())) {
		return Principal{}, false
	}
	token := bearerToken(r)
	if token == "" {
		return Principal{}, false
	}
	preferredOrg := PreferredOrgFromRequest(r)
	if a.oidc != nil && a.oidc.Enabled() {
		if sub, roles, err := a.oidc.Validate(r.Context(), token); err == nil && strings.TrimSpace(sub) != "" {
			org := preferredOrg
			if org == "" {
				org = DefaultOrgID()
			}
			role := RoleAnalyst
			if len(roles) > 0 {
				role = mapOIDCRole(roles[0])
			}
			return Principal{UserID: sub, OrgID: org, Role: role}, true
		}
	}
	now := time.Now().UTC()
	for _, entry := range a.keys {
		if !matchesAPIKey(token, entry) {
			continue
		}
		if entry.Revoked || entryExpired(entry, now) {
			return Principal{}, false
		}
		org := entry.OrgID
		if org == "" {
			org = preferredOrg
		}
		if org == "" {
			org = DefaultOrgID()
		}
		return Principal{UserID: entry.ID, OrgID: org, Role: normalizeRole(entry.Role)}, true
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
	method = strings.ToUpper(method)
	if method == http.MethodDelete && (strings.HasPrefix(path, "/api/v1/queries/saved/") || strings.HasPrefix(path, "/api/v1/schedules/")) {
		return true
	}
	if method == http.MethodPut && strings.HasPrefix(path, "/api/v1/schedules/") {
		return true
	}
	if method != http.MethodPost {
		return false
	}
	allowed := []string{
		"/api/v1/queries/run",
		"/api/v1/queries/explain",
		"/api/v1/queries/saved",
		"/api/v1/reports/generate",
		"/api/v1/reports/rewrite",
		"/api/v1/reports/share",
		"/api/v1/schedules",
		"/api/v1/suggestions/ask",
		"/api/v1/suggestions/chat",
		"/api/v1/suggestions/explain",
	}
	for _, p := range allowed {
		if path == p {
			return true
		}
	}
	if strings.HasPrefix(path, "/api/v1/reports/shares/") && strings.HasSuffix(path, "/revoke") {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/schedules/") && strings.HasSuffix(path, "/run") {
		return true
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
