package integration

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
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
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

const pgrstShapesSetup = `
CREATE ROLE web_anon NOLOGIN;
CREATE ROLE authenticator LOGIN PASSWORD 'auth' NOINHERIT;
GRANT web_anon TO authenticator;
CREATE ROLE pqn_reader LOGIN PASSWORD 'reader' NOINHERIT;
CREATE SCHEMA api;
CREATE TABLE api.customers(id int PRIMARY KEY, name text NOT NULL);
CREATE TABLE api.orders(id bigint PRIMARY KEY, customer_id int REFERENCES api.customers(id), status text,
  total numeric(12,2), created_at timestamptz NOT NULL, note text, doc jsonb, fts tsvector);
INSERT INTO api.customers SELECT g, 'cust-'||g FROM generate_series(1,50) g;
INSERT INTO api.orders SELECT g, (g%50)+1, CASE WHEN g%3=0 THEN 'paid' ELSE 'open' END, (g%1000)/10.0,
  TIMESTAMPTZ '2023-03-01 00:00:00+00' + g*interval '7 minutes',
  CASE WHEN g=7 THEN E'O''Reilly \\ back' WHEN g=8 THEN 'Loading...' END,
  jsonb_build_object('name','n'||(g%10),'n',g),
  to_tsvector('english', CASE WHEN g%5=0 THEN 'the cat sat' ELSE 'a dog ran' END)
FROM generate_series(1,5000) g;
CREATE FUNCTION api.label(api.orders) RETURNS text LANGUAGE sql STABLE AS $$ SELECT $1.status || '#' || $1.id $$;
CREATE FUNCTION api.big_orders(min_total numeric) RETURNS SETOF api.orders LANGUAGE sql STABLE AS $$ SELECT * FROM api.orders WHERE total >= min_total $$;
GRANT USAGE ON SCHEMA api TO web_anon, pqn_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO web_anon, pqn_reader;
ANALYZE;
`

// TestPostgRESTRequestShapes sends real requests of each common shape to
// PostgREST and checks that the imported statement, replayed with its captured
// binds, returns exactly the body the endpoint served. Shapes the importer does
// not support must be refused with a reason, never imported approximately.
func TestPostgRESTRequestShapes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstShapesSetup, pgrstJWTSecret)
	admin, err := pgxpool.New(ctx, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	// Production pins the analytical role's search_path to the allowed schemas
	// (internal/db AfterConnect); computed columns resolve through it.
	reader, err := pgxpool.New(ctx, stack.ReaderConnStr+"&search_path=api")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	app, err := helpers.AppPoolFromAdmin(ctx, admin, stack.ConnStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	runner := queryrunner.NewRunner(reader, queryrunner.NewValidator([]string{"api"}, 10000), 5000, 30*time.Second, queryrunner.WithExplainAnalyze(true))
	appDB := db.NewOrgScoped(app)
	qs := service.NewQueriesService(app, appDB, runner, config.MetricsConfig{})
	rs := service.NewReportsService(app, appDB, runner, noopLLM{}, config.MetricsConfig{})
	svc := service.NewInvestigationsService(appDB, qs, rs)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "pgrst-shapes", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	const (
		identical = iota // imports; replay body == endpoint body
		refused          // not imported; error names the reason
	)
	q := url.QueryEscape
	ids := make([]string, 1500)
	for i := range ids {
		ids[i] = strconv.Itoa(i + 1)
	}
	for _, tc := range []struct {
		name, method, path string
		header             map[string]string
		want               int
		reason             string
		logStatementAll    bool
	}{
		{name: "filter, order, limit", path: "/orders?status=eq.paid&select=id,total&order=id&limit=3"},
		{name: "count=exact", path: "/orders?status=eq.paid&select=id&order=id&limit=3", header: map[string]string{"Prefer": "count=exact"}},
		{name: "count=planned", path: "/orders?status=eq.paid&select=id&order=id&limit=3", header: map[string]string{"Prefer": "count=planned"}},
		{name: "count=estimated", path: "/orders?status=eq.paid&select=id&order=id&limit=3", header: map[string]string{"Prefer": "count=estimated"}},
		{name: "singular object", path: "/orders?id=eq.5&select=id,status", header: map[string]string{"Accept": "application/vnd.pgrst.object+json"}},
		{name: "csv (multi-line statement)", path: "/orders?select=id,status&order=id&limit=3", header: map[string]string{"Accept": "text/csv"}},
		{name: "in list", path: "/orders?id=in.(1,2,3)&select=id,status&order=id"},
		{name: "or", path: "/orders?or=(id.eq.1,id.eq.2)&select=id&order=id"},
		{name: "like", path: "/orders?status=like.pa*&select=id&order=id&limit=2"},
		{name: "embedded resource", path: "/customers?select=id,name,orders(id,total)&id=eq.1&orders.order=id&orders.limit=2"},
		{name: "json path", path: "/orders?select=id,doc->>name&doc->>name=eq.n3&order=id&limit=3"},
		{name: "jsonb contains", path: "/orders?doc=cs." + q(`{"n":5}`) + "&select=id"},
		{name: "full-text search", path: "/orders?fts=fts.cat&select=id&order=id&limit=3"},
		{name: "range header", path: "/orders?select=id&order=id", header: map[string]string{"Range-Unit": "items", "Range": "0-2"}},
		{name: "quote and backslash", path: "/orders?note=eq." + q(`O'Reilly \ back`) + "&select=id"},
		{name: "is null, nulls last", path: "/orders?note=not.is.null&select=id,note&order=note.desc.nullslast,id"},
		{name: "cast in select, offset", path: "/orders?select=id,total::text&order=id&limit=2&offset=4990"},
		{name: "computed column", path: "/orders?select=id,label&order=id&limit=2"},
		{name: "in list of 1500 ids", path: "/orders?id=in.(" + strings.Join(ids, ",") + ")&select=id&order=id&limit=2"},
		{name: "HEAD count", method: http.MethodHead, path: "/orders?status=eq.paid", header: map[string]string{"Prefer": "count=exact"}},
		{name: "time zone preference", path: "/orders?created_at=lt.2023-03-01T00:30:00&select=id,created_at&order=id", header: map[string]string{"Prefer": "timezone=America/New_York"}},
		{name: "rpc", path: "/rpc/big_orders?min_total=99&select=id&limit=2"},
		// Headers logged in the same excerpt are longer, proving no truncation.
		{name: "value ending in ...", path: "/orders?note=eq.Loading...&select=id"},
		{name: "unprovable value ending in ...", path: "/orders?note=eq." + strings.Repeat("x", 400) + "...&select=id", want: refused, reason: "ends in '...'"},
		{name: "embedded resource in csv", path: "/customers?select=id,orders(id)&id=eq.2&orders.limit=1", header: map[string]string{"Accept": "text/csv"}},
		// Last: switches the server to log_statement=all for the rest of the test.
		{name: "log_statement=all", path: "/orders?status=eq.paid&select=id&order=id&limit=2", logStatementAll: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.logStatementAll {
				for _, stmt := range []string{`ALTER SYSTEM SET log_statement = 'all'`, `SELECT pg_reload_conf()`} {
					if _, err := admin.Exec(ctx, stmt); err != nil {
						t.Fatal(err)
					}
				}
				time.Sleep(300 * time.Millisecond)
			}
			method := tc.method
			if method == "" {
				method = http.MethodGet
			}
			before, err := stack.PostgreSQLLogs(ctx)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequestWithContext(ctx, method, stack.BaseURL+tc.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			for k, v := range tc.header {
				req.Header.Set(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || resp.StatusCode >= 300 {
				t.Fatalf("endpoint HTTP %d: %s %v", resp.StatusCode, raw, err)
			}
			body := string(raw)
			// Every PostgREST read transaction ends in COMMIT.
			var excerpt string
			for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
				logs, err := stack.PostgreSQLLogs(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if excerpt = logs[len(before):]; strings.Contains(excerpt, "COMMIT") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("request transaction not logged:\n%s", excerpt)
				}
			}

			inv, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
			if tc.want == refused {
				var ve *investigations.ValidationError
				if !errors.As(err, &ve) || !strings.Contains(ve.Message, tc.reason) {
					t.Fatalf("want refusal containing %q, got %#v", tc.reason, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("import: %v", err)
			}
			if inv.Explain == nil || inv.Explain.EvidenceMode != "observed" {
				t.Fatalf("no observed plan evidence: %+v", inv.Explain)
			}
			if method == http.MethodGet {
				literal, err := queryrunner.SubstituteCapturedParams(inv.SQL, inv.Binds)
				if err != nil {
					t.Fatal(err)
				}
				// Replay through the runner in the context stored with the
				// investigation, as every product replay does.
				pr := inv.PostgrestRequest
				rc := &queryrunner.ReplayContext{Settings: pr.RequestSettings, Method: pr.Method, Path: pr.Endpoint}
				if pr.ReplaySearchPath != nil {
					rc.SearchPath = queryrunner.ParseSearchPath(*pr.ReplaySearchPath)
				}
				res, err := runner.Run(queryrunner.WithReplayContext(ctx, rc), `SELECT body::text AS body FROM (`+literal+`) q`, 1)
				if err != nil || len(res.Rows) != 1 {
					t.Fatalf("replay: %v", err)
				}
				if replay, _ := res.Rows[0][0].(string); replay != body {
					t.Fatalf("replay body differs from endpoint body\nendpoint: %s\nreplay:   %s", body, replay)
				}
			}
			cmp, err := svc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL, VerifyResults: true})
			if err != nil {
				t.Fatalf("self-compare: %v", err)
			}
			if s := cmp.Comparison.ResultEquivalenceStatus; s == nil || *s != "VerifiedEqual" {
				t.Fatalf("self-compare status = %v", s)
			}
		})
	}
}
