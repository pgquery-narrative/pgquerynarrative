package httpmw

import (
	"net/http"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/audit"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/observability"
	"github.com/pgquerynarrative/pgquerynarrative/app/ratelimit"
)

// AuthMiddleware requires Bearer token or browser session for protected routes when enabled.
func AuthMiddleware(next http.Handler, authenticator *auth.Authenticator, sessions *auth.SessionManager, auditStore *audit.Store, trusted *TrustedProxyMatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "" {
			path = "/"
		}
		isPublicSharedAPI := strings.HasPrefix(path, "/api/v1/reports/shared/")
		isPublicSharedPDF := path == "/web/reports/export/shared/pdf"
		isAuthRoute := strings.HasPrefix(path, "/auth/")
		needAuth := (strings.HasPrefix(path, "/api/") && !isPublicSharedAPI) ||
			path == "/metrics" ||
			((path == "/web/reports/export" || path == "/web/reports/export/pdf") && !isPublicSharedPDF)
		// Public share routes must not inherit the default-org admin principal.
		if isPublicSharedAPI || isPublicSharedPDF {
			next.ServeHTTP(w, r)
			return
		}
		if isAuthRoute || authenticator == nil || !authenticator.AuthRequired() || !needAuth {
			ctx := auth.WithPrincipal(r.Context(), auth.PrincipalFromContext(r.Context()))
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		var principal auth.Principal
		var ok bool
		if sessions != nil && sessions.Enabled() {
			principal, ok = auth.ValidateSessionCookie(r, sessions)
		}
		if !ok {
			principal, ok = authenticator.ValidatePrincipal(r)
		}
		if !ok {
			observability.IncAuthFailure()
			clientIP := ClientIPFromRequest(r, trusted)
			if auditStore != nil {
				auditStore.Record(r.Context(), audit.Entry{
					EventType: audit.EventAuthFailure,
					Details:   map[string]interface{}{"path": path},
					IP:        clientIP,
					UserAgent: r.UserAgent(),
				})
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"name":"unauthorized","message":"missing or invalid Authorization","code":"UNAUTHORIZED"}`))
			return
		}
		if !auth.AllowsMethod(principal.Role, r.Method, path) {
			observability.IncAuthzDenial()
			auth.WriteForbidden(w)
			return
		}
		ctx := auth.WithPrincipal(r.Context(), principal)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RateLimitMiddleware limits requests per client IP when limiter is non-nil.
func RateLimitMiddleware(next http.Handler, limiter ratelimit.AllowFunc, auditStore *audit.Store, trusted *TrustedProxyMatcher) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key := ClientIPFromRequest(r, trusted)
		if !limiter.Allow(key) {
			if auditStore != nil {
				auditStore.Record(r.Context(), audit.Entry{
					EventType: audit.EventRateLimitExceeded,
					Details:   map[string]interface{}{"path": r.URL.Path, "client": key},
					IP:        key,
					UserAgent: r.UserAgent(),
				})
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"name":"rate_limit_exceeded","message":"too many requests","code":"RATE_LIMIT_EXCEEDED"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
