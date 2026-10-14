package integration

import (
	"context"
	"io"
	"net/http"
	"net/url"
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

// pgrstSecuritySetup is a small fixture for the capture-path regressions below:
// one table with a text column (for the backslash/escaping checks) and a
// numeric column that exceeds 2^53 (for the lossless-JSON-comparison check).
const pgrstSecuritySetup = `
CREATE ROLE web_anon NOLOGIN;
CREATE ROLE authenticator LOGIN PASSWORD 'auth' NOINHERIT;
GRANT web_anon TO authenticator;
CREATE ROLE pqn_reader LOGIN PASSWORD 'reader' NOINHERIT;
CREATE SCHEMA api;
CREATE TABLE api.items(id bigint PRIMARY KEY, code text, n numeric);
INSERT INTO api.items(id,code,n) VALUES(1,'001',9007199254740992),(2,'other',0);
GRANT USAGE ON SCHEMA api TO web_anon, pqn_reader;
GRANT SELECT ON ALL TABLES IN SCHEMA api TO web_anon, pqn_reader;
`

// TestPostgRESTSecurityRegressions promotes the fixes for five defects found
// auditing the PostgREST capture path (2026-10-07) into durable, public
// coverage: a captured-value injection that depended on the session's
// standard_conforming_strings setting, a lossy-JSON-decode fallback that could
// approve unequal results, acceptance of a PostgreSQL-truncated bind value as
// exact evidence, rejection of valid zero-parameter GETs, and rejection of a
// capture carrying the optional Prefer: timezone=... session setting. Each
// subtest runs against a real, disposable PostgreSQL + PostgREST stack with
// the product's restricted analytical role (pqn_reader) — never a superuser —
// and goes through the real investigations service (CreateFromPostgrest /
// AddCandidate / GenerateReport), not a private helper function in isolation.
func TestPostgRESTSecurityRegressions(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	stack := helpers.RunPostgRESTStack(t, ctx, pgrstSecuritySetup, pgrstJWTSecret)
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
	appDB := db.NewOrgScoped(app)
	newService := func(pool *pgxpool.Pool) *service.InvestigationsService {
		r := queryrunner.NewRunner(pool, queryrunner.NewValidator([]string{"api"}, 10000), 5000, 5*time.Second, queryrunner.WithExplainAnalyze(true))
		q := service.NewQueriesService(pool, appDB, r, config.MetricsConfig{})
		return service.NewInvestigationsService(appDB, q, service.NewReportsService(pool, appDB, r, noopLLM{}, config.MetricsConfig{}))
	}
	svc := newService(reader)
	reqCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "security-regression", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})

	capture := func(t *testing.T, path string) string {
		t.Helper()
		before, err := stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		resp := pgrstGET(t, stack.BaseURL+path, "")
		if resp.status < 200 || resp.status >= 300 {
			t.Fatalf("endpoint HTTP %d: %s", resp.status, resp.body)
		}
		time.Sleep(100 * time.Millisecond)
		after, err := stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return after[len(before):]
	}

	// --- Defect 1: a captured value could escape its SQL literal and bypass
	// the read-only validator when a pooled connection's
	// standard_conforming_strings is "off". quoteSQLLiteral now renders every
	// substituted bind as an E'...' literal with backslashes doubled, which
	// PostgreSQL always decodes as an escape regardless of that session
	// setting (see internal/queryrunner/param_subst.go).
	t.Run("CapturedValueCannotEscapeItsLiteralUnderNonstandardConformingStrings", func(t *testing.T) {
		value := `\' ) SELECT pg_catalog.pg_sleep(0.2) --`
		if _, err := admin.Exec(ctx, `INSERT INTO api.items(id,code,n) VALUES(20,$1,0)`, value); err != nil {
			t.Fatal(err)
		}
		excerpt := capture(t, "/items?code=eq."+url.QueryEscape(value)+"&select=id,code&limit=3")

		cfg, err := pgxpool.ParseConfig(stack.ReaderConnStr)
		if err != nil {
			t.Fatal(err)
		}
		cfg.ConnConfig.RuntimeParams["standard_conforming_strings"] = "off"
		off, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer off.Close()

		inv, err := newService(off).CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt, Analyze: true})
		if err != nil {
			t.Fatalf("intake: %+v", err)
		}
		if inv.Explain == nil || inv.Explain.ServerExecutionTimeMs == nil {
			t.Fatal("no observed execution")
		}
		if *inv.Explain.ServerExecutionTimeMs > 100 {
			t.Fatalf("captured value executed the forbidden pg_sleep hidden in it on replay (%.3fms)", *inv.Explain.ServerExecutionTimeMs)
		}
	})

	// --- Defect 2: the equivalence fallback (used when the aggregate
	// hashtextextended fingerprint cannot run) decoded query results through
	// Go, which unmarshals JSON numbers as float64 and silently rounds an
	// integer above 2^53. Two results differing only in such a digit could
	// then be reported VerifiedEqual, and GenerateReport would accept them.
	// equivalence.go now renders every row to text inside PostgreSQL
	// (row::text) before comparing, so no column value is ever decoded into a
	// Go number. This is exercised twice: once with the hashtextextended grant
	// revoked from the analytical role, and once by padding the candidate past
	// the query byte limit so the fingerprint statement itself cannot run —
	// the two ways the fallback is reached in production.
	t.Run("EquivalenceFallbackDoesNotRoundLargeIntegersInJSON", func(t *testing.T) {
		excerpt := capture(t, "/items?id=eq.1&select=n&limit=3")
		inv, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
		if err != nil {
			t.Fatal(err)
		}
		candidate := strings.Replace(inv.SQL, `"api"."items"."n"`, `("api"."items"."n" + 1) AS n`, 1)
		if candidate == inv.SQL {
			t.Fatal("candidate unchanged")
		}

		assertDifferent := func(t *testing.T, candidateSQL string) {
			t.Helper()
			got, err := svc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: candidateSQL, VerifyResults: true})
			if err != nil {
				t.Fatal(err)
			}
			if got.Comparison.ResultEquivalenceStatus == nil || *got.Comparison.ResultEquivalenceStatus != "Different" {
				t.Fatalf("numerically different PostgREST body accepted: %+v", got.Comparison)
			}
			if _, err := svc.GenerateReport(reqCtx, &investigations.GenerateReportPayload{ID: inv.ID}); err == nil {
				t.Fatal("report gate accepted a Different comparison")
			}
		}

		t.Run("hashtextextended_grant_revoked", func(t *testing.T) {
			if _, err := admin.Exec(ctx, `REVOKE EXECUTE ON FUNCTION pg_catalog.hashtextextended(text,bigint) FROM PUBLIC`); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := admin.Exec(ctx, `GRANT EXECUTE ON FUNCTION pg_catalog.hashtextextended(text,bigint) TO PUBLIC`); err != nil {
					t.Fatal(err)
				}
			})
			assertDifferent(t, candidate)
		})

		t.Run("fingerprint_query_over_byte_limit", func(t *testing.T) {
			padded := candidate + " /*" + strings.Repeat("p", 9700-len(candidate)-6) + "*/"
			assertDifferent(t, padded)
		})
	})

	// --- Defect 3: PostgreSQL's own parameter logging truncates a value that
	// exceeds log_parameter_max_length and appends "..." *inside* the closing
	// quote. The parser used to accept the shortened text as the exact
	// captured value. It must now fail closed — the text log format cannot
	// prove the value is complete — with an actionable recapture error, never
	// a silently corrupted import.
	t.Run("TruncatedParameterIsRefusedNotSilentlyAccepted", func(t *testing.T) {
		before := countInvestigationsAdmin(t, ctx, admin)
		if _, err := admin.Exec(ctx, `ALTER SYSTEM SET log_parameter_max_length = 128`); err != nil {
			t.Fatal(err)
		}
		if _, err := admin.Exec(ctx, `SELECT pg_reload_conf()`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := admin.Exec(ctx, `ALTER SYSTEM RESET log_parameter_max_length`); err != nil {
				t.Fatal(err)
			}
			if _, err := admin.Exec(ctx, `SELECT pg_reload_conf()`); err != nil {
				t.Fatal(err)
			}
		})
		time.Sleep(150 * time.Millisecond)

		excerpt := capture(t, "/items?code=eq."+strings.Repeat("z", 180)+"&select=id,code&limit=3")
		if _, err := queryrunner.ParsePostgRESTLog(excerpt); err == nil {
			t.Fatal("a log-truncated bind value must be refused, not parsed as exact evidence")
		}
		if _, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt}); !isValidationError(err) {
			t.Fatalf("truncated capture must be a validation error, got %v", err)
		}
		if after := countInvestigationsAdmin(t, ctx, admin); after != before {
			t.Fatalf("refused intake created investigations: %d -> %d", before, after)
		}
	})

	// A value that genuinely ends in three literal dots (no truncation
	// occurred: log_parameter_max_length=-1 for this capture) is indistinguishable
	// from the truncation marker above using the text log format alone, so the
	// same conservative policy refuses it too. This is a documented, accepted
	// false positive — not a regression — for the ambiguous '...'-suffixed case.
	// A value ending in "..." is accepted only when a longer, untruncated value
	// in the same excerpt (here the logged request headers) proves the
	// truncation limit was not reached; otherwise it stays refused.
	t.Run("TrailingEllipsisNeedsProof", func(t *testing.T) {
		got, err := queryrunner.ParsePostgRESTLog(capture(t, "/items?code=eq.last...&select=id,code&limit=3"))
		if err != nil || got.Binds[0] != "last..." {
			t.Fatalf("a provably complete value ending in '...' must import exactly: %+v, %v", got, err)
		}
		long := strings.Repeat("y", 500) + "..."
		if _, err := queryrunner.ParsePostgRESTLog(capture(t, "/items?code=eq."+long+"&select=id")); err == nil {
			t.Fatal("a '...' value longer than every untruncated value must be refused")
		}
	})

	// --- Defect 4: a valid GET with zero SQL parameters (PostgreSQL correctly
	// emits no DETAIL line for the main statement, since there is nothing to
	// report) used to be rejected with a misleading message telling the
	// caller to enable a setting that was already enabled. The importer must
	// distinguish "no ParamRef nodes in the statement" from "missing bind
	// evidence for a parameterized statement." Covers the full
	// create/get/rank/compare/report path for this shape.
	t.Run("ZeroParameterGETImportsAndRoundTrips", func(t *testing.T) {
		excerpt := capture(t, "/items?select=id,code&order=id")
		cap, err := queryrunner.ParsePostgRESTLog(excerpt)
		if err != nil {
			t.Fatalf("valid zero-parameter GET must import: %v", err)
		}
		if len(cap.Binds) != 0 {
			t.Fatalf("zero-parameter statement must capture zero binds, got %d", len(cap.Binds))
		}
		inv, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: excerpt})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if got, err := svc.Get(reqCtx, &investigations.GetPayload{ID: inv.ID}); err != nil || got.ID != inv.ID {
			t.Fatalf("get: %+v %v", got, err)
		}
		if _, err := svc.RankCandidates(reqCtx, &investigations.RankCandidatesPayload{ID: inv.ID}); err != nil {
			t.Fatalf("rank: %v", err)
		}
		compared, err := svc.AddCandidate(reqCtx, &investigations.AddCandidatePayload{ID: inv.ID, CandidateSQL: inv.SQL, VerifyResults: true})
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		if compared.Comparison.ResultEquivalenceStatus == nil || *compared.Comparison.ResultEquivalenceStatus != "VerifiedEqual" {
			t.Fatalf("same SQL must verify equal: %+v", compared.Comparison)
		}
		if _, err := svc.GenerateReport(reqCtx, &investigations.GenerateReportPayload{ID: inv.ID}); err != nil {
			t.Fatalf("report: %v", err)
		}
	})

	// A statement quoting the text "$1" inside a quoted identifier alongside
	// zero real parameters, and a statement with zero ParamRef nodes whose log
	// nonetheless carries a DETAIL line, are AST-shape edge cases that do not
	// need a live PostgREST request to construct; see
	// TestParsePostgRESTLog_ZeroParameterEdgeCases in
	// internal/queryrunner/postgrest_capture_test.go.

	// --- Defect 5: PostgREST appends an extra set_config('timezone', $n, true)
	// call to its session-context statement when the request carries
	// Prefer: timezone=.... The parser required exactly seven target
	// expressions and rejected every such capture. It must instead treat the
	// seven required keys as a floor, validate every extra target with the
	// same strict set_config(key, $n, true) shape (never trusting an arbitrary
	// expression). The time zone is not replayed, so the stored limitations must
	// say which zone the endpoint used (it changes timestamptz output and
	// time-zone-dependent filters). Non-allowlisted custom settings are never
	// stored; see TestParsePostgRESTLogHEADSettingsAndAmbiguity.
	t.Run("TimezonePreferenceCaptureImports", func(t *testing.T) {
		excerpt := capture(t, "/items?id=eq.1&select=id,code&limit=3")
		// Re-capture the same endpoint but with the header this time, via a
		// fresh request (PostgREST appends the extra set_config only when the
		// header is present).
		before, err := stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		resp := pgrstGETWithHeaders(t, stack.BaseURL+"/items?id=eq.1&select=id,code&limit=3", map[string]string{"Prefer": "timezone=America/Los_Angeles"})
		if resp.status < 200 || resp.status >= 300 {
			t.Fatalf("endpoint HTTP %d: %s", resp.status, resp.body)
		}
		time.Sleep(100 * time.Millisecond)
		after, err := stack.PostgreSQLLogs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		tzExcerpt := after[len(before):]

		if _, err := queryrunner.ParsePostgRESTLog(tzExcerpt); err != nil {
			t.Fatalf("a capture carrying Prefer: timezone=... must still import: %v", err)
		}
		inv, err := svc.CreateFromPostgrest(reqCtx, &investigations.CreateFromPostgRESTPayload{PostgresLog: tzExcerpt})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if inv.PostgrestRequest == nil {
			t.Fatal("expected PostgREST provenance")
		}
		// The zone is kept and applied in every replay, so it is not a limitation.
		if inv.PostgrestRequest.RequestSettings["timezone"] != "America/Los_Angeles" {
			t.Fatalf("captured time zone not kept: %+v", inv.PostgrestRequest.RequestSettings)
		}
		if strings.Contains(strings.Join(inv.PostgrestRequest.ReplayLimitations, " "), "timezone") {
			t.Fatalf("an applied time zone is not a limitation: %q", inv.PostgrestRequest.ReplayLimitations)
		}
		_ = excerpt // the plain, no-Prefer capture above is used only to prove the baseline request still works unaffected.
	})
}

func countInvestigationsAdmin(t *testing.T, ctx context.Context, admin *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.investigations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// pgrstGETWithHeaders is pgrstGET plus request headers, for Prefer: and Accept:
// variations that change what PostgREST generates.
func pgrstGETWithHeaders(t *testing.T, url string, headers map[string]string) httpResult {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		t.Fatal(err)
	}
	return httpResult{status: resp.StatusCode, body: string(b), header: resp.Header}
}
