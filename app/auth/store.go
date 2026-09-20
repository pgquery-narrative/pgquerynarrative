package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Role names for RBAC.
const (
	RolePlatformAdmin = "platform_admin"
	RoleTenantAdmin   = "tenant_admin"
	RoleAdmin         = RolePlatformAdmin
	RoleAnalyst       = "analyst"
	RoleViewer        = "viewer"
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

// apiKeyJSON is one element of SECURITY_API_KEYS_JSON. Unknown fields are rejected so a
// misspelled field ("keyhash") is an error, not a key that silently does not exist.
type apiKeyJSON struct {
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

var sha256HexPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// ParseAPIKeysJSON parses SECURITY_API_KEYS_JSON strictly. Every mistake that would leave a key
// missing, unrestricted or never expiring is an error: invalid JSON, unknown fields, an entry
// with neither key nor key_hash, a malformed key_hash, an unknown or missing role, an expires_at
// that is not RFC3339, and an unknown scope. An empty string yields no keys and no error.
func ParseAPIKeysJSON(raw string) ([]APIKeyEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	var parsed []apiKeyJSON
	if err := dec.Decode(&parsed); err != nil {
		return nil, fmt.Errorf("not a valid JSON array of keys: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON array")
	}
	out := make([]APIKeyEntry, 0, len(parsed))
	for i, e := range parsed {
		at := fmt.Sprintf("entry %d", i)
		if e.ID != "" {
			at = fmt.Sprintf("entry %d (id %q)", i, e.ID)
		}
		key, hash := strings.TrimSpace(e.Key), strings.TrimSpace(e.KeyHash)
		if key == "" && hash == "" {
			return nil, fmt.Errorf("%s has neither key nor key_hash", at)
		}
		if hash != "" && !sha256HexPattern.MatchString(hash) {
			return nil, fmt.Errorf("%s: key_hash must be 64 hexadecimal characters (SHA-256)", at)
		}
		if !IsKnownRole(e.Role) {
			return nil, fmt.Errorf("%s: role %q is not one of viewer, analyst, tenant_admin, platform_admin (or an alias); a missing role is refused too", at, e.Role)
		}
		entry := APIKeyEntry{Key: key, KeyHash: hash, ID: e.ID, Prefix: strings.TrimSpace(e.Prefix), Role: e.Role, OrgID: e.OrgID, Revoked: e.Revoked}
		for _, sc := range e.Scopes {
			if !IsKnownScope(sc) {
				return nil, fmt.Errorf("%s: scope %q is not one of admin, write, read", at, sc)
			}
			entry.Scopes = append(entry.Scopes, sc)
		}
		if entry.ID == "" {
			entry.ID = "api-key"
		}
		if entry.Prefix == "" && entry.Key != "" {
			entry.Prefix = keyPrefix(entry.Key)
		}
		if ts := strings.TrimSpace(e.ExpiresAt); ts != "" {
			t, err := time.Parse(time.RFC3339, ts)
			if err != nil {
				return nil, fmt.Errorf("%s: expires_at %q must be RFC3339, for example 2030-01-01T00:00:00Z", at, ts)
			}
			entry.ExpiresAt = t.UTC()
		}
		out = append(out, entry)
	}
	return out, nil
}

// ValidateCredentialSources is the startup check for the authentication settings. With
// authentication enabled it requires at least one usable credential source and refuses a
// configuration that would parse to none, so a typo cannot leave the server without keys.
func ValidateCredentialSources(enabled bool, apiKey, apiKeyHash, keysJSON string, oidcConfigured bool) error {
	keys, err := ParseAPIKeysJSON(keysJSON)
	if err != nil {
		return fmt.Errorf("SECURITY_API_KEYS_JSON: %w", err)
	}
	if h := strings.TrimSpace(apiKeyHash); h != "" && !sha256HexPattern.MatchString(h) {
		return fmt.Errorf("SECURITY_API_KEY_HASH must be 64 hexadecimal characters (SHA-256)")
	}
	if !enabled {
		return nil
	}
	if len(keys) == 0 && strings.TrimSpace(apiKey) == "" && strings.TrimSpace(apiKeyHash) == "" && !oidcConfigured {
		return fmt.Errorf("authentication is enabled but no credential source yields a key: SECURITY_API_KEYS_JSON holds no keys and no other source is set")
	}
	return nil
}

// loadAPIKeys builds the key list. Invalid JSON configuration yields no JSON keys; the startup
// check (ValidateCredentialSources) refuses to run with it, and AuthRequired stays true, so a
// caller that skips the check still gets 401 rather than open access.
func loadAPIKeys(primaryKey string, primaryKeyHash string, keysJSON string) []APIKeyEntry {
	out, err := ParseAPIKeysJSON(keysJSON)
	if err != nil {
		out = nil
	}
	if pk := strings.TrimSpace(primaryKey); pk != "" {
		out = append(out, APIKeyEntry{Key: pk, ID: "api-key", Role: RoleAdmin})
	}
	if ph := strings.TrimSpace(primaryKeyHash); ph != "" {
		out = append(out, APIKeyEntry{KeyHash: ph, ID: "api-key", Role: RoleAdmin})
	}
	return out
}

// AuthRequired reports whether requests on protected paths must be authenticated. It depends only
// on whether authentication is enabled, never on whether credentials happen to exist: an enabled
// server with no usable key answers 401, it does not fall back to an open admin principal.
func (a *Authenticator) AuthRequired() bool {
	return a != nil && a.enabled
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
			fallbackRole := RoleViewer // a token without a roles claim gets the least privilege
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
			role := RoleViewer
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

// roleAliases maps every accepted spelling onto its canonical role.
var roleAliases = map[string]string{
	// Explicit platform-wide administrators only. Legacy "admin" is tenant-scoped.
	RolePlatformAdmin: RolePlatformAdmin, "platform_administrator": RolePlatformAdmin, "global_admin": RolePlatformAdmin, "superadmin": RolePlatformAdmin,
	RoleTenantAdmin: RoleTenantAdmin, "admin": RoleTenantAdmin, "administrator": RoleTenantAdmin, "org_admin": RoleTenantAdmin,
	"organization_admin": RoleTenantAdmin, "organisation_admin": RoleTenantAdmin,
	RoleViewer: RoleViewer, "read": RoleViewer, "readonly": RoleViewer, "reader": RoleViewer,
	RoleAnalyst: RoleAnalyst,
}

func roleKey(role string) string { return strings.ToLower(strings.TrimSpace(role)) }

// IsKnownRole reports whether role is a recognised spelling. Callers that accept a role from a
// person or a configuration file use it to refuse a typo instead of guessing.
func IsKnownRole(role string) bool {
	_, ok := roleAliases[roleKey(role)]
	return ok
}

// IsKnownScope reports whether scope is one of admin, write or read.
func IsKnownScope(scope string) bool {
	switch roleKey(scope) {
	case "admin", "write", "read":
		return true
	}
	return false
}

// normalizeRole maps a role spelling to its canonical name. An unrecognised value becomes the
// least-privileged role, viewer: a typo such as "read-only" must never grant write access.
func normalizeRole(role string) string {
	if r, ok := roleAliases[roleKey(role)]; ok {
		return r
	}
	return RoleViewer
}

// NormalizeRole maps configured role aliases onto canonical RBAC role names.
func NormalizeRole(role string) string {
	return normalizeRole(role)
}

// CanAssignRole reports whether actorRole may grant targetRole to a membership or API key.
// Only platform admins may assign platform_admin.
func CanAssignRole(actorRole, targetRole string) bool {
	if !IsAdminRole(actorRole) {
		return false
	}
	if IsPlatformAdminRole(targetRole) {
		return IsPlatformAdminRole(actorRole)
	}
	return true
}

// IsPlatformAdminRole reports whether the normalized role is a platform-wide admin.
func IsPlatformAdminRole(role string) bool {
	return normalizeRole(role) == RolePlatformAdmin
}

// IsTenantAdminRole reports whether the normalized role is a tenant-scoped admin.
func IsTenantAdminRole(role string) bool {
	return normalizeRole(role) == RoleTenantAdmin
}

// IsAdminRole reports whether the role is any admin class.
func IsAdminRole(role string) bool {
	switch normalizeRole(role) {
	case RolePlatformAdmin, RoleTenantAdmin:
		return true
	default:
		return false
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
	if IsAdminRole(role) {
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
	// Query Investigation is the analyst hero path: create, suggest/rank, compare, report.
	if strings.HasPrefix(path, "/api/v1/investigations") {
		return true
	}
	if strings.HasPrefix(path, "/api/v1/workspace/regressions/") && strings.HasSuffix(path, "/acknowledge") {
		return true
	}
	allowed := []string{
		"/api/v1/queries/run",
		"/api/v1/queries/explain",
		"/api/v1/queries/explain/compare",
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
	// Retrying a schedule run is the same "run this now" capability as
	// /schedules/{id}/run above, just scoped to an existing run instead of the
	// schedule itself.
	if strings.HasPrefix(path, "/api/v1/schedule-runs/") && strings.HasSuffix(path, "/retry") {
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
