package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
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
