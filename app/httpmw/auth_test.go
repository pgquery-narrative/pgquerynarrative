package httpmw

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

const testKey = "route-matrix-secret-key-0123456789"

func serve(t *testing.T, path, bearer string) (status int, reached bool, role string) {
	t.Helper()
	authn := auth.NewAuthenticator(true, testKey, "", "", nil)
	h := AuthMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		role = auth.PrincipalFromContext(r.Context()).Role
		w.WriteHeader(http.StatusOK)
	}), authn, nil, nil, nil)
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		r.Header.Set("Authorization", "Bearer "+bearer)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w.Code, reached, role
}

// Public on purpose: probes, the login flow, share links, and the SPA. Nothing else may be reachable
// without a credential.
var publicPaths = map[string]bool{
	"/health": true, "/ready": true, "/ready/connections": true, "/version": true,
	"/api/v1/reports/shared/some-token": true, "/web/reports/export/shared/pdf": true,
	"/auth/login": true, "/auth/callback": true, "/auth/logout": true, "/auth/refresh": true, "/auth/session": true,
	"/": true,
}

func TestProtectedRoutesRequireACredential(t *testing.T) {
	protected := []string{
		"/metrics", "/api/v1/me", "/api/v1/queries/run", "/api/v1/admin/api-keys", "/api/v1/settings",
		"/api/v1/diagnostics/db-privileges", "/api/v1/reports/x",
		"/web/reports/export", "/web/reports/export/pdf", "/web/reports/export/md", "/web/reports/export/json",
		"/web/reports/export/sql", "/web/reports/export/anything-new",
	}
	for _, p := range protected {
		if code, reached, _ := serve(t, p, ""); code != http.StatusUnauthorized || reached {
			t.Errorf("%s without a credential: status %d, handler reached %v", p, code, reached)
		}
		if code, reached, _ := serve(t, p, "wrong-token-value"); code != http.StatusUnauthorized || reached {
			t.Errorf("%s with a wrong token: status %d, handler reached %v", p, code, reached)
		}
		if code, reached, role := serve(t, p, testKey); code != http.StatusOK || !reached || role == "" {
			t.Errorf("%s with the key: status %d, reached %v", p, code, reached)
		}
	}
}

func TestPublicRoutesStayPublic(t *testing.T) {
	for p := range publicPaths {
		if code, reached, _ := serve(t, p, ""); code != http.StatusOK || !reached {
			t.Errorf("%s: status %d, reached %v", p, code, reached)
		}
	}
}

// Every route main.go registers must be either in publicPaths or refuse an anonymous request, so a
// new handler cannot be added without a decision. F1 was exactly this: three export routes were added
// and never listed.
func TestEveryRegisteredRouteIsDecided(t *testing.T) {
	src, err := os.ReadFile("../../cmd/server/main.go")
	if err != nil {
		t.Skip("cmd/server/main.go not found")
	}
	re := regexp.MustCompile(`combinedMux\.Handle(?:Func)?\("(/[^"]*)"`)
	found := 0
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		p := m[1]
		found++
		if p == "/api/" {
			p = "/api/v1/anything"
		}
		code, reached, _ := serve(t, p, "")
		if publicPaths[p] {
			continue
		}
		if code != http.StatusUnauthorized || reached {
			t.Errorf("registered route %s is reachable without a credential (status %d) and is not listed as public", m[1], code)
		}
	}
	if found < 10 {
		t.Fatalf("only %d routes found in main.go; the pattern is stale", found)
	}
}
