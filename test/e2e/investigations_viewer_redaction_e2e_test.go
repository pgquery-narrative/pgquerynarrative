package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	goahttp "goa.design/goa/v3/http"

	investigationsServer "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/investigations/server"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/internal/config"
	"github.com/pgquerynarrative/pgquerynarrative/internal/db"
	"github.com/pgquerynarrative/pgquerynarrative/internal/middleware"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// TestInvestigationsViewerRedactionOverRealHTTP exercises investigation GET
// and LIST through the actual HTTP authorization and serialization path —
// real Authorization: Bearer requests, the real middleware.AuthMiddleware
// and auth.Authenticator (API-key role resolution), the real generated Goa
// mux and encoders — rather than calling InvestigationsService methods
// directly. Unlike the rest of this package's E2E tests (which use
// withTestPrincipal to inject a fixed admin principal and skip auth
// entirely), this test mounts the real AuthMiddleware so role-based
// response shaping (applyInvestigationVisibility in
// internal/service/investigations.go) is proven to take effect on an actual
// wire response, not just in a direct service-method call.
//
// The planted literal contains an em dash ("public — bearer-secret@..."),
// the exact shape that used to defeat RedactLiteralFilterText's old
// boundary scan (see TestRedactLiteralFilterTextCannotBeTrickedByEmDashInLiteral
// in internal/story) — confirming the fix holds through the full response,
// not only through the helper function in isolation.
func TestInvestigationsViewerRedactionOverRealHTTP(t *testing.T) {
	ctx := context.Background()
	container, connStr := StartPostgres(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	WaitPostgres(t, ctx, connStr)
	helpers.RunMigrations(t, connStr)
	pool := NewTestPool(t, ctx, connStr)
	defer pool.Close()

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesService := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	reportsService := service.NewReportsService(pool, appDB, runner, &e2eLLM{}, config.MetricsConfig{})
	investigationsService := service.NewInvestigationsService(appDB, queriesService, reportsService)
	endpoints := investigations.NewEndpoints(investigationsService)

	mux := goahttp.NewMuxer()
	dec := goahttp.RequestDecoder
	enc := goahttp.ResponseEncoder
	errHandler := func(ctx context.Context, w http.ResponseWriter, err error) {
		_ = goahttp.ErrorEncoder(enc, nil)(ctx, w, err)
	}
	investigationsServer.Mount(mux, investigationsServer.New(endpoints, mux, dec, enc, errHandler, nil))

	const (
		adminKeyRaw    = "e2e-redaction-admin-7f1c9a"
		viewerKeyRaw   = "e2e-redaction-viewer-3b8d2e"
		analystKeyRaw  = "e2e-redaction-analyst-9c4f1a"
		otherOrgKeyRaw = "e2e-redaction-otherorg-5e2a7b"
		otherOrgID     = "11111111-2222-3333-4444-555555555555"
	)
	keysJSON := fmt.Sprintf(`[
		{"key_hash":%q,"id":"admin","role":"admin"},
		{"key_hash":%q,"id":"viewer","role":"viewer"},
		{"key_hash":%q,"id":"analyst","role":"analyst"},
		{"key_hash":%q,"id":"other-org","role":"admin","org_id":%q}
	]`, auth.HashAPIKey(adminKeyRaw), auth.HashAPIKey(viewerKeyRaw), auth.HashAPIKey(analystKeyRaw), auth.HashAPIKey(otherOrgKeyRaw), otherOrgID)
	authenticator := auth.NewAuthenticator(true, "", "", keysJSON, nil)
	handler := middleware.AuthMiddleware(mux, authenticator, nil, nil, nil)
	testServer := httptest.NewServer(handler)
	t.Cleanup(testServer.Close)
	base := testServer.URL

	doJSON := func(t *testing.T, method, path, bearer string, body any) (*http.Response, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			b, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			reader = bytes.NewReader(b)
		}
		req, err := http.NewRequest(method, base+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp, respBody
	}

	// A literal with an em dash in the filtered column, producing a plan
	// finding whose Message/Evidence and raw Plan JSON embed it verbatim —
	// exercising every redaction surface (SQL, Binds, raw Plan,
	// Findings[].Message/Evidence, candidate history), not just report.SQL.
	const secretEmail = "public — bearer-secret@example.test"
	sourceSQL := "SELECT region, sales_rep FROM demo.sales WHERE sales_rep = '" + secretEmail + "'"
	candidateSQL := sourceSQL + " AND 1=1"

	resp, body := doJSON(t, http.MethodPost, "/api/v1/investigations", adminKeyRaw, map[string]any{
		"title": "viewer redaction e2e",
		"sql":   sourceSQL,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create: status=%d body=%s", resp.StatusCode, body)
	}
	var created investigations.Investigation
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode create response: %v (body=%s)", err, body)
	}

	resp, body = doJSON(t, http.MethodPost, "/api/v1/investigations/"+created.ID+"/candidate", adminKeyRaw, map[string]any{
		"candidate_sql": candidateSQL,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("add candidate: status=%d body=%s", resp.StatusCode, body)
	}

	// Sanity check: the unredacted (admin) record really does carry the
	// secret and both SQL texts, so the assertions below fail for the right
	// reason if they ever do.
	resp, fixtureBody := doJSON(t, http.MethodGet, "/api/v1/investigations/"+created.ID, adminKeyRaw, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fixture GET as admin: status=%d body=%s", resp.StatusCode, fixtureBody)
	}
	for _, marker := range []string{secretEmail, sourceSQL, candidateSQL} {
		if !strings.Contains(string(fixtureBody), marker) {
			t.Fatalf("fixture setup is broken: %q missing from unredacted (admin) response: %s", marker, fixtureBody)
		}
	}

	assertNoSecret := func(t *testing.T, label string, body []byte) {
		t.Helper()
		for _, marker := range []string{secretEmail, sourceSQL, candidateSQL} {
			if strings.Contains(string(body), marker) {
				t.Errorf("%s: leaked marker %q over real HTTP: %s", label, marker, body)
			}
		}
	}
	assertHasSecret := func(t *testing.T, label string, body []byte) {
		t.Helper()
		for _, marker := range []string{secretEmail, sourceSQL, candidateSQL} {
			if !strings.Contains(string(body), marker) {
				t.Errorf("%s: expected marker %q present for authorized access, missing from: %s", label, marker, body)
			}
		}
	}

	t.Run("viewer GET is redacted over real HTTP", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodGet, "/api/v1/investigations/"+created.ID, viewerKeyRaw, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		assertNoSecret(t, "GET as viewer", body)
	})

	t.Run("viewer LIST is redacted over real HTTP", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodGet, "/api/v1/investigations", viewerKeyRaw, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		if !strings.Contains(string(body), created.ID) {
			t.Fatalf("list response does not include the seeded investigation: %s", body)
		}
		assertNoSecret(t, "LIST as viewer", body)
	})

	t.Run("analyst GET keeps full evidence over real HTTP", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodGet, "/api/v1/investigations/"+created.ID, analystKeyRaw, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		assertHasSecret(t, "GET as analyst", body)
	})

	t.Run("analyst LIST keeps full evidence over real HTTP", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodGet, "/api/v1/investigations", analystKeyRaw, nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status=%d body=%s", resp.StatusCode, body)
		}
		assertHasSecret(t, "LIST as analyst", body)
	})

	t.Run("cross-organization access is still a 404 over real HTTP, not merely redacted", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodGet, "/api/v1/investigations/"+created.ID, otherOrgKeyRaw, nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("another organization's admin got status=%d, want 404: %s", resp.StatusCode, body)
		}
	})

	t.Run("no credential is rejected by the real auth middleware", func(t *testing.T) {
		resp, body := doJSON(t, http.MethodGet, "/api/v1/investigations/"+created.ID, "", nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated request got status=%d, want 401 — confirms this test is actually driving AuthMiddleware: %s", resp.StatusCode, body)
		}
	})
}
