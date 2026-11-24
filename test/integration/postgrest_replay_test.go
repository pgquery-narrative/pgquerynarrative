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

const pgrstReplaySetup = `
CREATE ROLE web_anon NOLOGIN;
CREATE ROLE authenticated NOLOGIN;
CREATE ROLE slow_role NOLOGIN;
ALTER ROLE slow_role SET statement_timeout = '5ms';
CREATE ROLE authenticator LOGIN PASSWORD 'auth' NOINHERIT;
GRANT web_anon, authenticated, slow_role TO authenticator;
CREATE ROLE pqn_reader LOGIN PASSWORD 'reader' NOINHERIT;
CREATE SCHEMA api;
CREATE SCHEMA extensions;
CREATE EXTENSION citext SCHEMA extensions;
ALTER ROLE authenticator SET pgrst.db_extra_search_path = 'public, extensions';

CREATE TABLE api.notes(id int PRIMARY KEY, owner text NOT NULL, body text NOT NULL);
INSERT INTO api.notes SELECT g, CASE WHEN g % 3 = 0 THEN 'alice' ELSE 'bob' END, 'note ' || g FROM generate_series(1, 30) g;
ALTER TABLE api.notes ENABLE ROW LEVEL SECURITY;
CREATE POLICY own_notes ON api.notes FOR SELECT TO authenticated USING (owner = current_setting('request.jwt.claims', true)::json->>'sub');
CREATE POLICY reader_all ON api.notes FOR SELECT TO pqn_reader USING (true);

CREATE TABLE api.users(id int PRIMARY KEY, email extensions.citext NOT NULL);
INSERT INTO api.users VALUES (1, 'alice@example.com'), (2, 'bob@example.com');

CREATE TABLE api.big(id int PRIMARY KEY, v text NOT NULL);
INSERT INTO api.big SELECT g, md5(g::text) FROM generate_series(1, 400000) g;

CREATE TABLE api.events(id int PRIMARY KEY, kind text NOT NULL);
INSERT INTO api.events SELECT g, CASE WHEN g % 1000 = 0 THEN 'rare' ELSE 'common' END FROM generate_series(1, 200000) g;
CREATE INDEX events_kind ON api.events(kind);

GRANT USAGE ON SCHEMA api, extensions TO web_anon, authenticated, slow_role, pqn_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO web_anon, authenticated, slow_role, pqn_reader;
ANALYZE;
`

const pgrstTestDataKey = "integration-data-encryption-key-0123456789"

// newPostgRESTService builds the product services the way production wires
// them: a restricted analytical role whose search_path is pinned to its
// allowed schemas, and a query length limit.
func newPostgRESTService(t *testing.T, ctx context.Context, app *pgxpool.Pool, readerConnStr string, schemas []string, maxQueryLength int) *service.InvestigationsService {
	t.Helper()
	reader, err := pgxpool.New(ctx, readerConnStr+"&search_path="+strings.Join(schemas, ","))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	runner := queryrunner.NewRunner(reader, queryrunner.NewValidator(schemas, maxQueryLength), 5000, 30*time.Second, queryrunner.WithExplainAnalyze(true))
	appDB := db.NewOrgScoped(app)
	qs := service.NewQueriesService(app, appDB, runner, config.MetricsConfig{})
	qs.SetDataEncryptionKey([]byte(pgrstTestDataKey))
	rs := service.NewReportsService(app, appDB, runner, noopLLM{}, config.MetricsConfig{})
	return service.NewInvestigationsService(appDB, qs, rs)
}

func pgrstJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := base64.RawURLEncoding.EncodeToString
	hdr, _ := json.Marshal(map[string]string{"alg": "HS256", "typ": "JWT"})
	body, _ := json.Marshal(claims)
	signing := enc(hdr) + "." + enc(body)
	mac := hmac.New(sha256.New, []byte(pgrstJWTSecret))
	mac.Write([]byte(signing))
	return signing + "." + enc(mac.Sum(nil))
}

// pgrstCall sends one request and returns the PostgreSQL log written for it,
// once done(log) reports the request's records are complete.
func pgrstCall(t *testing.T, ctx context.Context, stack *helpers.PostgRESTStack, path, token string, done func(string) bool) (int, string, string) {
	t.Helper()
	before, err := stack.PostgreSQLLogs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, stack.BaseURL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		logs, err := stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if excerpt := logs[len(before):]; done(excerpt) {
			return resp.StatusCode, string(body), excerpt
		}
		if time.Now().After(deadline) {
			t.Fatalf("request %s not fully logged", path)
		}
	}
}

func endsTransaction(log string) bool {
	return strings.Contains(log, "COMMIT") || strings.Contains(log, "ABORT")
}

// scanActualRows returns the actual rows of the first plan node reading relation.
func scanActualRows(plan any, relation string) (float64, bool) {
	var found *float64
	var walk func(any)
	walk = func(node any) {
		m, ok := node.(map[string]any)
		if !ok || found != nil {
			return
		}
		if m["Relation Name"] == relation {
			if v, ok := m["Actual Rows"].(float64); ok {
				found = &v
				return
			}
		}
		for _, k := range []string{"Plan", "Plans"} {
			switch c := m[k].(type) {
			case map[string]any:
				walk(c)
			case []any:
				for _, x := range c {
					walk(x)
				}
			}
		}
	}
	if list, ok := plan.([]any); ok {
		for _, x := range list {
			walk(x)
		}
	}
	if found == nil {
		return 0, false
	}
	return *found, true
}

func validationMessage(err error) string {
	var ve *investigations.ValidationError
	if errors.As(err, &ve) {
		return ve.Message
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// actuallyGenericAfterPriorBinds prepares sql on a fresh connection, executes
// it five times with priorArgs — standing in for a connection's normal prior
// traffic — then returns whether the SIXTH execution, using capturedArgs (the
// value under investigation), actually runs with a generic plan.
// PostgreSQL's generic-vs-custom decision is made from the average cost of
// the first five executions' own custom plans alone, before it ever looks at
// the sixth call's parameters, so a cheap sixth value can still inherit the
// generic plan if the first five were expensive enough to make the generic
// plan look cheap by comparison (see modelPlanCache's doc comment in
// investigations_postgrest.go for why this means the production verdict
// cannot be judged from one capture's own cost).
func actuallyGenericAfterPriorBinds(t *testing.T, ctx context.Context, admin *pgxpool.Pool, sql string, priorArgs, capturedArgs []string) bool {
	t.Helper()
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	quote := func(args []string) string {
		q := make([]string, len(args))
		for i, a := range args {
			q[i] = "'" + strings.ReplaceAll(a, "'", "''") + "'"
		}
		return strings.Join(q, ", ")
	}
	if _, err := conn.Exec(ctx, "PREPARE pqn_skew AS "+sql); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.Exec(ctx, "DEALLOCATE pqn_skew") }()
	for i := 0; i < 5; i++ {
		if _, err := conn.Exec(ctx, "EXECUTE pqn_skew("+quote(priorArgs)+")"); err != nil {
			t.Fatal(err)
		}
	}
	var plan string
	rows, err := conn.Query(ctx, "EXPLAIN (VERBOSE) EXECUTE pqn_skew("+quote(capturedArgs)+")")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var line string
		_ = rows.Scan(&line)
		plan += line + "\n"
	}
	return strings.Contains(plan, "$1")
}

// TestPostgRESTReplayContext verifies, against a real PostgREST and PostgreSQL,
// that replays run in the captured request's context as far as policy allows,
// and that what they cannot reproduce is stated.
func TestPostgRESTReplayContext(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstReplaySetup, pgrstJWTSecret)
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
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "pgrst-replay", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})
	apiOnly := newPostgRESTService(t, ctx, app, stack.ReaderConnStr, []string{"api"}, 10000)
	withExtensions := newPostgRESTService(t, ctx, app, stack.ReaderConnStr, []string{"api", "extensions"}, 50000)

	t.Run("role replay under RLS, claims never replayed", func(t *testing.T) {
		alice := pgrstJWT(t, map[string]any{"role": "authenticated", "sub": "alice"})
		status, body, excerpt := pgrstCall(t, ctx, stack, "/notes?select=id,owner&order=id", alice, endsTransaction)
		if status != 200 || jsonArrayLen(t, body) != 10 {
			t.Fatalf("endpoint as alice: %d %s", status, body)
		}

		// Not allowlisted: the connection's role, which RLS lets see every row.
		plain, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
		if err != nil {
			t.Fatal(err)
		}
		if rows, ok := scanActualRows(plain.Explain.Plan, "notes"); !ok || rows != 30 || plain.PostgrestRequest.ReplayRole != nil {
			t.Fatalf("connection-role replay: rows=%v role=%v", rows, plain.PostgrestRequest.ReplayRole)
		}
		if !strings.Contains(strings.Join(plain.PostgrestRequest.ReplayLimitations, " "), `"authenticated" is not impersonated`) {
			t.Fatalf("role limitation not stated: %q", plain.PostgrestRequest.ReplayLimitations)
		}

		// Allowlisted but not granted: refused with the GRANT to run.
		apiOnly.SetPostgRESTReplayRoles([]string{"authenticated"})
		_, err = apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
		if msg := validationMessage(err); !strings.Contains(msg, `GRANT "authenticated" TO`) {
			t.Fatalf("ungranted replay role: %v", err)
		}

		if _, err := admin.Exec(ctx, `GRANT authenticated TO pqn_reader`); err != nil {
			t.Fatal(err)
		}
		inv, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
		if err != nil {
			t.Fatal(err)
		}
		if inv.PostgrestRequest.ReplayRole == nil || *inv.PostgrestRequest.ReplayRole != "authenticated" {
			t.Fatalf("replay role not recorded: %+v", inv.PostgrestRequest)
		}
		// SECURITY: claims are never replayed, even for the impersonated role.
		// The capture's SQL and binds are the real, evidenced request — but
		// alice's sub is parsed only from caller-supplied log text with no
		// verification it is authentic, so it must never be set as session
		// state. Without it, own_notes's claims-keyed RLS policy denies every
		// row, rather than the system forging alice's identity to produce her
		// 10. See TestForgedCaptureImpersonatesArbitraryIdentity for the
		// attack this prevents.
		if rows, ok := scanActualRows(inv.Explain.Plan, "notes"); !ok || rows != 0 {
			t.Fatalf("replay as authenticated without claims saw %v rows, want exactly 0 (claims-keyed RLS must fail closed, not forge alice's identity)", rows)
		}
		if raw, _ := json.Marshal(inv); strings.Contains(string(raw), "alice") {
			t.Fatal("claims leaked into the API investigation")
		}
		// Comparisons replay in the same (claims-free) context.
		cmp, err := apiOnly.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL, VerifyResults: true, Analyze: true})
		if err != nil {
			t.Fatal(err)
		}
		if rows, ok := scanActualRows(cmp.CandidateExplain.Plan, "notes"); !ok || rows != 0 {
			t.Fatalf("comparison ran outside the replay context: %v rows", rows)
		}
		if _, err := apiOnly.RankCandidates(reqCtx, &investigations.RankCandidatesPayload{ID: inv.ID}); err != nil {
			t.Fatalf("rank under replay context: %v", err)
		}
		// Withdrawing the allowlist stops impersonation for existing investigations.
		apiOnly.SetPostgRESTReplayRoles(nil)
		if _, err := apiOnly.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL}); !strings.Contains(validationMessage(err), "no longer allowlisted") {
			t.Fatalf("de-allowlisted role still replayed: %v", err)
		}

		// Bug: re-allowlisting the role in config is not the same as the
		// underlying Postgres GRANT still being there — an admin can revoke
		// that independently. postgrestReplayContext (used by every later
		// RankCandidates/AddCandidate on this investigation) must catch a
		// revoked grant with the same clear remediation message import time
		// gives for the identical condition, not a bare error from deep
		// inside Explain/ComparePlans' own SET LOCAL ROLE.
		apiOnly.SetPostgRESTReplayRoles([]string{"authenticated"})
		if _, err := admin.Exec(ctx, `REVOKE authenticated FROM pqn_reader`); err != nil {
			t.Fatal(err)
		}
		if _, err := apiOnly.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL}); !strings.Contains(validationMessage(err), `GRANT "authenticated" TO`) {
			t.Fatalf("revoked replay grant did not give the clear remediation message on AddCandidate: %v", err)
		}
		if _, err := apiOnly.RankCandidates(reqCtx, &investigations.RankCandidatesPayload{ID: inv.ID}); !strings.Contains(validationMessage(err), `GRANT "authenticated" TO`) {
			t.Fatalf("revoked replay grant did not give the clear remediation message on RankCandidates: %v", err)
		}
		if _, err := admin.Exec(ctx, `GRANT authenticated TO pqn_reader`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("search_path reaches extension operators only when allowed", func(t *testing.T) {
		status, body, excerpt := pgrstCall(t, ctx, stack, "/users?select=id&email=eq."+url.QueryEscape("ALICE@EXAMPLE.COM"), "", endsTransaction)
		if status != 200 || jsonArrayLen(t, body) != 1 {
			t.Fatalf("citext endpoint: %d %s", status, body)
		}
		narrow, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
		if err != nil {
			t.Fatal(err)
		}
		if rows, _ := scanActualRows(narrow.Explain.Plan, "users"); rows != 0 {
			t.Fatalf("without extensions the comparison is case-sensitive text, want 0 rows, got %v", rows)
		}
		if lim := strings.Join(narrow.PostgrestRequest.ReplayLimitations, " "); !strings.Contains(lim, "extensions") || !strings.Contains(lim, "citext") {
			t.Fatalf("unsearched extension schema not disclosed: %s", lim)
		}
		wide, err := withExtensions.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
		if err != nil {
			t.Fatal(err)
		}
		if rows, _ := scanActualRows(wide.Explain.Plan, "users"); rows != 1 {
			t.Fatalf("with extensions allowed the replay must match the endpoint's 1 row, got %v", rows)
		}
		if wide.PostgrestRequest.ReplaySearchPath == nil || *wide.PostgrestRequest.ReplaySearchPath != `"api", "extensions"` {
			t.Fatalf("applied search_path = %v", wide.PostgrestRequest.ReplaySearchPath)
		}
	})

	t.Run("request that hit statement_timeout", func(t *testing.T) {
		slow := pgrstJWT(t, map[string]any{"role": "slow_role"})
		status, body, excerpt := pgrstCall(t, ctx, stack, "/big?select=id&order=v&limit=1", slow, endsTransaction)
		if status != 500 || !strings.Contains(body, "57014") {
			t.Fatalf("expected the endpoint to time out: %d %s", status, body)
		}
		inv, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
		if err != nil {
			t.Fatal(err)
		}
		pr := inv.PostgrestRequest
		if pr.Failure == nil || !strings.Contains(*pr.Failure, "statement timeout") || pr.StatementDurationMs != nil || pr.Role != "slow_role" {
			t.Fatalf("failed request provenance: %+v", pr)
		}
		if inv.Explain.EvidenceMode != "observed" {
			t.Fatalf("the replay must still measure the statement: %+v", inv.Explain)
		}
		if !strings.Contains(strings.Join(pr.ReplayLimitations, " "), "statement_timeout=5ms") {
			t.Fatalf("captured timeout not stated: %q", pr.ReplayLimitations)
		}
	})

	// The model must agree with what PostgreSQL actually does: run the
	// statement as a prepared statement six times and inspect the sixth plan.
	actuallyGeneric := func(t *testing.T, inv *investigations.Investigation) bool {
		t.Helper()
		conn, err := admin.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		args := make([]string, len(inv.Binds))
		for i, b := range inv.Binds {
			args[i] = "'" + strings.ReplaceAll(b, "'", "''") + "'"
		}
		if _, err := conn.Exec(ctx, "PREPARE pqn_model AS "+inv.SQL); err != nil {
			t.Fatal(err)
		}
		defer func() { _, _ = conn.Exec(ctx, "DEALLOCATE pqn_model") }()
		execute := "EXECUTE pqn_model(" + strings.Join(args, ", ") + ")"
		for i := 0; i < 6; i++ {
			if _, err := conn.Exec(ctx, execute); err != nil {
				t.Fatal(err)
			}
		}
		var plan string
		rows, err := conn.Query(ctx, "EXPLAIN (VERBOSE) "+execute)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var line string
			_ = rows.Scan(&line)
			plan += line + "\n"
		}
		return strings.Contains(plan, "$1")
	}
	// Bug: for plan_cache_mode=auto, PostgreSQL decides generic-vs-custom by
	// comparing the generic plan's cost to the AVERAGE of the first five
	// custom-plan costs on that connection — not any single capture's. The
	// model used to compare the generic cost against only this capture's own
	// custom-plan cost and assert a deterministic "likely to switch"/"likely
	// to keep planning" verdict from it, which is only ever correct by
	// accident (it happens to hold below only because actuallyGeneric also
	// replays the identical captured value six times in a row, so the
	// "average of five" coincides with "this one value's own cost" — real
	// production traffic is not six repeats of one value; see
	// TestPostgRESTPlanCacheAutoModeUnknownFromOneCapture below for the
	// skewed-prior-binds case where that coincidence does not hold and the
	// old deterministic claim was wrong).
	t.Run("auto mode never claims a deterministic plan-cache verdict from one capture", func(t *testing.T) {
		for _, path := range []string{
			"/events?select=id&kind=eq.rare&order=id&limit=5",
			"/events?select=id,kind&id=eq.5000",
		} {
			_, _, excerpt := pgrstCall(t, ctx, stack, path, "", endsTransaction)
			inv, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
			if err != nil {
				t.Fatal(err)
			}
			pr := inv.PostgrestRequest
			if pr.PreparedStatement == nil || !*pr.PreparedStatement || pr.GenericPlanTotalCost == nil || pr.PlanCacheNote == nil {
				t.Fatalf("%s: plan cache not modelled: %+v", path, pr)
			}
			note := *pr.PlanCacheNote
			t.Logf("%s: custom %.2f, generic %.2f, PostgreSQL (six identical repeats) chose generic=%v: %s", path, inv.Explain.TotalCost, *pr.GenericPlanTotalCost, actuallyGeneric(t, inv), note)
			if !strings.Contains(note, "cannot be determined from one capture") {
				t.Fatalf("%s: note must not claim a deterministic plan-cache verdict from a single capture: %q", path, note)
			}
			if strings.Contains(note, "likely to switch") || strings.Contains(note, "likely to keep planning") {
				t.Fatalf("%s: note still makes the old deterministic claim: %q", path, note)
			}
		}
	})

	// Regression for the bug above: a real PostgreSQL probe with five prior
	// "common" binds (a seq scan, since "common" matches ~99.9% of
	// api.events) followed by the captured "rare" bind (an index scan,
	// api.events_kind, since "rare" matches ~0.1%). The rare bind's own
	// custom-plan cost is far cheaper than the generic plan's — exactly what
	// used to make the old per-capture model claim "likely to keep planning
	// this statement for each request's values". But PostgreSQL's real
	// decision is made from the average of the five *common* executions
	// (expensive), not this one capture, so it switches the sixth (captured)
	// execution to the generic plan regardless of how cheap that one value
	// would have been under its own custom plan.
	t.Run("TestPostgRESTPlanCacheAutoModeUnknownFromOneCapture", func(t *testing.T) {
		// No ORDER BY/LIMIT here (unlike the subtest above): with them, an
		// index scan ordered by id with an early stop can make both "common"
		// and "rare" equally cheap, masking the cost skew this test depends
		// on. Without them, the plan is driven purely by kind's selectivity.
		_, _, excerpt := pgrstCall(t, ctx, stack, "/events?select=id&kind=eq.rare", "", endsTransaction)
		inv, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
		if err != nil {
			t.Fatal(err)
		}
		pr := inv.PostgrestRequest
		if pr.GenericPlanTotalCost == nil || pr.PlanCacheNote == nil {
			t.Fatalf("plan cache not modelled: %+v", pr)
		}
		g, c := *pr.GenericPlanTotalCost, inv.Explain.TotalCost
		if g <= c {
			t.Fatalf("fixture setup is broken: want the generic plan (%.2f) costlier than this capture's own rare-bind custom plan (%.2f), so the old per-capture model would have claimed \"keep custom\"", g, c)
		}
		if strings.Contains(*pr.PlanCacheNote, "likely to keep planning") {
			t.Fatalf("note still makes the old deterministic claim that would have been wrong here: %q", *pr.PlanCacheNote)
		}

		// inv.Binds may carry more than the "kind" filter value (e.g. the
		// captured LIMIT, typed bigint) — only the bind equal to "rare" is
		// the one under test, so only it is swapped for "common"; every
		// other bind keeps its real captured value and type.
		commonArgs := append([]string(nil), inv.Binds...)
		for i, b := range commonArgs {
			if b == "rare" {
				commonArgs[i] = "common"
			}
		}
		if got := actuallyGenericAfterPriorBinds(t, ctx, admin, inv.SQL, commonArgs, inv.Binds); !got {
			t.Fatal("after five prior 'common' executions, PostgreSQL's sixth (captured 'rare') execution should run the generic plan — the old model's per-capture claim would have been wrong")
		}
	})

	// Create is an ordinary investigation path, not PostgREST-specific — it is
	// one of the many callers ClassifyRunError/SanitizeAPIError serve (ask,
	// queries, queries_compare, reports_generate, schedules). PostgreSQL's
	// SQLSTATE and raw server message must stay out of its error, the same as
	// any other shared caller; only the PostgREST replay-role grant check
	// (postgrestReplay/postgrestReplayContext) gets that detail, by calling
	// executionFailedMessage directly instead of through the shared classifier.
	t.Run("statement errors stay generic outside the PostgREST replay path", func(t *testing.T) {
		_, err := apiOnly.Create(reqCtx, &investigations.CreateInvestigationPayload{Title: "bad column", SQL: `SELECT nope FROM api.notes`})
		msg := validationMessage(err)
		if msg != "Query execution failed. Check your SQL and try again." {
			t.Fatalf("expected the shared generic execution-failed message, got %q", msg)
		}
		if strings.Contains(msg, "SQLSTATE") || strings.Contains(msg, "nope") {
			t.Fatalf("leaked PostgreSQL detail outside the PostgREST replay path: %q", msg)
		}
	})

	t.Run("query length limit is configurable", func(t *testing.T) {
		ids := make([]string, 3000)
		for i := range ids {
			ids[i] = strconv.Itoa(i + 1)
		}
		_, _, excerpt := pgrstCall(t, ctx, stack, "/big?select=id&id=in.("+strings.Join(ids, ",")+")&limit=1", "", endsTransaction)
		_, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
		if msg := validationMessage(err); !strings.Contains(msg, "DATABASE_MAX_QUERY_LENGTH") || !strings.Contains(msg, "bytes") {
			t.Fatalf("over-limit capture: %q", msg)
		}
		if _, err := withExtensions.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt}); err != nil {
			t.Fatalf("raised limit: %v", err)
		}
	})

	// Last: switches the server to production-style slow-query-only logging.
	t.Run("slow-query-only log", func(t *testing.T) {
		for _, stmt := range []string{`ALTER SYSTEM SET log_min_duration_statement = 5`, `SELECT pg_reload_conf()`} {
			if _, err := admin.Exec(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(300 * time.Millisecond)
		_, _, excerpt := pgrstCall(t, ctx, stack, "/big?select=id&order=v&limit=1", "", func(log string) bool {
			return strings.Contains(log, "pgrst_source") && strings.Contains(log, "DETAIL")
		})
		if strings.Contains(excerpt, "set_config") {
			t.Fatalf("fixture: the session context must not be logged:\n%s", excerpt)
		}
		inv, err := apiOnly.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
		if err != nil {
			t.Fatal(err)
		}
		pr := inv.PostgrestRequest
		if pr.SessionContextMissing == nil || !*pr.SessionContextMissing || pr.Endpoint != "" || inv.Title != "PostgREST request reading api.big" || pr.StatementDurationMs == nil {
			t.Fatalf("slow-query-only import: title=%q %+v", inv.Title, pr)
		}
	})
}

// TestPostgRESTStructuredLogs reads the same requests from real csvlog and
// jsonlog files and imports them through the service.
func TestPostgRESTStructuredLogs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstReplaySetup, pgrstJWTSecret,
		"-c", "logging_collector=on", "-c", "log_destination=csvlog,jsonlog",
		"-c", "log_directory=/tmp/pglog", "-c", "log_filename=pg", "-c", "log_parameter_max_length_on_error=-1")
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
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "pgrst-structured", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})
	svc := newPostgRESTService(t, ctx, app, stack.ReaderConnStr, []string{"api"}, 10000)

	readFile := func(name string) string {
		rc, err := stack.PG.CopyFileFromContainer(ctx, "/tmp/pglog/"+name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		defer rc.Close()
		b, _ := io.ReadAll(rc)
		return string(b)
	}
	for _, tc := range []struct{ name, path, token string }{
		{"normal", "/notes?select=id&order=id&limit=3", ""},
		{"timeout", "/big?select=id&order=v&limit=1", pgrstJWT(t, map[string]any{"role": "slow_role"})},
	} {
		beforeCSV, beforeJSON := len(readFile("pg.csv")), len(readFile("pg.json"))
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, stack.BaseURL+tc.path, nil)
		if tc.token != "" {
			req.Header.Set("Authorization", "Bearer "+tc.token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		for _, f := range []struct {
			file   string
			offset int
		}{{"pg.csv", beforeCSV}, {"pg.json", beforeJSON}} {
			t.Run(tc.name+"/"+f.file, func(t *testing.T) {
				var excerpt string
				for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(150 * time.Millisecond) {
					if excerpt = readFile(f.file)[f.offset:]; endsTransaction(excerpt) {
						break
					}
					if time.Now().After(deadline) {
						t.Fatal("request not fully logged")
					}
				}
				inv, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
				if err != nil {
					t.Fatalf("import %s: %v", f.file, err)
				}
				pr := inv.PostgrestRequest
				if tc.name == "timeout" && (pr.Failure == nil || pr.Role != "slow_role") {
					t.Fatalf("timeout from %s: %+v", f.file, pr)
				}
				if tc.name == "normal" && (pr.Failure != nil || pr.Endpoint != "/notes" || pr.QueryID == nil) {
					t.Fatalf("request from %s: %+v", f.file, pr)
				}
			})
		}
	}
}
