package middleware

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgquerynarrative/pgquerynarrative/internal/audit"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	servermw "github.com/pgquerynarrative/pgquerynarrative/internal/middleware"
	"github.com/pgquerynarrative/pgquerynarrative/internal/ratelimit"
	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
)

// SecurityConfig mirrors standalone-server auth and rate-limit wiring for embedders.
type SecurityConfig struct {
	Authenticator  *auth.Authenticator
	Sessions       *auth.SessionManager
	AuditStore     *audit.Store
	RateLimiter    ratelimit.AllowFunc
	TrustedProxies []string
	// RateLimitFailureMode controls behavior when the distributed limiter's storage fails.
	// Empty defaults to ratelimit.FailOpen (see ratelimit.ParseFailureMode).
	RateLimitFailureMode ratelimit.FailureMode
	// StrictAIFailClosed forces AI/report routes to fail closed on limiter storage failure
	// regardless of RateLimitFailureMode, mirroring the standalone server's production policy.
	StrictAIFailClosed bool
}

// NewAuthenticator builds the *auth.Authenticator SecurityConfig.Authenticator needs.
// internal/auth isn't importable outside this module, so this is the only way an embedder can
// populate that field with a real value instead of leaving it nil (which disables auth entirely).
// oidc is optional (nil disables OIDC, matching a zero-value cmd/server wiring) — build one with
// NewOIDCValidator.
func NewAuthenticator(enabled bool, primaryKey, primaryKeyHash, keysJSON string, oidc *auth.OIDCValidator) *auth.Authenticator {
	return auth.NewAuthenticator(enabled, primaryKey, primaryKeyHash, keysJSON, oidc)
}

// OIDCConfig configures NewOIDCValidator. Mirrors internal/auth.OIDCConfig's public fields —
// that type itself can't be named outside this module, so this is the public equivalent.
type OIDCConfig struct {
	Issuer     string
	Audience   string
	JWKSURL    string // Defaults to Issuer + "/.well-known/jwks.json" when empty.
	StrictMode bool   // Require aud/exp/iat/sub, reject a missing kid, no single-key fallback.
}

// NewOIDCValidator builds the *auth.OIDCValidator NewAuthenticator's oidc parameter needs, for
// the same reason NewAuthenticator itself exists: internal/auth can't be imported directly.
// Returns nil (OIDC disabled) when cfg.Issuer is empty, matching internal/auth.NewOIDCValidator.
func NewOIDCValidator(cfg OIDCConfig) *auth.OIDCValidator {
	return auth.NewOIDCValidator(auth.OIDCConfig{
		Issuer:     cfg.Issuer,
		Audience:   cfg.Audience,
		JWKSURL:    cfg.JWKSURL,
		StrictMode: cfg.StrictMode,
	})
}

// NewSessionManager builds the *auth.SessionManager SecurityConfig.Sessions needs, for the same
// reason NewAuthenticator exists: internal/auth can't be imported directly by embedders.
func NewSessionManager(secret string, ttl time.Duration, secure bool) *auth.SessionManager {
	return auth.NewSessionManager(secret, ttl, secure)
}

// NewSessionStore builds a database-backed store for SessionManager.AttachSessionStore, so a
// revoked session is actually invalidated server-side instead of only clearing the browser
// cookie (a bare NewSessionManager with no attached store can't revoke a copy of the cookie the
// caller already saved elsewhere). Same internal/auth constraint as NewMembershipStore: the type
// is internal, but the constructor only needs a *pgxpool.Pool.
func NewSessionStore(pool *pgxpool.Pool) *auth.SessionStore {
	return auth.NewSessionStore(pool)
}

// NewMembershipStore builds a membership resolver for Authenticator.SetMembershipStore, so an
// API key or OIDC principal with no explicit org_id resolves to the caller's real organization
// membership instead of always falling back to the default org. Same internal/auth constraint as
// NewAuthenticator: the type is internal, but this constructor only needs public parameter types
// (a *pgxpool.Pool and a bool), so wrapping it is enough to make it usable externally.
func NewMembershipStore(pool *pgxpool.Pool, autoJoinDefault bool) *auth.MembershipStore {
	return auth.NewMembershipStore(pool, autoJoinDefault)
}

// NewManagedKeyStore builds a durable hashed-API-key store for Authenticator.SetManagedKeyStore
// (the CLI/MCP key issuance path, as opposed to the static SECURITY_API_KEYS_JSON keys
// NewAuthenticator alone covers). Same internal/auth constraint as NewMembershipStore.
func NewManagedKeyStore(pool *pgxpool.Pool) *auth.ManagedKeyStore {
	return auth.NewManagedKeyStore(pool)
}

// WrapSecured applies the same auth and rate-limit middleware as cmd/server.
func WrapSecured(next http.Handler, sec SecurityConfig) http.Handler {
	trusted := servermw.NewTrustedProxyMatcher(sec.TrustedProxies)
	h := servermw.AuthMiddleware(next, sec.Authenticator, sec.Sessions, sec.AuditStore, trusted)
	return servermw.RateLimitMiddleware(h, sec.RateLimiter, sec.AuditStore, trusted, sec.Authenticator, sec.Sessions, sec.RateLimitFailureMode, sec.StrictAIFailClosed)
}

// MountChiSecured mounts narrative routes under prefix with auth and rate-limit parity to the standalone server.
func MountChiSecured(r chi.Router, client *narrative.Client, prefix string, sec SecurityConfig) {
	prefix = normalizePrefix(prefix)
	trusted := servermw.NewTrustedProxyMatcher(sec.TrustedProxies)
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return servermw.AuthMiddleware(next, sec.Authenticator, sec.Sessions, sec.AuditStore, trusted)
		})
		r.Use(func(next http.Handler) http.Handler {
			return servermw.RateLimitMiddleware(next, sec.RateLimiter, sec.AuditStore, trusted, sec.Authenticator, sec.Sessions, sec.RateLimitFailureMode, sec.StrictAIFailClosed)
		})
		if prefix != "" {
			r.Route(prefix, func(r chi.Router) {
				mountChiRoutes(r, client)
			})
		} else {
			mountChiRoutes(r, client)
		}
	})
}

func normalizePrefix(prefix string) string {
	for len(prefix) > 0 && prefix[len(prefix)-1] == '/' {
		prefix = prefix[:len(prefix)-1]
	}
	return prefix
}
