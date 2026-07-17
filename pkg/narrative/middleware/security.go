package middleware

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/httpmw"
	"github.com/pgquerynarrative/pgquerynarrative/app/ratelimit"
	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
)

// SecurityConfig mirrors standalone-server auth and rate-limit wiring for embedders.
type SecurityConfig struct {
	Authenticator  *auth.Authenticator
	Sessions       *auth.SessionManager
	AuditStore     *audit.Store
	RateLimiter    ratelimit.AllowFunc
	TrustedProxies []string
}

// WrapSecured applies the same auth and rate-limit middleware as cmd/server.
func WrapSecured(next http.Handler, sec SecurityConfig) http.Handler {
	trusted := httpmw.NewTrustedProxyMatcher(sec.TrustedProxies)
	h := httpmw.AuthMiddleware(next, sec.Authenticator, sec.Sessions, sec.AuditStore, trusted)
	return httpmw.RateLimitMiddleware(h, sec.RateLimiter, sec.AuditStore, trusted)
}

// MountChiSecured mounts narrative routes under prefix with auth and rate-limit parity to the standalone server.
func MountChiSecured(r chi.Router, client *narrative.Client, prefix string, sec SecurityConfig) {
	prefix = normalizePrefix(prefix)
	trusted := httpmw.NewTrustedProxyMatcher(sec.TrustedProxies)
	r.Group(func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return httpmw.AuthMiddleware(next, sec.Authenticator, sec.Sessions, sec.AuditStore, trusted)
		})
		r.Use(func(next http.Handler) http.Handler {
			return httpmw.RateLimitMiddleware(next, sec.RateLimiter, sec.AuditStore, trusted)
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
