package integration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/internal/config"
	"github.com/pgquerynarrative/pgquerynarrative/internal/db"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/service"
	"github.com/pgquerynarrative/pgquerynarrative/internal/story"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

const pgrstJWTSecret = "pgrst-integration-test-secret-0123456789"

// pgrstSetupSQL builds a 1M-row table, an intentionally inefficient view that
// filters on date_trunc(created_at) and orders by id, and a row-level-security
// policy that narrows the tenant_b role. The view uses security_invoker so RLS is
// evaluated for the PostgREST role, as it is in production.
const pgrstSetupSQL = `
CREATE ROLE web_anon NOLOGIN;
CREATE ROLE tenant_b NOLOGIN;
CREATE ROLE authenticator LOGIN PASSWORD 'auth' NOINHERIT;
GRANT web_anon, tenant_b TO authenticator;
CREATE ROLE pqn_reader LOGIN PASSWORD 'reader' NOINHERIT;

CREATE SCHEMA data;
CREATE SCHEMA api;
CREATE TABLE data.orders (
  id bigint PRIMARY KEY,
  customer_id int NOT NULL,
  created_at timestamp NOT NULL,
  status text NOT NULL,
  total numeric(12,2) NOT NULL
);
INSERT INTO data.orders
SELECT g, (g % 5000) + 1, TIMESTAMP '2023-01-01' + (g * interval '37 seconds'),
       CASE WHEN g % 3 = 0 THEN 'paid' ELSE 'open' END, ((g % 1000) / 10.0)
FROM generate_series(1, 1000000) g;
ANALYZE data.orders;

CREATE VIEW api.orders_daily WITH (security_invoker = true) AS
  SELECT id, customer_id, date_trunc('day', created_at) AS day, status, total FROM data.orders;

GRANT USAGE ON SCHEMA data, api TO web_anon, tenant_b, pqn_reader;
GRANT SELECT ON data.orders TO web_anon, tenant_b, pqn_reader;
GRANT SELECT ON api.orders_daily TO web_anon, tenant_b, pqn_reader;
-- Exposed, but granted only to tenant_b: anonymous reads must be denied.
CREATE VIEW api.orders_staff WITH (security_invoker = true) AS SELECT id, total FROM data.orders;
GRANT SELECT ON api.orders_staff TO tenant_b;

ALTER TABLE data.orders ENABLE ROW LEVEL SECURITY;
CREATE POLICY open_read ON data.orders FOR SELECT TO web_anon, pqn_reader USING (true);
CREATE POLICY tenant_b_scope ON data.orders FOR SELECT TO tenant_b USING (customer_id <= 100);
`

const pgrstEndpoint = "/orders_daily?day=eq.2023-03-01T00:00:00&select=id,total&order=id&limit=3"

type httpResult struct {
	status  int
	body    string
	header  http.Header
	elapsed time.Duration
}

func pgrstGET(t *testing.T, url, bearer string) httpResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	start := time.Now()
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return httpResult{status: resp.StatusCode, body: string(b), header: resp.Header, elapsed: time.Since(start)}
}

// pgrstTenantToken is an HS256 JWT whose role claim selects PostgREST's tenant_b role.
func pgrstTenantToken(t *testing.T, role string) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	hdr, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	claims, _ := json.Marshal(map[string]string{"role": role})
	signing := enc(hdr) + "." + enc(claims)
	mac := hmac.New(sha256.New, []byte(pgrstJWTSecret))
	mac.Write([]byte(signing))
	return signing + "." + enc(mac.Sum(nil))
}

// TestPostgRESTInvestigation runs a real PostgREST v16.4 endpoint with an
// inefficient access pattern, opens an investigation from its captured log
// evidence, applies the product-proposed DDL in this disposable database only,
// and checks the endpoint, the measurements, and the report.
func TestPostgRESTInvestigation(t *testing.T) {
	ctx := context.Background()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstSetupSQL, pgrstJWTSecret)

	adminPool, err := pgxpool.New(ctx, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)
	readerPool, err := pgxpool.New(ctx, stack.ReaderConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readerPool.Close)

	appPool, err := helpers.AppPoolFromAdmin(ctx, adminPool, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)
	// Both runtime pools enforce their actual role privileges and RLS.
	validator := queryrunner.NewValidator([]string{"data", "api"}, 10000)
	runner := queryrunner.NewRunner(readerPool, validator, 5000, 30*time.Second, queryrunner.WithExplainAnalyze(true))
	appDB := db.NewOrgScoped(appPool)
	queriesSvc := service.NewQueriesService(appPool, appDB, runner, config.MetricsConfig{})
	reportsSvc := service.NewReportsService(appPool, appDB, runner, noopLLM{}, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "pgrst-integration", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	logLen := func() int {
		logs, err := stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatalf("read PostgreSQL logs: %v", err)
		}
		return len(logs)
	}
	// excerptSince waits until the log written after offset holds one complete
	// PostgREST read request, then returns that excerpt.
	excerptSince := func(offset int) string {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for {
			logs, err := stack.PostgreSQLLogs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			part := logs[offset:]
			if _, perr := queryrunner.ParsePostgRESTLog(part); perr == nil {
				return part
			}
			if time.Now().After(deadline) {
				tail := logs[offset:]
				if len(tail) > 6000 {
					tail = tail[len(tail)-6000:]
				}
				t.Fatalf("no complete PostgREST request in the PostgreSQL log after %d bytes; tail:\n%s", offset, tail)
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	countInvestigations := func() int {
		var n int
		if err := adminPool.QueryRow(ctx, `SELECT count(*) FROM app.investigations`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// --- 1. Real endpoint, captured evidence, investigation ----------------
	offset := logLen()
	page1 := pgrstGET(t, stack.BaseURL+pgrstEndpoint, "")
	if page1.status != http.StatusOK {
		t.Fatalf("endpoint status %d: %s", page1.status, page1.body)
	}
	pageTiming := page1.header.Get("Server-Timing")
	if !strings.Contains(pageTiming, "transaction;dur=") {
		t.Fatalf("PostgREST did not send Server-Timing transaction stage: %q", pageTiming)
	}
	// Capture before the next request: one excerpt must hold exactly one read.
	excerpt := excerptSince(offset)
	page2 := pgrstGET(t, stack.BaseURL+pgrstEndpoint+"&offset=3", "")

	httpMs := float64(page1.elapsed.Microseconds()) / 1000
	inv, err := invSvc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{
		PostgresLog:    excerpt,
		ServerTiming:   &pageTiming,
		HTTPDurationMs: &httpMs,
		Analyze:        true,
	})
	if err != nil {
		t.Fatalf("CreateFromPostgrest: %v", err)
	}

	// Exact source SQL, bind values and provenance come from the log.
	if !strings.HasPrefix(inv.SQL, "WITH pgrst_source AS") || !strings.Contains(inv.SQL, `"api"."orders_daily"."day" = $1`) {
		t.Fatalf("investigation SQL is not the exact PostgREST statement: %s", inv.SQL)
	}
	if got, want := strings.Join(inv.Binds, "|"), "2023-03-01 00:00:00|3|0"; got != want {
		t.Fatalf("binds = %q, want %q", got, want)
	}
	pr := inv.PostgrestRequest
	if pr == nil || pr.Method != "GET" || pr.Endpoint != "/orders_daily" || pr.Role != "web_anon" {
		t.Fatalf("postgrest provenance = %+v", pr)
	}
	if pr.ServerTimingTransactionMs == nil || *pr.ServerTimingTransactionMs <= 0 ||
		pr.StatementDurationMs == nil || *pr.StatementDurationMs <= 0 ||
		pr.HTTPDurationMs == nil || *pr.HTTPDurationMs <= 0 {
		t.Fatalf("timings must all be present and separate: %+v", pr)
	}
	if pr.QueryID == nil || *pr.QueryID == "" {
		t.Fatal("the log prefix carries a query identifier (%Q); it must be recorded")
	}
	if len(pr.ReplayLimitations) == 0 {
		t.Fatal("replay limitations must be recorded")
	}
	// Plan evidence is an EXPLAIN ANALYZE of the literal bind substitution.
	if inv.Explain == nil || inv.Explain.TotalCost <= 0 || inv.Explain.EvidenceMode != "observed" {
		t.Fatalf("expected observed plan evidence, got %+v", inv.Explain)
	}

	// Credentials and session context never persist.
	stored, err := json.Marshal(inv)
	if err != nil {
		t.Fatal(err)
	}
	// Limitation text may name request.jwt.claims; credential values must never appear.
	for _, leak := range []string{"Bearer ", "authorization", "eyJ", "cookie"} {
		if strings.Contains(strings.ToLower(string(stored)), strings.ToLower(leak)) {
			t.Fatalf("persisted investigation contains %q", leak)
		}
	}

	// --- 2. Rewrites decline the wrapper, and an index is proposed ---------
	sugg, err := invSvc.SuggestRewrite(reqCtx, &investigations.SuggestRewritePayload{ID: inv.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(sugg.Candidates) != 0 || sugg.DeclineReason == nil || !strings.Contains(*sugg.DeclineReason, "generated response wrapper") {
		t.Fatalf("wrapper must decline rewrites with a reason, got %+v", sugg)
	}

	ranked, err := invSvc.RankCandidates(reqCtx, &investigations.RankCandidatesPayload{ID: inv.ID})
	if err != nil {
		t.Fatalf("RankCandidates: %v", err)
	}
	var indexDDL, indexRationale string
	for _, c := range ranked.Candidates {
		ddl := ""
		if c.Ddl != nil {
			ddl = *c.Ddl
		}
		t.Logf("candidate kind=%s rankable=%v ddl=%q rationale=%s", c.Kind, c.Rankable, ddl, c.Rationale)
		if c.Kind == queryrunner.CandidateKindIndexDDL && ddl != "" && indexDDL == "" {
			indexDDL, indexRationale = ddl, c.Rationale
		}
	}
	if indexDDL == "" {
		t.Fatal("expected an index DDL recommendation from the captured plan")
	}
	// The bare-column index assumes a predicate rewrite the endpoint cannot make
	// (measured below: it does not lower the cost). The advice must say so.
	if !strings.Contains(indexRationale, "As captured, this endpoint cannot use it") {
		t.Fatalf("function-wrapped index advice must disclose that the endpoint cannot use it as captured: %s", indexRationale)
	}

	// --- 3. Apply the proposed DDL in this disposable database only --------
	// Row-level security changes the plan: a non-leakproof predicate such as
	// date_trunc cannot drive an index scan under a security barrier, even with a
	// policy that is USING (true). So each plan is measured as the role that runs
	// it: the product's replay role, the endpoint's PostgREST role, and the table
	// owner (which bypasses RLS).
	literalSQL, err := queryrunner.SubstituteCapturedParams(inv.SQL, inv.Binds)
	if err != nil {
		t.Fatal(err)
	}
	beforeReplay := inv.Explain.TotalCost
	beforeEndpointRole := planCostAsRole(t, ctx, adminPool, "web_anon", literalSQL)
	if d := beforeReplay - beforeEndpointRole; d > 0.01 || d < -0.01 {
		t.Fatalf("replay cost %.2f differs from the endpoint role's plan %.2f under the same RLS policy", beforeReplay, beforeEndpointRole)
	}

	if _, err := adminPool.Exec(ctx, indexDDL); err != nil {
		t.Fatalf("apply proposed DDL %q: %v", indexDDL, err)
	}
	if _, err := adminPool.Exec(ctx, `ANALYZE data.orders`); err != nil {
		t.Fatal(err)
	}
	// The product's DDL: measured, not assumed. Its advice assumes the predicate is
	// rewritten to a range on created_at, which this statement does not allow, and
	// it ignores ORDER BY id LIMIT 3.
	afterReplay := planCost(t, ctx, readerPool, literalSQL)
	afterEndpointRole := planCostAsRole(t, ctx, adminPool, "web_anon", literalSQL)
	t.Logf("product-proposed DDL, total_cost: replay(pqn_reader) %.2f -> %.2f; endpoint(web_anon) %.2f -> %.2f",
		beforeReplay, afterReplay, beforeEndpointRole, afterEndpointRole)
	if afterReplay < beforeReplay || afterEndpointRole < beforeEndpointRole {
		t.Logf("NOTE: the proposed index now lowers the estimate; update the advisor-gap note before trusting this assertion")
	}

	// Manual control, not a product candidate: an index that leads with the
	// predicate expression and continues with the ORDER BY key. It replaces the
	// proposed index so the difference is attributable to this one.
	if _, err := adminPool.Exec(ctx, `DROP INDEX data.`+proposedIndexName(t, ctx, adminPool)); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `CREATE INDEX orders_day_id_control ON data.orders ((date_trunc('day'::text, created_at)), id)`); err != nil {
		t.Fatal(err)
	}
	if _, err := adminPool.Exec(ctx, `ANALYZE data.orders`); err != nil {
		t.Fatal(err)
	}
	controlEndpointRole := planCostAsRole(t, ctx, adminPool, "web_anon", literalSQL)
	controlOwner := planCostAsRole(t, ctx, adminPool, "", literalSQL)
	t.Logf("manual control (not product-generated), total_cost: endpoint(web_anon) %.2f; owner, RLS bypassed %.2f (before %.2f)",
		controlEndpointRole, controlOwner, beforeEndpointRole)
	if controlOwner >= beforeEndpointRole {
		t.Fatalf("control index did not lower the owner-role plan: before %.2f, control %.2f", beforeEndpointRole, controlOwner)
	}
	if controlEndpointRole < beforeEndpointRole*0.99 {
		t.Fatalf("control index lowered the RLS-constrained plan (%.2f -> %.2f); the RLS limitation note is stale", beforeEndpointRole, controlEndpointRole)
	}

	// Endpoint results after the index changes: same bodies, ordering and pagination.
	after1 := pgrstGET(t, stack.BaseURL+pgrstEndpoint, "")
	after2 := pgrstGET(t, stack.BaseURL+pgrstEndpoint+"&offset=3", "")
	if after1.body != page1.body || after2.body != page2.body {
		t.Fatalf("endpoint body changed after the index change:\nbefore %s\nafter  %s", page1.body, after1.body)
	}
	if after1.header.Get("Content-Range") != page1.header.Get("Content-Range") {
		t.Fatalf("Content-Range changed: %q vs %q", page1.header.Get("Content-Range"), after1.header.Get("Content-Range"))
	}
	assertAscendingIDs(t, page1.body, page2.body)

	// --- 4. Report cites the evidence, and says what is not claimed --------
	report, err := invSvc.GenerateReport(reqCtx, &investigations.GenerateReportPayload{ID: inv.ID})
	if err != nil {
		t.Fatalf("GenerateReport: %v", err)
	}
	if report.ReportID == nil {
		t.Fatal("report not linked to investigation")
	}
	var evidenceJSON []byte
	if err := adminPool.QueryRow(ctx, `
		SELECT metrics->'investigation'->'provenance'->'postgrest' FROM app.reports WHERE id = $1
	`, *report.ReportID).Scan(&evidenceJSON); err != nil {
		t.Fatal(err)
	}
	var ev story.PostgRESTEvidence
	if err := json.Unmarshal(evidenceJSON, &ev); err != nil {
		t.Fatalf("report postgrest evidence: %v (%s)", err, evidenceJSON)
	}
	if ev.EndpointEquivalence != "not_claimed" || ev.Endpoint != "/orders_daily" {
		t.Fatalf("report evidence = %+v", ev)
	}
	if !containsPrefix(ev.ApplyAt, "Underlying relation api.orders_daily") {
		t.Fatalf("apply_at must name the underlying view, got %q", ev.ApplyAt)
	}

	// --- 5. Role / RLS: a tenant_b request differs from the connection role ---
	tenantToken := pgrstTenantToken(t, "tenant_b")
	offset = logLen()
	tenantResp := pgrstGET(t, stack.BaseURL+"/orders_daily?day=eq.2023-03-01T00:00:00&select=id,total&order=id", tenantToken)
	if tenantResp.status != http.StatusOK {
		t.Fatalf("tenant_b endpoint status %d: %s", tenantResp.status, tenantResp.body)
	}
	tenantInv, err := invSvc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{
		PostgresLog: excerptSince(offset),
	})
	if err != nil {
		t.Fatalf("CreateFromPostgrest tenant_b: %v", err)
	}
	if tenantInv.PostgrestRequest == nil || tenantInv.PostgrestRequest.Role != "tenant_b" {
		t.Fatalf("tenant_b provenance = %+v", tenantInv.PostgrestRequest)
	}
	if !containsPrefix(tenantInv.PostgrestRequest.ReplayLimitations, "Replay uses the connection's database role; captured PostgREST role \"tenant_b\"") {
		t.Fatalf("role mismatch not labeled: %q", tenantInv.PostgrestRequest.ReplayLimitations)
	}
	tenantSQL, err := queryrunner.SubstituteCapturedParams(tenantInv.SQL, tenantInv.Binds)
	if err != nil {
		t.Fatal(err)
	}
	endpointRows := jsonArrayLen(t, tenantResp.body)
	asTenant := countRowsAs(t, ctx, adminPool, "tenant_b", tenantSQL)
	asReader := countRowsAs(t, ctx, adminPool, "pqn_reader", tenantSQL)
	if asTenant != endpointRows {
		t.Fatalf("SET ROLE tenant_b rows %d != endpoint rows %d", asTenant, endpointRows)
	}
	if asReader <= asTenant {
		t.Fatalf("connection-role replay should see more rows than tenant_b (reader %d, tenant_b %d); the RLS limitation would not be real", asReader, asTenant)
	}
	t.Logf("RLS boundary: endpoint as tenant_b = %d rows; replay as connection role = %d rows", endpointRows, asReader)

	// --- 6. Fail-closed intake ----------------------------------------------
	before := countInvestigations()
	// Drop the last bind from the request's own execute line (the last DETAIL).
	cut := strings.LastIndex(excerpt, ", $3 = '0'")
	missingBind := excerpt[:cut] + excerpt[cut+len(", $3 = '0'"):]
	if _, err := invSvc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: missingBind}); !isValidationError(err) {
		t.Fatalf("missing bind must be a validation error, got %v", err)
	}

	// The validator reads statement text, not view bodies, so a function hidden in
	// a view is outside its reach (the analytical role's privileges bound that).
	// Here the captured statement itself calls pg_sleep, and must be refused.
	sleepy := strings.ReplaceAll(excerpt, `SELECT "api"."orders_daily"."id",`, `SELECT pg_sleep(0)::text, "api"."orders_daily"."id",`)
	if sleepy == excerpt {
		t.Fatal("test fixture did not inject pg_sleep")
	}
	if _, err := invSvc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: sleepy}); !isValidationError(err) {
		t.Fatalf("statement calling pg_sleep must be refused by the validator, got %v", err)
	}
	if after := countInvestigations(); after != before {
		t.Fatalf("refused intake created investigations: %d -> %d", before, after)
	}

	anon := pgrstGET(t, stack.BaseURL+"/orders_staff?select=id&limit=1", "")
	if anon.status != http.StatusUnauthorized && anon.status != http.StatusForbidden {
		t.Fatalf("anon read of ungranted table should be denied, got %d", anon.status)
	}

	// --- 7. Tenant isolation and the non-PostgREST path --------------------
	other := auth.WithPrincipal(ctx, auth.Principal{UserID: "other-org", OrgID: "11111111-2222-3333-4444-555555555555", Role: auth.RoleAdmin})
	if _, err := invSvc.Get(other, &investigations.GetPayload{ID: inv.ID}); err == nil {
		t.Fatal("another organization must not read the investigation")
	}

	manual, err := invSvc.Create(reqCtx, &investigations.CreateInvestigationPayload{
		Title: "manual path",
		SQL:   `SELECT count(*) FROM data.orders WHERE id < 100`,
	})
	if err != nil {
		t.Fatalf("manual Create: %v", err)
	}
	if manual.PostgrestRequest != nil || len(manual.Binds) != 0 {
		t.Fatal("manual investigations must carry no PostgREST provenance")
	}
}

func assertAscendingIDs(t *testing.T, bodies ...string) {
	t.Helper()
	last := int64(0)
	for _, body := range bodies {
		var rows []struct {
			ID int64 `json:"id"`
		}
		if err := json.Unmarshal([]byte(body), &rows); err != nil {
			t.Fatalf("decode page: %v", err)
		}
		for _, r := range rows {
			if r.ID <= last {
				t.Fatalf("ids not strictly ascending across pages: %d after %d", r.ID, last)
			}
			last = r.ID
		}
	}
}

func jsonArrayLen(t *testing.T, body string) int {
	t.Helper()
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("decode endpoint body: %v", err)
	}
	return len(rows)
}

// countRowsAs runs sql with the role switched, as the row set that role sees.
func countRowsAs(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role, sql string) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT json_array_length(q.body) FROM (`+sql+`) q`).Scan(&n); err != nil {
		t.Fatalf("count as %s: %v", role, err)
	}
	return n
}

// proposedIndexName finds the index the product's DDL created on data.orders.
func proposedIndexName(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	var name string
	if err := pool.QueryRow(ctx, `
		SELECT indexname FROM pg_indexes
		WHERE schemaname = 'data' AND tablename = 'orders' AND indexdef LIKE '%(created_at)%'
		ORDER BY indexname LIMIT 1
	`).Scan(&name); err != nil {
		t.Fatalf("find proposed index: %v", err)
	}
	return name
}

// planCost returns the planner's total cost for sql on pool.
func planCost(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string) float64 {
	t.Helper()
	var raw []byte
	if err := pool.QueryRow(ctx, `EXPLAIN (FORMAT JSON) `+sql).Scan(&raw); err != nil {
		t.Fatalf("explain: %v", err)
	}
	return totalCostFromJSON(t, raw)
}

// planCostAsRole returns the planner's total cost for sql run as role; an empty role runs as the pool's user.
func planCostAsRole(t *testing.T, ctx context.Context, pool *pgxpool.Pool, role, sql string) float64 {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if role != "" {
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
			t.Fatal(err)
		}
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `EXPLAIN (FORMAT JSON) `+sql).Scan(&raw); err != nil {
		t.Fatalf("explain as %q: %v", role, err)
	}
	return totalCostFromJSON(t, raw)
}

func totalCostFromJSON(t *testing.T, raw []byte) float64 {
	t.Helper()
	var plans []struct {
		Plan struct {
			TotalCost float64 `json:"Total Cost"`
		} `json:"Plan"`
	}
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode plan: %v (%s)", err, raw)
	}
	return plans[0].Plan.TotalCost
}

func containsPrefix(items []string, prefix string) bool {
	for _, s := range items {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
}

func isValidationError(err error) bool {
	var ve *investigations.ValidationError
	return err != nil && errors.As(err, &ve)
}
