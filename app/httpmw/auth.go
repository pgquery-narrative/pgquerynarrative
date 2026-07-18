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
				_ = auditStore.Record(r.Context(), audit.Entry{
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

// Route classes used to key rate-limit buckets and to decide failure-mode overrides.
// AI/report routes trigger LLM calls and/or heavier query execution, so they are treated
// as higher-sensitivity for rate-limit storage failure handling (see RateLimitMiddleware).
const (
	routeClassAI       = "ai"
	routeClassShare    = "share"
	routeClassStandard = "standard"
)

// routeClassForPath classifies a request path for rate-limit bucketing and failure-mode
// policy. Public share endpoints are isolated in their own "share" class so token-guessing
// traffic cannot exhaust AI budgets. Report generation/rewrite and suggestion endpoints
// invoke the LLM and are classified "ai"; everything else is "standard".
func routeClassForPath(path string) string {
	switch {
	case strings.HasPrefix(path, "/api/v1/reports/shared/"), path == "/web/reports/export/shared/pdf":
		return routeClassShare
	case strings.HasPrefix(path, "/api/v1/reports"):
		return routeClassAI
	case strings.HasPrefix(path, "/api/v1/suggestions"):
		return routeClassAI
	default:
		return routeClassStandard
	}
}

// RateLimitKey builds a distributed rate-limit bucket key for r. When the request carries a
// recognizable identity (browser session cookie, or a bearer credential that
// Authenticator.PeekPrincipal can validate locally without a DB round trip), the key is
// scoped to org + identity + route class so different tenants/users/route classes get
// independent budgets. Requests with no recognizable identity (public/shared routes, or
// invalid/missing credentials that AuthMiddleware will itself reject) fall back to client IP,
// preserving IP-based brute-force protection for unauthenticated traffic.
func RateLimitKey(r *http.Request, trusted *TrustedProxyMatcher, authenticator *auth.Authenticator, sessions *auth.SessionManager) (key, routeClass string) {
	routeClass = routeClassForPath(r.URL.Path)
	if sessions != nil && sessions.Enabled() {
		if p, ok := auth.ValidateSessionCookie(r, sessions); ok && p.OrgID != "" && p.UserID != "" {
			return p.OrgID + ":" + p.UserID + ":" + routeClass, routeClass
		}
	}
	if authenticator != nil {
		if p, ok := authenticator.PeekPrincipal(r); ok && p.OrgID != "" && p.UserID != "" {
			return p.OrgID + ":" + p.UserID + ":" + routeClass, routeClass
		}
	}
	return "ip:" + ClientIPFromRequest(r, trusted), routeClass
}

// RateLimitMiddleware limits requests per client key (org+identity+route class when the
// request carries recognizable credentials, otherwise client IP) when limiter is non-nil.
// mode is the configured storage-failure policy (open/closed/local_fallback); when strict is
// true, AI/report routes always fail closed on storage failure regardless of mode, since those
// routes are the most expensive to let through uncontrolled.
func RateLimitMiddleware(next http.Handler, limiter ratelimit.AllowFunc, auditStore *audit.Store, trusted *TrustedProxyMatcher, authenticator *auth.Authenticator, sessions *auth.SessionManager, mode ratelimit.FailureMode, strict bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if limiter == nil {
			next.ServeHTTP(w, r)
			return
		}
		key, routeClass := RateLimitKey(r, trusted, authenticator, sessions)
		effectiveMode := mode
		if strict && (routeClass == routeClassAI || routeClass == routeClassShare) {
			effectiveMode = ratelimit.FailClosed
		}
		var allowed bool
		if aware, ok := limiter.(ratelimit.ModeAwareAllower); ok {
			allowed = aware.AllowWithMode(r.Context(), key, effectiveMode)
		} else {
			allowed = limiter.Allow(key)
		}
		if !allowed {
			if auditStore != nil {
				_ = auditStore.Record(r.Context(), audit.Entry{
					EventType: audit.EventRateLimitExceeded,
					Details:   map[string]interface{}{"path": r.URL.Path, "client": key, "route_class": routeClass},
					IP:        ClientIPFromRequest(r, trusted),
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
