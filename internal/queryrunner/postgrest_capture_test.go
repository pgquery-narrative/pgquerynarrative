package queryrunner

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// realPostgRESTLog is an unedited excerpt from PostgREST v16.4 running
// GET /orders_daily?day=eq.2023-03-01T00:00:00&select=id,total&order=id&limit=3
// against PostgreSQL 18 with log_min_duration_statement=0 and
// log_parameter_max_length=-1.
func realPostgRESTLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/postgrest_orders_daily.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParsePostgRESTLogRealCapture(t *testing.T) {
	got, err := ParsePostgRESTLog(realPostgRESTLog(t))
	if err != nil {
		t.Fatalf("ParsePostgRESTLog: %v", err)
	}
	if got.Method != "GET" || got.Endpoint != "/orders_daily" || got.Role != "web_anon" {
		t.Fatalf("provenance = %+v", got)
	}
	if got.SearchPath != `"api", "public"` {
		t.Fatalf("search_path = %q", got.SearchPath)
	}
	if want := []string{"2023-03-01 00:00:00", "3", "0"}; !reflect.DeepEqual(got.Binds, want) {
		t.Fatalf("binds = %q, want %q", got.Binds, want)
	}
	if got.StatementMs == nil || *got.StatementMs != 37.535 {
		t.Fatalf("statement ms = %v, want 37.535 (the execute line, not the transaction)", got.StatementMs)
	}
	if !strings.HasPrefix(got.SQL, "WITH pgrst_source AS") || !strings.Contains(got.SQL, `"api"."orders_daily"."day" = $1`) {
		t.Fatalf("SQL is not the exact generated statement: %s", got.SQL)
	}
	if !IsPostgRESTWrapper(got.SQL) {
		t.Fatal("captured statement should be recognized as the PostgREST wrapper")
	}
}

func TestParsePostgRESTLogNeverKeepsCredentials(t *testing.T) {
	// Same excerpt with an Authorization header and a JWT in the session context.
	log := strings.ReplaceAll(realPostgRESTLog(t),
		`$6 = '{"user-agent":"curl/8.7.1"`,
		`$6 = '{"authorization":"Bearer eyJhbGciOiJIUzI1NiJ9.secret","user-agent":"curl/8.7.1"`)
	log = strings.ReplaceAll(log, `$3 = '{"role":"web_anon"}'`, `$3 = '{"role":"web_anon","sub":"eyJhbGciOi-token-value"}'`)
	got, err := ParsePostgRESTLog(log)
	if err != nil {
		t.Fatalf("ParsePostgRESTLog: %v", err)
	}
	for _, v := range []string{got.Method, got.Endpoint, got.Role, got.SearchPath, got.QueryID, got.SQL, strings.Join(got.Binds, "|")} {
		if strings.Contains(v, "eyJ") || strings.Contains(v, "Bearer") || strings.Contains(v, "secret") {
			t.Fatalf("credential material leaked into capture: %q", v)
		}
	}
}

func TestParsePostgRESTLogPostgres16ParameterPrefix(t *testing.T) {
	got, err := ParsePostgRESTLog(strings.ReplaceAll(realPostgRESTLog(t), "Parameters: ", "parameters: "))
	if err != nil || got.Role != "web_anon" || len(got.Binds) != 3 {
		t.Fatalf("PostgreSQL 16 capture: %+v, %v", got, err)
	}
}

func TestParsePostgRESTLogFailsClosed(t *testing.T) {
	base := realPostgRESTLog(t)

	tests := []struct {
		name    string
		log     string
		wantErr error
		wantSub string
	}{
		{
			name:    "no PostgREST read in excerpt",
			log:     "2026-10-06 10:07:55.832 UTC [107] app=PostgREST 16.4 LOG:  duration: 0.016 ms  execute 3: COMMIT\n",
			wantErr: ErrNoPostgRESTRead,
		},
		{
			name:    "two requests in one excerpt",
			log:     base + strings.ReplaceAll(base, "[107]", "[108]"),
			wantSub: "holds 2 PostgREST read requests",
		},
		{
			name:    "write request is not a read",
			log:     strings.ReplaceAll(base, `$4 = 'GET'`, `$4 = 'DELETE'`),
			wantSub: `only GET and HEAD read requests`,
		},
		{
			name:    "missing DETAIL parameters for the request",
			log:     dropDetailAfter(base, "execute 2:"),
			wantSub: "log_parameter_max_length=-1",
		},
		{
			name:    "NULL bind value",
			log:     strings.ReplaceAll(base, `$3 = '0'`, `$3 = NULL`),
			wantSub: "NULL binds are not supported",
		},
		{
			name:    "statement needs more binds than logged",
			log:     strings.ReplaceAll(base, `$2 = '3', $3 = '0'`, `$2 = '3'`),
			wantSub: "uses $3 but the log carries 2",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParsePostgRESTLog(tc.log)
			if err == nil {
				t.Fatalf("expected error, got capture %+v", got)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %q, want it to contain %q", err, tc.wantSub)
			}
		})
	}
}

// dropDetailAfter removes the DETAIL line that follows the first line containing marker.
func dropDetailAfter(log, marker string) string {
	lines := strings.Split(log, "\n")
	for i, l := range lines {
		if strings.Contains(l, marker) {
			for j := i + 1; j < len(lines); j++ {
				if strings.Contains(lines[j], "DETAIL:") {
					return strings.Join(append(lines[:j:j], lines[j+1:]...), "\n")
				}
			}
		}
	}
	return log
}

func TestSplitLogParamsKeepsEmbeddedMarkers(t *testing.T) {
	got, err := splitLogParams(`$1 = 'it''s $2 = fake', $2 = '3', $3 = 'NULL'`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"it's $2 = fake", "3", "NULL"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("splitLogParams = %q, want %q", got, want)
	}
}

func TestSuggestRewritesDeclinesPostgRESTWrapper(t *testing.T) {
	// The same date_trunc predicate that yields a sargable rewrite for user SQL
	// must not yield one inside PostgREST's generated wrapper.
	wrapped := `WITH pgrst_source AS ( SELECT id FROM data.orders WHERE date_trunc('day', created_at) = $1 ORDER BY id LIMIT $2 OFFSET $3 ) SELECT null::bigint AS total_result_set, pg_catalog.count(_postgrest_t) AS page_total, coalesce(json_agg(_postgrest_t), '[]') AS body, nullif(current_setting('response.headers', true), '') AS response_headers, nullif(current_setting('response.status', true), '') AS response_status, '' AS response_inserted FROM ( SELECT * FROM pgrst_source ) _postgrest_t`
	findings := []PlanFinding{{IsSeqScan: true, Category: CategoryIndexCandidate, Relation: "orders", Schema: "data", Message: "Seq Scan on orders"}}
	if got := SuggestRewrites(wrapped, findings); len(got) != 0 {
		t.Fatalf("wrapper must decline all rewrites, got %d", len(got))
	}
	if !IsPostgRESTWrapper(wrapped) {
		t.Fatal("wrapper not detected")
	}
	if IsPostgRESTWrapper("SELECT id FROM data.orders WHERE id = 1") {
		t.Fatal("ordinary SQL must not be detected as a wrapper")
	}
}

func TestCollectRelationsSkipsWrapperCTE(t *testing.T) {
	rels, err := CollectRelations(`WITH pgrst_source AS ( SELECT id FROM "api"."orders_daily" WHERE day = $1 ) SELECT count(*) FROM pgrst_source`)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"api.orders_daily"}; !reflect.DeepEqual(rels, want) {
		t.Fatalf("relations = %q, want %q", rels, want)
	}
}

func TestServerTimingDuration(t *testing.T) {
	h := "jwt;dur=0.6, parse;dur=0.0, plan;dur=0.0, transaction;dur=54.8, response;dur=0.1"
	if d, ok := ServerTimingDuration(h, "transaction"); !ok || d != 54.8 {
		t.Fatalf("transaction = %v, %v", d, ok)
	}
	if _, ok := ServerTimingDuration(h, "db"); ok {
		t.Fatal("absent metric must not be reported")
	}
}

func TestPostgRESTLogRejectsAmbiguousOrIncompleteContext(t *testing.T) {
	base := realPostgRESTLog(t)
	for name, input := range map[string]string{
		"URL query contains secret": strings.ReplaceAll(base, "'/orders_daily'", "'/orders_daily?token=private'"),
		"NULL context":              strings.ReplaceAll(base, "$2 = 'web_anon'", "$2 = NULL"),
		"truncated parameter":       strings.ReplaceAll(base, "$1 = '2023-03-01 00:00:00'", "$1 = '2023-03-01'..."),
		"duplicate parameter":       strings.ReplaceAll(base, "$2 = '3', $3 = '0'", "$2 = '3', $2 = '0'"),
		"incomplete context keys":   strings.ReplaceAll(base, "'request.cookies'", "'unknown.setting'"),
		"session-level setting":     strings.ReplaceAll(base, "set_config('role', $2, true)", "set_config('role', $2, false)"),
		"multiline record":          base + "unprefixed log continuation\n",
		"oversize input":            strings.Repeat("x", 200001),
		"invalid UTF8":              base + string([]byte{0xff}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePostgRESTLog(input); err == nil {
				t.Fatal("unsafe/ambiguous capture accepted")
			}
		})
	}
}

// When the request's own session context is not in its transaction, the
// capture imports with unknown provenance; it never borrows another
// transaction's or another backend's context.
func TestPostgRESTLogWithoutOwnSessionContext(t *testing.T) {
	base := realPostgRESTLog(t)
	marker := "2026-10-06 10:07:55.832 UTC [107] app=PostgREST 16.4 LOG:  duration: 37.535"
	for name, input := range map[string]string{
		"transaction boundary":               strings.Replace(base, marker, "2026-10-06 10:07:55.831 UTC [107] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 8: COMMIT\n"+marker, 1),
		"aborted after an error":             strings.Replace(base, marker, "2026-10-06 10:07:55.831 UTC [107] app=PostgREST 16.4 ERROR:  canceled\n2026-10-06 10:07:55.831 UTC [107] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 6: ABORT\n"+marker, 1),
		"context belongs to another backend": strings.Replace(strings.Replace(base, "[107] app=PostgREST 16.4 LOG:  duration: 0.082 ms  execute 1:", "[999] app=PostgREST 16.4 LOG:  duration: 0.082 ms  execute 1:", 1), "[107] app=PostgREST 16.4 DETAIL:  Parameters: $1 = '\"api\"", "[999] app=PostgREST 16.4 DETAIL:  Parameters: $1 = '\"api\"", 2),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ParsePostgRESTLog(input)
			if err != nil {
				t.Fatal(err)
			}
			if got.ContextLogged || got.Role != "" || got.Endpoint != "" {
				t.Fatalf("borrowed a session context that is not this request's: %+v", got)
			}
		})
	}
}

func TestPostgRESTLogMatchesInterleavedBackendAndASTParameters(t *testing.T) {
	base := realPostgRESTLog(t)
	// Context parameter order is not a contract; use the AST to find each value.
	base = strings.ReplaceAll(base, "set_config('role', $2, true)", "set_config('role', $8, true)")
	base = strings.ReplaceAll(base, "set_config('request.jwt.claims', $3, true)", "set_config('request.jwt.claims', $2, true)")
	base = strings.ReplaceAll(base, "set_config('role', $8, true)", "set_config('role', $3, true)")
	base = strings.ReplaceAll(base, `$2 = 'web_anon', $3 = '{"role":"web_anon"}'`, `$2 = '{"token":"private"}', $3 = 'web_anon'`)
	marker := "2026-10-06 10:07:55.832 UTC [107] app=PostgREST 16.4 DETAIL:"
	base = strings.Replace(base, marker, "2026-10-06 10:07:55.832 UTC [999] app=PostgREST 16.4 LOG:  duration: 1 ms  statement: SELECT 1\n"+marker, 1)
	got, err := ParsePostgRESTLog(base)
	if err != nil || got.Role != "web_anon" || len(got.Binds) != 3 {
		t.Fatalf("interleaved/reordered context: %+v, %v", got, err)
	}
}

func TestServerTimingRejectsNonfiniteAndNegativeDurations(t *testing.T) {
	for _, value := range []string{"-1", "NaN", "Inf", "1e999", "oops"} {
		if _, ok := ServerTimingDuration("transaction;dur="+value, "transaction"); ok {
			t.Fatalf("invalid duration accepted: %s", value)
		}
	}
}

// PostgreSQL marks a value cut by log_parameter_max_length by appending "..."
// inside the quotes, so a value ending that way is accepted only when a longer,
// untruncated value logged under the same setting proves the limit was not hit.
func TestParsePostgRESTLogEllipsisNeedsProof(t *testing.T) {
	genuine, err := os.ReadFile("testdata/postgrest_ellipsis.log")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParsePostgRESTLog(string(genuine))
	if err != nil || !reflect.DeepEqual(got.Binds, []string{"Loading..."}) {
		t.Fatalf("real request for 'Loading...' (headers prove no truncation): %+v, %v", got, err)
	}
	// The same value with no longer untruncated parameter anywhere in the excerpt.
	lines := strings.Split(string(genuine), "\n")
	var mainOnly []string
	for _, l := range lines {
		if strings.Contains(l, "pgrst_source") || strings.Contains(l, "'Loading...'") {
			mainOnly = append(mainOnly, l)
		}
	}
	if _, err := ParsePostgRESTLog(strings.Join(mainOnly, "\n")); err == nil || !strings.Contains(err.Error(), "log_parameter_max_length") {
		t.Fatalf("an unprovable '...' value must be refused with advice, got %v", err)
	}
	// A value longer than everything else in the excerpt cannot be proven either.
	long := strings.Repeat("z", 400) + "..."
	if _, err := ParsePostgRESTLog(strings.ReplaceAll(string(genuine), "'Loading...'", "'"+long+"'")); err == nil {
		t.Fatal("a '...' value longer than every untruncated value must be refused")
	}
}

// ellipsisMainOnly is the postgrest_ellipsis.log fixture stripped to only its
// target request's own lines (its session-context statement dropped), the
// same reduction TestParsePostgRESTLogEllipsisNeedsProof uses to produce a
// request with no evidence of its own that "Loading..." was not truncated.
func ellipsisMainOnly(t *testing.T) string {
	t.Helper()
	genuine, err := os.ReadFile("testdata/postgrest_ellipsis.log")
	if err != nil {
		t.Fatal(err)
	}
	var mainOnly []string
	for _, l := range strings.Split(string(genuine), "\n") {
		if strings.Contains(l, "pgrst_source") || strings.Contains(l, "'Loading...'") {
			mainOnly = append(mainOnly, l)
		}
	}
	out := strings.Join(mainOnly, "\n")
	if _, err := ParsePostgRESTLog(out); err == nil {
		t.Fatal("fixture setup is broken: expected this reduced excerpt to be refused on its own")
	}
	return out
}

// Bug: unprovenTruncation's "longest untruncated value" was computed over
// every paramSet parsed anywhere in the log excerpt, filtered only by
// source ("detail" vs "context") — never by which backend logged it. A
// target request on one PID with no evidence of its own that a value was
// not truncated became wrongly accepted the moment any OTHER backend in the
// same excerpt happened to log a longer, non-truncated "detail" parameter,
// even though that unrelated backend's logging configuration says nothing
// about this request's own.
func TestParsePostgRESTLogUnrelatedBackendCannotProveTruncation(t *testing.T) {
	mainOnly := ellipsisMainOnly(t)
	// An entirely unrelated backend (PID 999, no pgrst_source statement, no
	// relation to this request at all) logs a much longer, untruncated
	// "detail" parameter on its own unrelated statement.
	unrelated := "2026-10-08 17:38:18.420 UTC [999] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 5: SELECT 1\n" +
		"2026-10-08 17:38:18.420 UTC [999] app=PostgREST 16.4 DETAIL:  Parameters: $1 = '" + strings.Repeat("z", 400) + "'\n"
	if _, err := ParsePostgRESTLog(unrelated + mainOnly); err == nil || !strings.Contains(err.Error(), "log_parameter_max_length") {
		t.Fatalf("an unrelated backend's longer parameter must not rescue an unproven truncated value, got %v", err)
	}
	// Order must not matter: the same unrelated evidence appended after the
	// target request's own lines must be rejected too.
	if _, err := ParsePostgRESTLog(mainOnly + unrelated); err == nil {
		t.Fatal("an unrelated backend's longer parameter (logged after the request) must not rescue it either")
	}
}

// Bug (same root cause): a connection pooler hands the same OS PID to many
// sequential, unrelated backends/transactions over an excerpt's lifetime.
// unprovenTruncation's evidence was scoped only by source, never by
// transaction, so an earlier, already-finished transaction on the very same
// PID — fully separated from the target request by its own COMMIT — could
// still "prove" the target request's truncated-looking value was not cut,
// even though a backend restarting under a reused PID (or a pooled backend
// between two unrelated requests) is not evidence the second request used
// the same log_parameter_max_length configuration as the first.
func TestParsePostgRESTLogReusedPIDAcrossTransactionsCannotProveTruncation(t *testing.T) {
	mainOnly := ellipsisMainOnly(t)
	// An earlier, fully-finished transaction on the SAME PID (106) as the
	// target request, closed by its own COMMIT before the target request's
	// lines begin.
	earlierTxn := "2026-10-08 17:30:00.000 UTC [106] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 1: BEGIN\n" +
		"2026-10-08 17:30:00.000 UTC [106] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 2: SELECT $1\n" +
		"2026-10-08 17:30:00.000 UTC [106] app=PostgREST 16.4 DETAIL:  Parameters: $1 = '" + strings.Repeat("z", 400) + "'\n" +
		"2026-10-08 17:30:00.000 UTC [106] app=PostgREST 16.4 LOG:  duration: 0.1 ms  execute 3: COMMIT\n"
	if _, err := ParsePostgRESTLog(earlierTxn + mainOnly); err == nil || !strings.Contains(err.Error(), "log_parameter_max_length") {
		t.Fatalf("an earlier, COMMITted transaction on a reused PID must not rescue a later transaction's unproven truncated value, got %v", err)
	}
}

// log_parameter_max_length (DETAIL) and log_parameter_max_length_on_error
// (CONTEXT) are independently configurable, so proof from one source must
// never cover a value logged under the other, even within the same
// transaction on the same backend.
func TestParsePostgRESTLogIndependentDetailAndContextLimits(t *testing.T) {
	log := fixture(t, "postgrest_timeout.log")
	// Drop the failing bind's own DETAIL line, exactly as
	// TestParsePostgRESTLogFailedRequestSources does, so the request's binds
	// can only be recovered from the error CONTEXT line (source "context") —
	// otherwise the bind's own DETAIL (source "detail") always wins. Then make
	// the recovered CONTEXT value end in "...". The same transaction's
	// session-context DETAIL (the JWT header blob) is a long, untruncated
	// "detail"-source value — proof for "detail", not "context".
	var noBindDetail []string
	lines := strings.Split(log, "\n")
	for i, l := range lines {
		if strings.Contains(l, "DETAIL:  Parameters: $1 = '1'") && i > 0 && strings.Contains(lines[i-1], " bind ") {
			continue
		}
		noBindDetail = append(noBindDetail, l)
	}
	withAmbiguousContext := strings.Replace(strings.Join(noBindDetail, "\n"),
		`CONTEXT:  unnamed portal with parameters: $1 = '1', $2 = '0'`,
		`CONTEXT:  unnamed portal with parameters: $1 = '1...', $2 = '0'`, 1)
	// Sanity: this fixture's long "detail"-source header value (the JWT blob)
	// is still present and untouched by the edit above.
	if !strings.Contains(withAmbiguousContext, `$8 = '{"user-agent"`) {
		t.Fatal("fixture setup is broken: expected a long detail-source value to remain in the transaction")
	}
	if _, err := ParsePostgRESTLog(withAmbiguousContext); err == nil {
		t.Fatal("a long same-transaction DETAIL value must not prove a CONTEXT value was not truncated")
	}
}

// Only an exact trailing "..." is treated as ambiguous; dots elsewhere, or a
// short run that doesn't end the value, are ordinary data.
func TestSplitLogParamsAllowsDotsThatDoNotEndTheValue(t *testing.T) {
	got, err := splitLogParams(`$1 = 'a...b', $2 = '...leading', $3 = 'trailing.dot.'`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a...b", "...leading", "trailing.dot."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// realZeroParameterLog is an unedited PostgreSQL 16.4 + PostgREST v16.4
// excerpt (captured auditing this parser on 2026-10-07) for
// GET /items?select=id,code&order=id — a statement with zero $N parameters.
// PostgreSQL logs no DETAIL line at all for it, since there is nothing to
// report.
const realZeroParameterLog = `2026-10-07 07:21:18.276 UTC [86] -7610786251424593350 app=PostgREST 16.4 LOG:  duration: 0.015 ms  bind 0: BEGIN ISOLATION LEVEL READ COMMITTED READ ONLY
2026-10-07 07:21:18.276 UTC [86] -7610786251424593350 app=PostgREST 16.4 LOG:  duration: 0.020 ms  execute 0: BEGIN ISOLATION LEVEL READ COMMITTED READ ONLY
2026-10-07 07:21:18.276 UTC [86] -3704990386267960320 app=PostgREST 16.4 LOG:  duration: 0.022 ms  bind 1: select set_config('search_path', $1, true), set_config('role', $2, true), set_config('request.jwt.claims', $3, true), set_config('request.method', $4, true), set_config('request.path', $5, true), set_config('request.headers', $6, true), set_config('request.cookies', $7, true)
2026-10-07 07:21:18.276 UTC [86] -3704990386267960320 app=PostgREST 16.4 DETAIL:  Parameters: $1 = '"api", "public"', $2 = 'web_anon', $3 = '{"role":"web_anon"}', $4 = 'GET', $5 = '/items', $6 = '{"accept-encoding":"gzip","user-agent":"Go-http-client/1.1","host":"localhost:55029"}', $7 = '{}'
2026-10-07 07:21:18.276 UTC [86] -3704990386267960320 app=PostgREST 16.4 LOG:  duration: 0.013 ms  execute 1: select set_config('search_path', $1, true), set_config('role', $2, true), set_config('request.jwt.claims', $3, true), set_config('request.method', $4, true), set_config('request.path', $5, true), set_config('request.headers', $6, true), set_config('request.cookies', $7, true)
2026-10-07 07:21:18.276 UTC [86] -3704990386267960320 app=PostgREST 16.4 DETAIL:  Parameters: $1 = '"api", "public"', $2 = 'web_anon', $3 = '{"role":"web_anon"}', $4 = 'GET', $5 = '/items', $6 = '{"accept-encoding":"gzip","user-agent":"Go-http-client/1.1","host":"localhost:55029"}', $7 = '{}'
2026-10-07 07:21:18.276 UTC [86] 5945821488514438717 app=PostgREST 16.4 LOG:  duration: 0.105 ms  parse 16: WITH pgrst_source AS ( SELECT "api"."items"."id", "api"."items"."code" FROM "api"."items"  ORDER BY "api"."items"."id"    )  SELECT null::bigint AS total_result_set, pg_catalog.count(_postgrest_t) AS page_total, coalesce(json_agg(_postgrest_t), '[]') AS body, nullif(current_setting('response.headers', true), '') AS response_headers, nullif(current_setting('response.status', true), '') AS response_status, '' AS response_inserted FROM ( SELECT * FROM pgrst_source ) _postgrest_t
2026-10-07 07:21:18.276 UTC [86] 5945821488514438717 app=PostgREST 16.4 LOG:  duration: 0.124 ms  bind 16: WITH pgrst_source AS ( SELECT "api"."items"."id", "api"."items"."code" FROM "api"."items"  ORDER BY "api"."items"."id"    )  SELECT null::bigint AS total_result_set, pg_catalog.count(_postgrest_t) AS page_total, coalesce(json_agg(_postgrest_t), '[]') AS body, nullif(current_setting('response.headers', true), '') AS response_headers, nullif(current_setting('response.status', true), '') AS response_status, '' AS response_inserted FROM ( SELECT * FROM pgrst_source ) _postgrest_t
2026-10-07 07:21:18.276 UTC [86] 5945821488514438717 app=PostgREST 16.4 LOG:  duration: 0.032 ms  execute 16: WITH pgrst_source AS ( SELECT "api"."items"."id", "api"."items"."code" FROM "api"."items"  ORDER BY "api"."items"."id"    )  SELECT null::bigint AS total_result_set, pg_catalog.count(_postgrest_t) AS page_total, coalesce(json_agg(_postgrest_t), '[]') AS body, nullif(current_setting('response.headers', true), '') AS response_headers, nullif(current_setting('response.status', true), '') AS response_status, '' AS response_inserted FROM ( SELECT * FROM pgrst_source ) _postgrest_t
2026-10-07 07:21:18.276 UTC [86] 6097083398544187049 app=PostgREST 16.4 LOG:  duration: 0.008 ms  bind 3: COMMIT
2026-10-07 07:21:18.276 UTC [86] 6097083398544187049 app=PostgREST 16.4 LOG:  duration: 0.013 ms  execute 3: COMMIT
`

func TestParsePostgRESTLogZeroParameterStatement(t *testing.T) {
	got, err := ParsePostgRESTLog(realZeroParameterLog)
	if err != nil {
		t.Fatalf("a valid zero-parameter GET must import (this used to fail, telling the caller to enable a setting that was already enabled): %v", err)
	}
	if len(got.Binds) != 0 {
		t.Fatalf("expected zero binds, got %d: %q", len(got.Binds), got.Binds)
	}
	if got.Endpoint != "/items" || got.Method != "GET" {
		t.Fatalf("provenance = %+v", got)
	}
}

// A quoted identifier containing the text "$1" is not a parameter — the AST,
// not a lexical scan, decides what counts as one — so a statement with zero
// real parameters and such an identifier must still import with zero binds.
func TestParsePostgRESTLogZeroParameterStatementWithQuotedDollarText(t *testing.T) {
	log := strings.ReplaceAll(realZeroParameterLog, `"api"."items"."code"`, `"col$1"`)
	got, err := ParsePostgRESTLog(log)
	if err != nil {
		t.Fatalf("a quoted identifier containing the text \"$1\" must not be mistaken for a parameter: %v", err)
	}
	if len(got.Binds) != 0 {
		t.Fatalf("expected zero binds, got %d", len(got.Binds))
	}
}

// Defensive negative control: a statement with zero ParamRef nodes must still
// be refused if its log nonetheless carries DETAIL parameters for it — the
// n != len(binds) check applies to the zero case too, not only to
// parameterized statements.
func TestParsePostgRESTLogRejectsDetailMismatchForZeroParamStatement(t *testing.T) {
	marker := "execute 16: WITH pgrst_source"
	idx := strings.Index(realZeroParameterLog, marker)
	if idx < 0 {
		t.Fatal("fixture marker not found")
	}
	lineEnd := strings.Index(realZeroParameterLog[idx:], "\n")
	insertAt := idx + lineEnd + 1
	log := realZeroParameterLog[:insertAt] +
		"2026-10-07 07:21:18.276 UTC [86] 5945821488514438717 app=PostgREST 16.4 DETAIL:  Parameters: $1 = 'unexpected'\n" +
		realZeroParameterLog[insertAt:]
	if _, err := ParsePostgRESTLog(log); err == nil {
		t.Fatal("a zero-parameter statement whose log carries DETAIL parameters anyway must be refused")
	}
}

// PostgREST appends further set_config calls (for example timezone, from a
// Prefer: timezone=... header) to its session-context statement; the required
// seven keys are a floor, not an exact count, and every extra target must
// still pass the same strict set_config(key, $n, true) shape check.
func TestParsePostgRESTLogSessionContextAllowsOptionalExtraSettings(t *testing.T) {
	base := realPostgRESTLog(t)
	withTZ := strings.ReplaceAll(base,
		"set_config('request.cookies', $7, true)",
		"set_config('request.cookies', $7, true), set_config('timezone', $8, true)")
	withTZ = strings.ReplaceAll(withTZ,
		`$7 = '{}'`,
		`$7 = '{}', $8 = 'America/Los_Angeles'`)
	got, err := ParsePostgRESTLog(withTZ)
	if err != nil {
		t.Fatalf("an optional extra session setting must not reject the capture: %v", err)
	}
	if got.Role != "web_anon" || got.SearchPath != `"api", "public"` {
		t.Fatalf("provenance = %+v", got)
	}
}

// An extra target in the session-context statement is still validated with
// the same strict shape as the required seven — an arbitrary expression in
// the extra slot must not be trusted just because the key count now floats.
func TestParsePostgRESTLogSessionContextRejectsMalformedExtraSetting(t *testing.T) {
	base := realPostgRESTLog(t)
	withBogus := strings.ReplaceAll(base,
		"set_config('request.cookies', $7, true)",
		"set_config('request.cookies', $7, true), pg_sleep(0)")
	if _, err := ParsePostgRESTLog(withBogus); err == nil {
		t.Fatal("a malformed/non-set_config extra target must be refused")
	}
}

// A duplicate key among the extra settings must be refused, same as a
// duplicate among the required seven.
func TestParsePostgRESTLogSessionContextRejectsDuplicateExtraKey(t *testing.T) {
	base := realPostgRESTLog(t)
	withDup := strings.ReplaceAll(base,
		"set_config('request.cookies', $7, true)",
		"set_config('request.cookies', $7, true), set_config('role', $8, true)")
	withDup = strings.ReplaceAll(withDup, `$7 = '{}'`, `$7 = '{}', $8 = 'web_anon'`)
	if _, err := ParsePostgRESTLog(withDup); err == nil {
		t.Fatal("a duplicate key in an extra setting must be refused")
	}
}

// PostgreSQL's stderr format writes each newline inside a message as newline +
// tab. Other clients' formatted SQL lands in the same window, and PostgREST's
// own CSV statement contains newline literals; both used to fail the import.
func TestParsePostgRESTLogMultilineRecords(t *testing.T) {
	base := realPostgRESTLog(t)
	lines := strings.SplitAfter(base, "\n")
	other := "2026-10-06 10:07:55.793 UTC [222] app=psql LOG:  duration: 1.000 ms  statement: SELECT 1\n\tFROM pg_class\n"
	if _, err := ParsePostgRESTLog(strings.Join(lines[:8], "") + other + strings.Join(lines[8:], "")); err != nil {
		t.Fatalf("another backend's multi-line statement broke the import: %v", err)
	}
	got, err := ParsePostgRESTLog(strings.ReplaceAll(base, "'[]') AS body", "'[]') || '\n\t' AS body"))
	if err != nil {
		t.Fatalf("multi-line PostgREST statement: %v", err)
	}
	if !strings.Contains(got.SQL, "'[]') || '\n' AS body") {
		t.Fatalf("multi-line SQL not restored exactly: %q", got.SQL)
	}
}

// Only the backend PID is needed from log_line_prefix, so common production
// prefixes and managed services that never log application_name work.
func TestParsePostgRESTLogAcceptsCommonLinePrefixes(t *testing.T) {
	base := realPostgRESTLog(t)
	const ours = "[107] app=PostgREST 16.4 "
	for name, prefix := range map[string]string{
		"user@db":        "[107] authenticator@pgquerynarrative app=PostgREST 16.4 ",
		"pgBadger":       "[107]: [3-1] user=authenticator,db=app,app=PostgREST 16.4,client=10.0.0.5 ",
		"RDS, no app":    ":10.0.0.5(5432):authenticator@app:[107]:",
		"PID+line [p-l]": "[107-3] ",
	} {
		got, err := ParsePostgRESTLog(strings.ReplaceAll(base, ours, prefix))
		if err != nil || got.Role != "web_anon" || len(got.Binds) != 3 {
			t.Fatalf("%s: %+v, %v", name, got, err)
		}
	}
	if _, err := ParsePostgRESTLog(strings.ReplaceAll(base, ours, "app=PostgREST ")); err == nil || !strings.Contains(err.Error(), "[%p]") {
		t.Fatalf("a prefix without the PID must be refused with an actionable error, got %v", err)
	}
}

func TestParsePostgRESTLogHEADSettingsAndAmbiguity(t *testing.T) {
	base := realPostgRESTLog(t)
	if got, err := ParsePostgRESTLog(strings.ReplaceAll(base, "$4 = 'GET'", "$4 = 'HEAD'")); err != nil || got.Method != "HEAD" {
		t.Fatalf("HEAD is a read (Prefer: count=exact row counts): %+v, %v", got, err)
	}
	if _, err := ParsePostgRESTLog(base + strings.ReplaceAll(base, "[107]", "[108]")); err == nil || !strings.Contains(err.Error(), "backend PIDs 107, 108") {
		t.Fatalf("ambiguous excerpt must name the backends to filter by, got %v", err)
	}
	log := strings.ReplaceAll(base, "set_config('request.cookies', $7, true)",
		"set_config('request.cookies', $7, true), set_config('TimeZone', $8, true), set_config('app.secret', $9, true)")
	log = strings.ReplaceAll(log, `$7 = '{}'`, `$7 = '{}', $8 = 'America/New_York', $9 = 'hunter2'`)
	got, err := ParsePostgRESTLog(log)
	if err != nil || got.Settings["timezone"] != "America/New_York" || got.OtherSettings != 1 {
		t.Fatalf("settings: %+v, %v", got, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", *got), "hunter2") || strings.Contains(fmt.Sprintf("%+v", *got), "app.secret") {
		t.Fatal("a custom setting's name or value leaked into the capture")
	}
}

// toLogStatementAll rewrites a log_min_duration_statement capture into the
// layout PostgreSQL writes when log_statement=all is also on: the execute
// statement without a duration, its DETAIL, then a bare duration record.
// withDuration=false models log_statement alone (no duration records).
func toLogStatementAll(log string, withDuration bool) string {
	lines := strings.Split(strings.TrimRight(log, "\n"), "\n")
	var out []string
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		head, rest, ok := strings.Cut(line, "LOG:  duration: ")
		ms, stmt, ok2 := strings.Cut(rest, " ms  ")
		if !ok || !ok2 || !(strings.HasPrefix(stmt, "execute ") || strings.HasPrefix(stmt, "statement: ")) {
			out = append(out, line)
			continue
		}
		out = append(out, head+"LOG:  "+stmt)
		if i+1 < len(lines) && strings.Contains(lines[i+1], "DETAIL:  ") {
			i++
			out = append(out, lines[i])
		}
		if withDuration {
			out = append(out, head+"LOG:  duration: "+ms+" ms")
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func TestParsePostgRESTLogStatementAll(t *testing.T) {
	base := realPostgRESTLog(t)
	got, err := ParsePostgRESTLog(toLogStatementAll(base, true))
	if err != nil {
		t.Fatalf("log_statement=all capture: %v", err)
	}
	if got.StatementMs == nil || *got.StatementMs != 37.535 || got.Role != "web_anon" || len(got.Binds) != 3 {
		t.Fatalf("log_statement=all capture = %+v", got)
	}
	got, err = ParsePostgRESTLog(toLogStatementAll(base, false))
	if err != nil || got.StatementMs != nil || len(got.Binds) != 3 {
		t.Fatalf("log_statement without durations: %+v, %v", got, err)
	}
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Real PostgreSQL 18 + PostgREST 16.4 output for the same requests in each
// log format (logging_collector with log_destination=stderr,csvlog,jsonlog).
func TestParsePostgRESTLogFormats(t *testing.T) {
	for _, ext := range []string{"log", "csv", "json"} {
		t.Run("normal."+ext, func(t *testing.T) {
			got, err := ParsePostgRESTLog(fixture(t, "postgrest_normal."+ext))
			if err != nil {
				t.Fatal(err)
			}
			if !got.ContextLogged || got.Role != "web_anon" || got.Endpoint != "/orders" || got.Failure != "" ||
				!reflect.DeepEqual(got.Binds, []string{"paid", "2", "0"}) || got.StatementMs == nil || got.QueryID == "" ||
				got.Prepared == nil || !*got.Prepared {
				t.Fatalf("capture = %+v", got)
			}
		})
		// The request hit the role's statement_timeout. Its parallel workers log
		// FATAL with the same statement under other PIDs; they are not requests.
		t.Run("timeout."+ext, func(t *testing.T) {
			got, err := ParsePostgRESTLog(fixture(t, "postgrest_timeout."+ext))
			if err != nil {
				t.Fatal(err)
			}
			if got.Failure != "canceling statement due to statement timeout" || got.StatementMs != nil ||
				got.Role != "slow_role" || got.Endpoint != "/big" || !reflect.DeepEqual(got.Binds, []string{"1", "0"}) {
				t.Fatalf("capture = %+v", got)
			}
			// The per-role setting's name is a parameter: set_config($2, $3, true).
			if got.Settings["statement_timeout"] != "20ms" || got.OtherSettings != 0 {
				t.Fatalf("role settings = %v (other %d)", got.Settings, got.OtherSettings)
			}
			for _, v := range []string{got.Method, got.Endpoint, got.Role, got.SearchPath, got.SQL, strings.Join(got.Binds, "|")} {
				if strings.Contains(v, "eyJ") || strings.Contains(strings.ToLower(v), "bearer") {
					t.Fatalf("credential material leaked: %q", v)
				}
			}
		})
	}
}

func TestParsePostgRESTLogFailedRequestSources(t *testing.T) {
	log := fixture(t, "postgrest_timeout.log")
	// Parameters only in the error CONTEXT (log_parameter_max_length_on_error).
	var noBindDetail []string
	lines := strings.Split(log, "\n")
	for i, l := range lines {
		if strings.Contains(l, "DETAIL:  Parameters: $1 = '1'") && i > 0 && strings.Contains(lines[i-1], " bind ") {
			continue
		}
		noBindDetail = append(noBindDetail, l)
	}
	got, err := ParsePostgRESTLog(strings.Join(noBindDetail, "\n"))
	if err != nil || !reflect.DeepEqual(got.Binds, []string{"1", "0"}) || got.Failure == "" {
		t.Fatalf("parameters from error CONTEXT: %+v, %v", got, err)
	}
	// Recovered only from the error CONTEXT line: the statement's name was
	// never logged, so whether it was prepared is genuinely unknown — not a
	// confident "no" (the CONTEXT line's "unnamed portal" names the portal,
	// not the statement, and says nothing about it).
	if got.Prepared != nil {
		t.Fatalf("prepared status recovered only from error CONTEXT must be unknown (nil), got %v", *got.Prepared)
	}
	// Neither bind DETAIL nor CONTEXT: refused with the setting to change.
	var none []string
	for _, l := range noBindDetail {
		if !strings.Contains(l, "CONTEXT:  unnamed portal") {
			none = append(none, l)
		}
	}
	if _, err := ParsePostgRESTLog(strings.Join(none, "\n")); err == nil || !strings.Contains(err.Error(), "log_parameter_max_length_on_error") {
		t.Fatalf("failed request without parameters must name the setting, got %v", err)
	}
	// log_statement=all logs the execute before it fails: one request, marked failed.
	var withExecute []string
	for _, l := range lines {
		if strings.Contains(l, "ERROR:  canceling statement") {
			withExecute = append(withExecute, strings.Replace(strings.Replace(l, "ERROR:  canceling statement due to statement timeout", "LOG:  execute 5: "+mainSQL(t, log), 1), "", "", 1))
			withExecute = append(withExecute, strings.Replace(l, "ERROR:  canceling statement due to statement timeout", "DETAIL:  Parameters: $1 = '1', $2 = '0'", 1))
		}
		withExecute = append(withExecute, l)
	}
	got, err = ParsePostgRESTLog(strings.Join(withExecute, "\n"))
	if err != nil || got.Failure == "" || len(got.Binds) != 2 {
		t.Fatalf("log_statement=all failed request: %+v, %v", got, err)
	}
	// This variant's own execute line was logged (log_statement=all), so its
	// statement name is known — unlike the CONTEXT-only case above.
	if got.Prepared == nil || !*got.Prepared {
		t.Fatalf("log_statement=all failed request should know it was prepared, got %+v", got)
	}
}

// A connection-pooled backend hands the same PID to many sequential,
// unrelated requests over its lifetime, and PostgREST reuses the identical
// SQL text for every call to the same endpoint shape. If an earlier,
// already-finished request's bind for that exact text is visible in the
// excerpt but its own outcome is not — the common shape of a hand-trimmed
// excerpt that starts a little early — a later, different request that fails
// with no bind of its own must not be given the earlier request's stale
// values just because the SQL text matches.
func TestParsePostgRESTLogDoesNotReuseAnEarlierRequestsStaleBind(t *testing.T) {
	fixtureLog := fixture(t, "postgrest_timeout.log")
	main := mainSQL(t, fixtureLog)
	var errLine, stmtLine string
	for _, l := range strings.Split(fixtureLog, "\n") {
		switch {
		case strings.Contains(l, "ERROR:  canceling statement"):
			errLine = l
		case strings.Contains(l, "STATEMENT:") && strings.Contains(l, "[106]"):
			stmtLine = l
		}
	}
	if errLine == "" || stmtLine == "" {
		t.Fatal("fixture missing its ERROR/STATEMENT lines")
	}

	log := strings.Join([]string{
		// An earlier, already-finished request's bind for the identical SQL
		// text (same prepared statement, reused). Its own execute/outcome is
		// not in this excerpt at all.
		`2026-10-08 17:38:14.100 UTC [106] 1111111111111111111 app=PostgREST 16.4 LOG:  duration: 1.085 ms  bind 5: ` + main,
		`2026-10-08 17:38:14.100 UTC [106] 1111111111111111111 app=PostgREST 16.4 DETAIL:  Parameters: $1 = '999', $2 = '888'`,
		// Some later, unrelated statement on the same backend completes —
		// exactly the signal that the bind above is no longer the most recent
		// pending one.
		`2026-10-08 17:38:15.300 UTC [106] 2222222222222222222 app=PostgREST 16.4 LOG:  duration: 0.006 ms  execute 6: SELECT 1`,
		// This request's own statement, with no bind/DETAIL/CONTEXT of its own.
		errLine,
		stmtLine,
	}, "\n")

	got, err := ParsePostgRESTLog(log)
	if err == nil {
		t.Fatalf("must fail closed, not silently return an earlier request's stale bind values: %+v", got)
	}
	if !strings.Contains(err.Error(), "parameters are not in the log") {
		t.Fatalf("expected a fail-closed 'parameters are not in the log' error, got: %v", err)
	}
}

// The same staleness risk as the lastBind test above, through the other
// mechanism finish() uses: matching "the last stmts entry for this backend"
// by SQL text alone. If an earlier, already-finished request on the same
// pooled backend succeeded (with its own duration/parameters logged, as
// log_statement=all does), and a later, unrelated request fails with the
// identical reused SQL text and no bind/DETAIL of its own, that earlier
// success record must not be silently relabeled as this failure.
func TestParsePostgRESTLogDoesNotMergeFailureIntoAnEarlierFinishedSuccess(t *testing.T) {
	fixtureLog := fixture(t, "postgrest_timeout.log")
	main := mainSQL(t, fixtureLog)
	var errLine, stmtLine string
	for _, l := range strings.Split(fixtureLog, "\n") {
		switch {
		case strings.Contains(l, "ERROR:  canceling statement"):
			errLine = l
		case strings.Contains(l, "STATEMENT:") && strings.Contains(l, "[106]"):
			stmtLine = l
		}
	}
	if errLine == "" || stmtLine == "" {
		t.Fatal("fixture missing its ERROR/STATEMENT lines")
	}

	log := strings.Join([]string{
		// An earlier request on the same backend, same reused SQL text,
		// finished successfully: log_statement=all logs its execute with a
		// known duration (so it is already a confirmed, closed success, not a
		// pending one), with its own DETAIL.
		`2026-10-08 17:38:14.100 UTC [106] 1111111111111111111 app=PostgREST 16.4 LOG:  duration: 5.000 ms  execute 5: ` + main,
		`2026-10-08 17:38:14.100 UTC [106] 1111111111111111111 app=PostgREST 16.4 DETAIL:  Parameters: $1 = '777', $2 = '666'`,
		// This request's own failure, with no bind/DETAIL/CONTEXT of its own.
		errLine,
		stmtLine,
	}, "\n")

	got, err := ParsePostgRESTLog(log)
	if err == nil {
		t.Fatalf("must fail closed, not relabel an earlier finished success as this failure: %+v", got)
	}
	// The earlier success is correctly kept as its own, separate request
	// instead of being merged away, so the "exactly one request" guard is what
	// catches this excerpt — a different, still fail-closed, error than the
	// lastBind case above, where the earlier request leaves no stmts entry of
	// its own to collide with.
	if !strings.Contains(err.Error(), "holds 2 PostgREST read requests") {
		t.Fatalf("expected a fail-closed error, got: %v", err)
	}
	if got != nil {
		t.Fatalf("must not return a result alongside an error: %+v", got)
	}
}

func mainSQL(t *testing.T, log string) string {
	t.Helper()
	for _, l := range strings.Split(log, "\n") {
		if _, after, ok := strings.Cut(l, "STATEMENT:  "); ok && strings.Contains(l, "[106]") {
			return after
		}
	}
	t.Fatal("no STATEMENT in fixture")
	return ""
}

// Production slow-query logging records only the slow statement and its
// parameters, not PostgREST's fast session-context statement.
func TestParsePostgRESTLogSlowQueryOnly(t *testing.T) {
	var slow []string
	lines := strings.Split(fixture(t, "postgrest_normal.log"), "\n")
	for i, l := range lines {
		if strings.Contains(l, "execute 9: WITH pgrst_source") || strings.Contains(l, " execute ") && strings.Contains(l, "pgrst_source") {
			slow = append(slow, l, lines[i+1])
		}
	}
	got, err := ParsePostgRESTLog(strings.Join(slow, "\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got.ContextLogged || got.Role != "" || got.Endpoint != "" || got.StatementMs == nil || !reflect.DeepEqual(got.Binds, []string{"paid", "2", "0"}) {
		t.Fatalf("slow-query-only capture = %+v", got)
	}
}

func TestParsePostgRESTLogRPC(t *testing.T) {
	got, err := ParsePostgRESTLog(fixture(t, "postgrest_rpc.log"))
	if err != nil || got.Endpoint != "/rpc/big_orders" || !reflect.DeepEqual(got.Binds, []string{"99", "2", "0"}) {
		t.Fatalf("RPC capture: %+v, %v", got, err)
	}
	if fns, err := CollectFunctions(got.SQL); err != nil || !reflect.DeepEqual(fns, []string{"api.big_orders"}) {
		t.Fatalf("functions = %v, %v", fns, err)
	}
	got, err = ParsePostgRESTLog(fixture(t, "postgrest_rpc_scalar.log"))
	if err != nil || got.Endpoint != "/rpc/optional_args" || len(got.Binds) != 0 {
		t.Fatalf("scalar RPC capture: %+v, %v", got, err)
	}
}

func TestParsePostgRESTLogRefusesSyslog(t *testing.T) {
	syslog := "Oct  8 17:38:15 db1 postgres[106]: [21-1] 2026-10-08 17:38:15.349 UTC [106] LOG:  duration: 1.0 ms  execute 5: WITH pgrst_source AS (\n" +
		"Oct  8 17:38:15 db1 postgres[106]: [21-2] #011SELECT 1)\n"
	if _, err := ParsePostgRESTLog(syslog); err == nil || !strings.Contains(err.Error(), "syslog") {
		t.Fatalf("syslog must be refused with a reason, got %v", err)
	}
}

// With db-prepared-statements=false PostgREST sends unnamed statements, which
// PostgreSQL plans for each execution's own values.
func TestParsePostgRESTLogUnnamedStatement(t *testing.T) {
	log := regexp.MustCompile(`(parse|bind|execute) \d+:`).ReplaceAllString(fixture(t, "postgrest_normal.log"), "$1 <unnamed>:")
	got, err := ParsePostgRESTLog(log)
	if err != nil || got.Prepared == nil || *got.Prepared {
		t.Fatalf("unnamed statement: %+v, %v", got, err)
	}
}

// Real PostgreSQL 18 + PostgREST 12.2.12 output for GET /rpc/big_orders?min_total=99.
// Unlike v13.0.4+ and v16.4, this PostgREST version passes RPC scalar
// arguments through one JSON payload parameter with no inline cast (see
// postgrestJSONPayloadCast); captured 2026-10-08 auditing this parser.
func TestParsePostgRESTLogRPCJSONPayloadNeedsExplicitCast(t *testing.T) {
	log := fixture(t, "postgrest_rpc_v12_json_payload.log")
	got, err := ParsePostgRESTLog(log)
	if err != nil || got.Endpoint != "/rpc/big_orders" || !reflect.DeepEqual(got.Binds, []string{`{"min_total":"99"}`, "2", "0"}) {
		t.Fatalf("v12.2.12 RPC capture: %+v, %v", got, err)
	}
	n, typ := postgrestJSONPayloadCast(got.SQL)
	if n != 1 || typ != "json" {
		t.Fatalf("postgrestJSONPayloadCast = %d, %q", n, typ)
	}
	lit, err := SubstituteCapturedParams(got.SQL, got.Binds)
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if !strings.Contains(lit, `E'{"min_total":"99"}'::json`) {
		t.Fatalf("the JSON payload bind must carry an explicit ::json cast: %s", lit)
	}
	if _, _, ok := parseSingleSelect(lit); !ok {
		t.Fatalf("substituted SQL does not parse as one SELECT: %s", lit)
	}
	// A plain table/view GET never matches the RPC shape.
	if n, _ := postgrestJSONPayloadCast(realPostgRESTLog(t)); n != 0 {
		t.Fatalf("ordinary GET falsely matched the RPC JSON-payload shape: bind %d", n)
	}
}
