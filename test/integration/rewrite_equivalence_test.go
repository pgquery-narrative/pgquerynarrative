package integration

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
	"github.com/pgquerynarrative/pgquerynarrative/app/db"
	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/app/service"
	"github.com/pgquerynarrative/pgquerynarrative/test/testhelpers"
)

// equivalenceEnv is a migrated Postgres with the service layer wired on top —
// the same objects the HTTP handlers use, so these tests exercise real planner
// output and real row comparison rather than a stubbed runner.
type equivalenceEnv struct {
	pool       *pgxpool.Pool
	runner     *queryrunner.Runner
	queriesSvc *service.QueriesService
	invSvc     *service.InvestigationsService
	ctx        context.Context
	reqCtx     context.Context
}

func newEquivalenceEnv(t *testing.T) *equivalenceEnv {
	t.Helper()
	ctx := context.Background()
	container := testhelpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	waitReady(t, ctx, connStr)

	migrationsPath, err := filepath.Abs("../../app/db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	m, err := migrate.New("file://"+migrationsPath, connStr)
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	validator := queryrunner.NewValidator([]string{"demo"}, 100000)
	runner := queryrunner.NewRunner(pool, validator, 50000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesSvc := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	var llmClient llm.Client = noopLLM{}
	reportsSvc := service.NewReportsService(pool, appDB, runner, llmClient, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)

	return &equivalenceEnv{
		pool:       pool,
		runner:     runner,
		queriesSvc: queriesSvc,
		invSvc:     invSvc,
		ctx:        ctx,
		reqCtx: auth.WithPrincipal(ctx, auth.Principal{
			UserID: "equivalence-test",
			OrgID:  auth.DefaultOrganizationID,
			Role:   auth.RoleAdmin,
		}),
	}
}

func (e *equivalenceEnv) exec(t *testing.T, sql string) {
	t.Helper()
	if _, err := e.pool.Exec(e.ctx, sql); err != nil {
		t.Fatalf("exec %.60q: %v", sql, err)
	}
}

func (e *equivalenceEnv) compare(t *testing.T, payload *queries.ComparePlansPayload) *queries.ComparePlansResult {
	t.Helper()
	res, err := e.queriesSvc.ComparePlans(e.reqCtx, payload)
	if err != nil {
		t.Fatalf("ComparePlans: %v", err)
	}
	return res
}

func equivalenceNotes(res *queries.ComparePlansResult) string {
	if res == nil || res.ResultEquivalenceNotes == nil {
		return ""
	}
	return *res.ResultEquivalenceNotes
}

func equivalenceStatus(res *queries.ComparePlansResult) string {
	if res == nil || res.ResultEquivalenceStatus == nil {
		return "<nil>"
	}
	return *res.ResultEquivalenceStatus
}

// TestRewriteEquivalence_MisalignedDateTruncIsNotRewritten pins the PR-1 fix:
// DATE_TRUNC('month', date) = '2025-01-15' is always false, so widening it to the
// whole of January would change the result set. The rewriter must decline.
func TestRewriteEquivalence_MisalignedDateTruncIsNotRewritten(t *testing.T) {
	env := newEquivalenceEnv(t)
	env.exec(t, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-03-31', INTERVAL '1 day') AS d
	`)
	env.exec(t, `ANALYZE demo.sales`)

	misaligned := `SELECT id FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-15'`
	aligned := `SELECT id FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-01'`

	t.Run("a misaligned constant produces no rewrite candidate", func(t *testing.T) {
		cands := queryrunner.SuggestRewrites(misaligned, nil)
		for _, c := range cands {
			if strings.Contains(strings.ToLower(c.SQL), "date >=") {
				t.Fatalf("misaligned DATE_TRUNC was widened into a range: %s", c.SQL)
			}
		}
	})

	t.Run("the misaligned predicate really is unsatisfiable", func(t *testing.T) {
		// If this ever returns rows, the premise of the guard has changed.
		var n int
		if err := env.pool.QueryRow(env.ctx,
			`SELECT count(*) FROM demo.sales WHERE DATE_TRUNC('month', date) = DATE '2025-01-15'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("expected the misaligned predicate to match nothing, got %d rows", n)
		}
	})

	t.Run("an aligned constant is still rewritten to a sargable range", func(t *testing.T) {
		cands := queryrunner.SuggestRewrites(aligned, nil)
		var rewritten string
		for _, c := range cands {
			if !strings.Contains(strings.ToLower(c.SQL), "date_trunc") {
				rewritten = c.SQL
				break
			}
		}
		if rewritten == "" {
			t.Fatalf("expected a sargable rewrite for an aligned literal, got %#v", cands)
		}
		res := env.compare(t, &queries.ComparePlansPayload{
			BeforeSQL: aligned, AfterSQL: rewritten, VerifyResults: true,
		})
		if got := equivalenceStatus(res); got != service.EquivalenceVerifiedEqual {
			t.Fatalf("aligned rewrite should be VerifiedEqual, got %q (%v)", got, equivalenceNotes(res))
		}
	})
}

// TestRewriteEquivalence_OrUnionKeepsNullRows pins the PR-1 NULL-safety fix: the
// OR→UNION rewrite negates the previous branch, and a plain NOT drops rows where
// the predicate is NULL. Real NULLs in the table are what make this observable.
func TestRewriteEquivalence_OrUnionKeepsNullRows(t *testing.T) {
	env := newEquivalenceEnv(t)
	env.exec(t, `
		CREATE TABLE demo.or_null (
			id     int primary key,
			region text,
			tier   text
		)
	`)
	env.exec(t, `
		INSERT INTO demo.or_null (id, region, tier) VALUES
			(1, 'EMEA', 'gold'),
			(2, 'APAC', 'gold'),
			(3, NULL,   'gold'),   -- region IS NULL: dropped by a naive NOT (region = 'EMEA')
			(4, 'EMEA', NULL),
			(5, NULL,   NULL),
			(6, 'AMER', 'silver')
	`)
	env.exec(t, `ANALYZE demo.or_null`)

	original := `SELECT id FROM demo.or_null WHERE region = 'EMEA' OR tier = 'gold'`

	cands := queryrunner.SuggestRewrites(original, nil)
	var union string
	for _, c := range cands {
		if strings.Contains(strings.ToUpper(c.SQL), "UNION") {
			union = c.SQL
			break
		}
	}
	if union == "" {
		// A skip here would let the NULL-safety guarantee rot silently.
		t.Fatalf("expected an OR→UNION rewrite candidate for %q, got %#v", original, cands)
	}
	t.Logf("union rewrite under test: %s", union)

	if strings.Contains(strings.ToUpper(union), "NOT (") && !strings.Contains(strings.ToUpper(union), "IS NOT TRUE") {
		t.Errorf("UNION branch uses a NULL-dropping NOT rather than IS NOT TRUE: %s", union)
	}

	res := env.compare(t, &queries.ComparePlansPayload{
		BeforeSQL: original, AfterSQL: union, VerifyResults: true,
	})
	if got := equivalenceStatus(res); got != service.EquivalenceVerifiedEqual {
		t.Fatalf("OR→UNION rewrite must return the same rows, got %q (%v)", got, equivalenceNotes(res))
	}
}

// TestComparePlans_VerifyResultsRequiresQueryPermission pins the PR-3 authorization
// split: planning a compare needs `explain`, but executing rows needs `query` too.
func TestComparePlans_VerifyResultsRequiresQueryPermission(t *testing.T) {
	env := newEquivalenceEnv(t)
	env.exec(t, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-01-31', INTERVAL '1 day') AS d
	`)
	env.exec(t, `ANALYZE demo.sales`)

	const sql = `SELECT id FROM demo.sales WHERE region = 'North'`

	// The bootstrap organisation ships seeded role grants that give analysts every
	// permission on the "default" connection (migration 000044), so a second
	// organisation is what actually models "may plan, may not run" — and it also
	// proves those seeded grants do not leak across tenants.
	var orgID string
	if err := env.pool.QueryRow(env.ctx, `
		INSERT INTO app.organizations (name, slug) VALUES ($1, $2) RETURNING id::text
	`, "Explain Only Tenant", "explain-only-tenant").Scan(&orgID); err != nil {
		t.Fatalf("create organization: %v", err)
	}

	authz := auth.NewConnectionAuthorizer(env.pool)
	if err := authz.AssignConnection(env.ctx, orgID, "default"); err != nil {
		t.Fatalf("assign connection: %v", err)
	}
	const analyst = "explain-only-analyst"
	if err := authz.GrantPermission(env.ctx, orgID, "default", analyst, map[string]bool{
		auth.ActionExplain: true,
	}); err != nil {
		t.Fatalf("grant: %v", err)
	}
	env.queriesSvc.SetAuthorizer(authz)

	analystCtx := auth.WithPrincipal(env.ctx, auth.Principal{
		UserID: analyst, OrgID: orgID, Role: auth.RoleAnalyst,
	})

	t.Run("planning only is permitted", func(t *testing.T) {
		res, err := env.queriesSvc.ComparePlans(analystCtx, &queries.ComparePlansPayload{
			BeforeSQL: sql, AfterSQL: sql, VerifyResults: false,
		})
		if err != nil {
			t.Fatalf("explain-only compare should be allowed: %v", err)
		}
		if got := equivalenceStatus(res); got != service.EquivalenceNotRequested {
			t.Errorf("status = %q, want NotRequested when verification was not asked for", got)
		}
	})

	t.Run("executing rows without the query permission is refused", func(t *testing.T) {
		_, err := env.queriesSvc.ComparePlans(analystCtx, &queries.ComparePlansPayload{
			BeforeSQL: sql, AfterSQL: sql, VerifyResults: true,
		})
		if err == nil {
			t.Fatal("expected a denial when verify_results is set without the query permission")
		}
		// goa's generated Error() is empty for design-declared errors, so assert on
		// the typed value the transport actually maps to 403.
		ve, ok := err.(*queries.ValidationError)
		if !ok {
			t.Fatalf("expected *queries.ValidationError, got %T: %v", err, err)
		}
		if ve.Code == nil || *ve.Code != "CONNECTION_FORBIDDEN" {
			t.Errorf("code = %v, want CONNECTION_FORBIDDEN (message %q)", ve.Code, ve.Message)
		}
	})
}

// TestComparePlans_HostileTimestampBindIsInert pins the PR-2 fix: a bind whose
// value only *starts* like a timestamp must never be spliced into the SQL as
// syntax. Two outcomes are safe — rejected at substitution, or quoted into an
// inert string literal — and both are asserted here. What must never happen is
// the payload taking effect as a predicate.
func TestComparePlans_HostileTimestampBindIsInert(t *testing.T) {
	env := newEquivalenceEnv(t)
	env.exec(t, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-01-31', INTERVAL '1 day') AS d
	`)
	env.exec(t, `ANALYZE demo.sales`)

	const parameterized = `SELECT id FROM demo.sales WHERE date >= $1`

	var benignCount, totalCount int
	if err := env.pool.QueryRow(env.ctx,
		`SELECT count(*) FROM demo.sales WHERE date >= DATE '2025-01-20'`).Scan(&benignCount); err != nil {
		t.Fatal(err)
	}
	if err := env.pool.QueryRow(env.ctx, `SELECT count(*) FROM demo.sales`).Scan(&totalCount); err != nil {
		t.Fatal(err)
	}
	if benignCount == 0 || benignCount >= totalCount {
		t.Fatalf("seed is not discriminating: benign=%d total=%d", benignCount, totalCount)
	}

	t.Run("a benign bind still works", func(t *testing.T) {
		res := env.compare(t, &queries.ComparePlansPayload{
			BeforeSQL: parameterized, AfterSQL: parameterized,
			VerifyResults: true, Binds: []string{"2025-01-20"},
		})
		if res.ResultBeforeRowCount == nil || int(*res.ResultBeforeRowCount) != benignCount {
			t.Fatalf("benign bind should select %d rows, got %v", benignCount, res.ResultBeforeRowCount)
		}
	})

	for _, hostile := range []string{
		`2025-01-01T00:00:00' OR '1'='1`,
		`2025-01-01T00:00:00'::timestamp OR 1=1 --`,
		`2025-01-01' UNION SELECT id FROM demo.sales --`,
		`2025-01-01'/* comment */OR TRUE--`,
		`2025-01-01'; DROP TABLE demo.sales; --`,
	} {
		t.Run(hostile, func(t *testing.T) {
			// First: what does substitution itself do with this value?
			substituted, subErr := queryrunner.SubstituteParams(parameterized, []string{hostile})
			if subErr == nil {
				t.Logf("substituted: %s", substituted)
				// Accepted means it must have been escaped into one literal. The
				// payload's own quote has to appear doubled, and none of its SQL
				// keywords may have escaped the quotes as syntax.
				if !strings.Contains(substituted, "''") {
					t.Errorf("hostile quote was not escaped: %s", substituted)
				}
				if strings.Count(substituted, "--") > 0 && !strings.Contains(substituted, "--'") &&
					!strings.Contains(substituted, "-- '") {
					t.Logf("note: comment marker present inside the literal: %s", substituted)
				}
			} else {
				t.Logf("rejected at substitution: %v", subErr)
			}

			// Second, and the assertion that matters: the payload must not take
			// effect. Either the compare errors, or it returns the narrow row set —
			// never every row, and never a dropped table.
			res, err := env.queriesSvc.ComparePlans(env.reqCtx, &queries.ComparePlansPayload{
				BeforeSQL:     parameterized,
				AfterSQL:      parameterized,
				VerifyResults: true,
				Binds:         []string{hostile},
			})
			if err == nil && res.ResultBeforeRowCount != nil && int(*res.ResultBeforeRowCount) == totalCount {
				t.Fatalf("hostile bind widened the result to every row (%d) — it executed as SQL", totalCount)
			}

			// The table must still be there, whatever happened above.
			var stillThere int
			if qerr := env.pool.QueryRow(env.ctx, `SELECT count(*) FROM demo.sales`).Scan(&stillThere); qerr != nil {
				t.Fatalf("demo.sales no longer queryable after bind %q: %v", hostile, qerr)
			}
			if stillThere != totalCount {
				t.Fatalf("row count changed from %d to %d after bind %q", totalCount, stillThere, hostile)
			}
		})
	}
}

// TestComparePlans_LargeResultIsSampleMatchNotVerifiedEqual pins the PR-3 honesty
// rule: past the sample cap the tool has compared a bounded sample, and must say so.
func TestComparePlans_LargeResultIsSampleMatchNotVerifiedEqual(t *testing.T) {
	env := newEquivalenceEnv(t)
	// Comfortably past the 1000-row equivalence sample cap.
	env.exec(t, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), DATE '2025-01-01' + (n % 90), 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(1, 2500) AS n
	`)
	env.exec(t, `ANALYZE demo.sales`)

	const sql = `SELECT id, date, region FROM demo.sales`

	t.Run("identical SQL over a large result is SampleMatch", func(t *testing.T) {
		res := env.compare(t, &queries.ComparePlansPayload{
			BeforeSQL: sql, AfterSQL: sql, VerifyResults: true,
		})
		if got := equivalenceStatus(res); got != service.EquivalenceSampleMatch {
			t.Fatalf("status = %q, want SampleMatch for a result past the cap (%v)", got, equivalenceNotes(res))
		}
	})

	t.Run("a different ORDER BY does not read as Different", func(t *testing.T) {
		// Equivalence is a multiset comparison: row order must not matter.
		res := env.compare(t, &queries.ComparePlansPayload{
			BeforeSQL:     sql + ` ORDER BY date ASC`,
			AfterSQL:      sql + ` ORDER BY date DESC`,
			VerifyResults: true,
		})
		if got := equivalenceStatus(res); got != service.EquivalenceSampleMatch && got != service.EquivalenceVerifiedEqual {
			t.Fatalf("reordering rows must not report a mismatch, got %q (%v)", got, equivalenceNotes(res))
		}
	})

	t.Run("a genuinely different result set is Different", func(t *testing.T) {
		res := env.compare(t, &queries.ComparePlansPayload{
			BeforeSQL:     sql,
			AfterSQL:      sql + ` WHERE region = 'North' AND date < DATE '2025-01-10'`,
			VerifyResults: true,
		})
		if got := equivalenceStatus(res); got != service.EquivalenceDifferent {
			t.Fatalf("status = %q, want Different (%v)", got, equivalenceNotes(res))
		}
	})
}

// TestComparePlans_VolatileQueryNeverReportsAsEqual pins the PR-3 honesty rule
// from the other side: two runs of random() cannot produce matching rows, so the
// tool must not claim they did. Reporting Different here is correct — the values
// really do differ — what would be wrong is VerifiedEqual or SampleMatch.
func TestComparePlans_VolatileQueryNeverReportsAsEqual(t *testing.T) {
	env := newEquivalenceEnv(t)
	env.exec(t, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), DATE '2025-01-01' + (n % 30), 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(1, 200) AS n
	`)
	env.exec(t, `ANALYZE demo.sales`)

	const volatileSQL = `SELECT id, random() AS r FROM demo.sales`
	res := env.compare(t, &queries.ComparePlansPayload{
		BeforeSQL: volatileSQL, AfterSQL: volatileSQL, VerifyResults: true,
	})
	// The counts match but the rows cannot: the honest answer is Different (the
	// values really do differ), never VerifiedEqual.
	got := equivalenceStatus(res)
	t.Logf("volatile query equivalence: %s (%v)", got, equivalenceNotes(res))
	if got == service.EquivalenceVerifiedEqual || got == service.EquivalenceSampleMatch {
		t.Fatalf("random() cannot produce matching rows twice; %q overstates what was verified (%v)",
			got, equivalenceNotes(res))
	}
}

// TestInvestigationCreate_IsEstimateOnly pins the PR-5 default: creating an
// investigation plans the query, it does not run it, so the evidence is estimated.
func TestInvestigationCreate_IsEstimateOnly(t *testing.T) {
	env := newEquivalenceEnv(t)
	env.exec(t, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), DATE '2025-01-01' + (n % 30), 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(1, 500) AS n
	`)
	env.exec(t, `ANALYZE demo.sales`)

	inv, err := env.invSvc.Create(env.reqCtx, &investigations.CreateInvestigationPayload{
		Title: "estimate-only default",
		SQL:   `SELECT product_category, SUM(total_amount) FROM demo.sales GROUP BY product_category`,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if inv.Explain == nil {
		t.Fatal("expected an EXPLAIN payload")
	}
	if inv.Explain.EvidenceMode != queryrunner.EvidenceEstimated {
		t.Errorf("evidence_mode = %q, want %q", inv.Explain.EvidenceMode, queryrunner.EvidenceEstimated)
	}
	if inv.Explain.ServerExecutionTimeMs != nil && *inv.Explain.ServerExecutionTimeMs > 0 {
		t.Errorf("an estimate-only EXPLAIN must not report a server execution time, got %v",
			*inv.Explain.ServerExecutionTimeMs)
	}
}
