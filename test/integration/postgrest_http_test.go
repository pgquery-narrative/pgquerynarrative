package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	goahttp "goa.design/goa/v3/http"

	invclient "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/investigations/client"
	invserver "github.com/pgquerynarrative/pgquerynarrative/api/gen/http/investigations/server"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/internal/config"
	"github.com/pgquerynarrative/pgquerynarrative/internal/db"
	"github.com/pgquerynarrative/pgquerynarrative/internal/middleware"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

type postgrestAPIDoer struct{ client *http.Client }

func (d postgrestAPIDoer) Do(r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer postgrest-test-admin")
	return d.client.Do(r)
}

// Exercises generated HTTP codecs, real auth and connection authorization,
// RLS-enforced metadata storage, exact text binds, and a useful generated index.
func TestPostgRESTHTTPWorkflow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	setup := pgrstSetupSQL + `
CREATE TABLE data.accounts (id bigint PRIMARY KEY, code text NOT NULL);
INSERT INTO data.accounts SELECT g, CASE WHEN g % 5000 = 1 THEN '001' ELSE 'code-' || g END FROM generate_series(1, 100000) g;
CREATE VIEW api.accounts WITH (security_invoker = true) AS SELECT * FROM data.accounts;
GRANT SELECT ON data.accounts, api.accounts TO web_anon, pqn_reader;
ANALYZE data.accounts;
`
	stack := helpers.RunPostgRESTStack(t, ctx, setup, pgrstJWTSecret)
	admin, err := pgxpool.New(ctx, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	app, err := helpers.AppPoolFromAdmin(ctx, admin, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	reader, err := pgxpool.New(ctx, stack.ReaderConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	if _, err := admin.Exec(ctx, `INSERT INTO app.organization_connections (organization_id, connection_id) VALUES ($1, 'default') ON CONFLICT DO NOTHING`, auth.DefaultOrganizationID); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `DELETE FROM app.connection_permissions WHERE organization_id = $1 AND principal_id = 'analyst'`, auth.DefaultOrganizationID); err != nil {
		t.Fatal(err)
	}
	appDB := db.NewOrgScoped(app)
	runner := queryrunner.NewRunner(reader, queryrunner.NewValidator([]string{"api", "data"}, 10000), 5000, 30*time.Second, queryrunner.WithExplainAnalyze(true))
	qs := service.NewQueriesService(reader, appDB, runner, config.MetricsConfig{})
	authorizer := auth.NewConnectionAuthorizer(app)
	authorizer.SetAllowlistRequired(true)
	qs.SetAuthorizer(authorizer)
	rs := service.NewReportsService(reader, appDB, runner, noopLLM{}, config.MetricsConfig{})
	is := service.NewInvestigationsService(appDB, qs, rs)
	mux := goahttp.NewMuxer()
	invserver.Mount(mux, invserver.New(investigations.NewEndpoints(is), mux, goahttp.RequestDecoder, goahttp.ResponseEncoder, nil, nil))
	authenticator := auth.NewAuthenticator(true, "postgrest-test-admin", "", `[
 {"key":"postgrest-test-viewer","id":"viewer","role":"viewer"},
 {"key":"postgrest-test-analyst","id":"analyst","role":"analyst"},
 {"key":"postgrest-test-other","id":"other","role":"admin","org_id":"11111111-2222-3333-4444-555555555555"}
]`, nil)
	srv := httptest.NewServer(http.MaxBytesHandler(middleware.AuthMiddleware(mux, authenticator, nil, nil, nil), 1<<20))
	t.Cleanup(srv.Close)
	httpClient := &http.Client{Timeout: 40 * time.Second}
	api := invclient.NewClient("http", strings.TrimPrefix(srv.URL, "http://"), postgrestAPIDoer{httpClient}, goahttp.RequestEncoder, goahttp.ResponseDecoder, false)
	call := func(method, path, key string, payload any) int {
		t.Helper()
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(ctx, method, srv.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		return resp.StatusCode
	}

	logs, err := stack.PostgreSQLLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	offset := len(logs)
	endpoint := "/accounts?code=eq.001&select=id,code&order=id&limit=3"
	beforePage := pgrstGET(t, stack.BaseURL+endpoint, "")
	if beforePage.status != http.StatusOK || jsonArrayLen(t, beforePage.body) != 3 {
		t.Fatalf("endpoint did not return the text-filtered page: %d %s", beforePage.status, beforePage.body)
	}
	var excerpt string
	for deadline := time.Now().Add(10 * time.Second); ; {
		logs, err = stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		excerpt = logs[offset:]
		if _, err = queryrunner.ParsePostgRESTLog(excerpt); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("capture did not become readable: %v", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	const intake = "/api/v1/investigations/from-postgrest"
	for _, tc := range []struct {
		name, key string
		body      any
		status    int
	}{
		{"anonymous", "", map[string]any{"postgres_log": excerpt}, 401},
		{"viewer", "postgrest-test-viewer", map[string]any{"postgres_log": excerpt}, 403},
		{"connection permission", "postgrest-test-analyst", map[string]any{"postgres_log": excerpt}, 400},
		{"missing log", "postgrest-test-admin", map[string]any{}, 400},
		{"negative timing", "postgrest-test-admin", map[string]any{"postgres_log": excerpt, "http_duration_ms": -1}, 400},
		{"oversized log", "postgrest-test-admin", map[string]any{"postgres_log": strings.Repeat("x", 200001)}, 400},
		{"unsupported log", "postgrest-test-admin", map[string]any{"postgres_log": "invalid capture"}, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := call(http.MethodPost, intake, tc.key, tc.body); got != tc.status {
				t.Fatalf("HTTP %d, want %d", got, tc.status)
			}
		})
	}
	var count int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.investigations`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("denied requests persisted evidence: count=%d err=%v", count, err)
	}

	result, err := api.CreateFromPostgrest()(ctx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
	if err != nil {
		t.Fatalf("HTTP capture intake: %v", err)
	}
	inv := result.(*investigations.Investigation)
	if inv.Binds[0] != "001" || inv.PostgrestRequest == nil || inv.Explain == nil || inv.Explain.EvidenceMode != "observed" {
		t.Fatalf("capture was not faithfully replayed: %+v", inv)
	}
	// Read the persisted row without organization context as the actual app role.
	if err := app.QueryRow(ctx, `SELECT count(*) FROM app.investigations WHERE id = $1`, inv.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("RLS did not deny unscoped read: count=%d err=%v", count, err)
	}
	if got := call(http.MethodGet, "/api/v1/investigations/"+inv.ID, "postgrest-test-other", nil); got != 404 {
		t.Fatalf("cross-org HTTP read = %d", got)
	}
	result, err = api.RankCandidates()(ctx, &investigations.RankCandidatesPayload{ID: inv.ID})
	if err != nil {
		t.Fatal(err)
	}
	var ddl string
	for _, candidate := range result.(*investigations.RankedCandidateList).Candidates {
		if candidate.Ddl != nil && strings.Contains(*candidate.Ddl, `"accounts"`) && strings.Contains(*candidate.Ddl, `"code"`) {
			ddl = *candidate.Ddl
			break
		}
	}
	if ddl == "" {
		t.Fatal("product did not propose an index for the observed text predicate")
	}
	if !strings.Contains(ddl, `"data"."accounts"`) {
		t.Fatalf("index must target the scanned base table, not the same-named API view: %s", ddl)
	}
	// Only this test's administrator applies DDL, only in its disposable database.
	if _, err := admin.Exec(ctx, ddl); err != nil {
		t.Fatalf("apply generated DDL %s: %v", ddl, err)
	}
	result, err = api.CreateFromPostgrest()(ctx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
	if err != nil {
		t.Fatal(err)
	}
	after := result.(*investigations.Investigation)
	if after.Explain.TotalCost >= inv.Explain.TotalCost {
		t.Fatalf("generated index failed to improve plan: %.2f -> %.2f", inv.Explain.TotalCost, after.Explain.TotalCost)
	}
	t.Logf("system-generated index: planner cost %.2f -> %.2f; observed replay execution ms %v -> %v (HTTP latency not inferred)", inv.Explain.TotalCost, after.Explain.TotalCost, *inv.Explain.ServerExecutionTimeMs, *after.Explain.ServerExecutionTimeMs)
	afterPage := pgrstGET(t, stack.BaseURL+endpoint, "")
	if afterPage.status != 200 || afterPage.body != beforePage.body || afterPage.header.Get("Content-Range") != beforePage.header.Get("Content-Range") {
		t.Fatal("generated index changed the endpoint's page or response status")
	}

	// Candidate comparisons must automatically use the exact captured text binds.
	result, err = api.AddCandidate()(ctx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL, VerifyResults: true})
	if err != nil {
		t.Fatal(err)
	}
	compared := result.(*investigations.Investigation)
	if compared.Comparison == nil || compared.Comparison.ResultEquivalenceStatus == nil || *compared.Comparison.ResultEquivalenceStatus != "VerifiedEqual" || len(compared.Candidates) != 1 || compared.Candidates[0].Binds[0] != "001" {
		t.Fatalf("captured-bind comparison not verified/persisted: %+v", compared.Comparison)
	}
	if _, err := api.GenerateReport()(ctx, &investigations.GenerateReportPayload{ID: inv.ID}); err != nil {
		t.Fatal(err)
	}
	// Different results must still block a shippable report for imported requests.
	differentSQL := strings.Replace(inv.SQL, `= $1`, `= 'missing-account'`, 1)
	result, err = api.AddCandidate()(ctx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: differentSQL, VerifyResults: true})
	if err != nil {
		t.Fatal(err)
	}
	if cmp := result.(*investigations.Investigation).Comparison; cmp.ResultEquivalenceStatus == nil || *cmp.ResultEquivalenceStatus != "Different" {
		t.Fatalf("different captured query reported as %+v", cmp)
	}
	if _, err := api.GenerateReport()(ctx, &investigations.GenerateReportPayload{ID: inv.ID}); err == nil {
		t.Fatal("report accepted a Different comparison")
	}
	if _, err := api.AddCandidate()(ctx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL, Binds: []string{"1", "3", "0"}}); err == nil {
		t.Fatal("comparison accepted changed captured binds")
	}
}
