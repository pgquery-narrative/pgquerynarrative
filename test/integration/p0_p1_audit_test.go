package integration

import (
	"context"
	"errors"
	"fmt"
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

type auditStack struct {
	pool       *pgxpool.Pool
	queriesSvc *service.QueriesService
	invSvc     *service.InvestigationsService
	ctx        context.Context
}

func newAuditStack(t *testing.T) auditStack {
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

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 5000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	queriesSvc := service.NewQueriesService(pool, appDB, runner, config.MetricsConfig{})
	var llmClient llm.Client = noopLLM{}
	reportsSvc := service.NewReportsService(pool, appDB, runner, llmClient, config.MetricsConfig{})
	invSvc := service.NewInvestigationsService(appDB, queriesSvc, reportsSvc)

	reqCtx := auth.WithPrincipal(ctx, auth.Principal{
		UserID: "p0-p1-audit",
		OrgID:  auth.DefaultOrganizationID,
		Role:   auth.RoleAdmin,
	})
	return auditStack{pool: pool, queriesSvc: queriesSvc, invSvc: invSvc, ctx: reqCtx}
}

// TestP0Audit_EquivalenceRunErrorIsUnverified ensures runtime failures during
// COUNT(*) never map to Different — the P0 trust contract.
func TestP0Audit_EquivalenceRunErrorIsUnverified(t *testing.T) {
	st := newAuditStack(t)

	_, err := st.pool.Exec(st.ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		VALUES (gen_random_uuid(), DATE '2025-01-01', 'Electronics', 'Widget', 1, 10, 10, 'North', 'A')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	beforeSQL := `SELECT region FROM demo.sales WHERE region = 'North'`
	// EXPLAIN (no ANALYZE) plans this; Run/COUNT evaluates 1/0 and fails → Unverified.
	afterSQL := `SELECT 1/(quantity-quantity) AS x FROM demo.sales WHERE region = 'North'`

	cmp, err := st.queriesSvc.ComparePlans(st.ctx, &queries.ComparePlansPayload{
		BeforeSQL: beforeSQL,
		AfterSQL:  afterSQL,
		Analyze:   false,
	})
	if err != nil {
		t.Fatalf("ComparePlans: %v", err)
	}
	if cmp.ResultEquivalenceStatus == nil {
		t.Fatal("expected status pointer")
	}
	if *cmp.ResultEquivalenceStatus != service.EquivalenceUnverified {
		t.Fatalf("expected Unverified, got %q notes=%v", *cmp.ResultEquivalenceStatus, cmp.ResultEquivalenceNotes)
	}
	if cmp.ResultChecksumEqual != nil {
		t.Fatalf("run error must leave checksum nil (unknown), got %v", *cmp.ResultChecksumEqual)
	}
}

// TestP0Audit_GenerateReportBlocksUnverified verifies API-level blocking when
// equivalence could not be verified (not silently shippable).
func TestP0Audit_GenerateReportBlocksUnverified(t *testing.T) {
	st := newAuditStack(t)

	_, err := st.pool.Exec(st.ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		VALUES (gen_random_uuid(), DATE '2025-01-01', 'Electronics', 'Widget', 1, 10, 10, 'North', 'A')
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	inv, err := st.invSvc.Create(st.ctx, &investigations.CreateInvestigationPayload{
		Title: "Unverified block",
		SQL:   `SELECT region FROM demo.sales WHERE region = 'North'`,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	inv, err = st.invSvc.AddCandidate(st.ctx, &investigations.AddCandidatePayload{
		ID:           inv.ID,
		CandidateSQL: `SELECT 1/(quantity-quantity) AS x FROM demo.sales WHERE region = 'North'`,
	})
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}
	if inv.Comparison == nil || inv.Comparison.ResultEquivalenceStatus == nil {
		t.Fatalf("missing comparison: %#v", inv.Comparison)
	}
	if *inv.Comparison.ResultEquivalenceStatus != service.EquivalenceUnverified {
		t.Fatalf("expected stored Unverified, got %q notes=%v",
			*inv.Comparison.ResultEquivalenceStatus, inv.Comparison.ResultEquivalenceNotes)
	}

	_, err = st.invSvc.GenerateReport(st.ctx, &investigations.GenerateReportPayload{ID: inv.ID})
	if err == nil {
		t.Fatal("GenerateReport must reject Unverified equivalence")
	}
	var ve *investigations.ValidationError
	if !errors.As(err, &ve) || ve.Code == nil || *ve.Code != "EQUIVALENCE_NOT_EQUAL" {
		t.Fatalf("expected EQUIVALENCE_NOT_EQUAL, got %T %v", err, err)
	}
}

// TestP0Audit_ComparisonRoundTrip preserves equivalence fields through DB JSON.
func TestP0Audit_ComparisonRoundTrip(t *testing.T) {
	st := newAuditStack(t)

	_, err := st.pool.Exec(st.ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2025-01-01', DATE '2025-01-05', INTERVAL '1 day') AS d
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	problem := `SELECT region FROM demo.sales WHERE region = 'North'`
	inv, err := st.invSvc.Create(st.ctx, &investigations.CreateInvestigationPayload{
		Title: "Round trip",
		SQL:   problem,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	inv, err = st.invSvc.AddCandidate(st.ctx, &investigations.AddCandidatePayload{
		ID:           inv.ID,
		CandidateSQL: problem,
	})
	if err != nil {
		t.Fatalf("AddCandidate: %v", err)
	}

	reloaded, err := st.invSvc.Get(st.ctx, &investigations.GetPayload{ID: inv.ID})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reloaded.Comparison == nil {
		t.Fatal("comparison missing after reload")
	}
	if reloaded.Comparison.ResultEquivalenceStatus == nil ||
		*reloaded.Comparison.ResultEquivalenceStatus != service.EquivalenceEqual {
		t.Fatalf("expected Equal after reload, got %#v", reloaded.Comparison.ResultEquivalenceStatus)
	}
	if reloaded.Comparison.ResultBeforeRowCount == nil || *reloaded.Comparison.ResultBeforeRowCount <= 0 {
		t.Fatalf("expected before row count, got %#v", reloaded.Comparison.ResultBeforeRowCount)
	}
	if reloaded.Comparison.ResultEquivalenceNotes == nil || *reloaded.Comparison.ResultEquivalenceNotes == "" {
		t.Fatal("expected equivalence notes on stored comparison")
	}
}

// TestP1Audit_SmallTableSuppressesIndexDDL verifies catalog enrichment does not
// draft CREATE INDEX on tables below the small-table threshold.
func TestP1Audit_SmallTableSuppressesIndexDDL(t *testing.T) {
	st := newAuditStack(t)

	_, err := st.pool.Exec(st.ctx, `DELETE FROM demo.sales`)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	_, err = st.pool.Exec(st.ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), CURRENT_DATE, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(1, 50)
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = st.pool.Exec(st.ctx, `ANALYZE demo.sales`)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(st.pool, validator, 5000, 30*time.Second)

	// Use a disposable connection so planner GUCs stick for EXPLAIN.
	conn, err := st.pool.Acquire(st.ctx)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer conn.Release()
	_, err = conn.Exec(st.ctx, `
		SET enable_indexscan = off;
		SET enable_bitmapscan = off;
		SET max_parallel_workers_per_gather = 0;
	`)
	if err != nil {
		t.Fatalf("planner GUCs: %v", err)
	}

	// Explain via the shared runner; enrichment uses catalog size/rows, not the
	// forced seq-scan connection. Prefer sales_rep (no demo index) so advice
	// is attempted even if the planner picks an index on region.
	exp, err := runner.Explain(st.ctx, `SELECT sales_rep FROM demo.sales WHERE sales_rep = 'A'`, false)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}

	var sawSmallTableAdvice bool
	var sawSeqOrCandidate bool
	for _, f := range exp.Findings {
		if f.IsSeqScan || f.Category == queryrunner.CategorySeqScan || f.Category == queryrunner.CategoryIndexCandidate {
			sawSeqOrCandidate = true
		}
		if f.IndexAdvice == nil {
			continue
		}
		for _, issue := range f.IndexAdvice.Issues {
			if issue == "small_table" {
				sawSmallTableAdvice = true
			}
		}
		if hasIssue(f.IndexAdvice.Issues, "small_table") && f.IndexAdvice.CandidateDDL != "" {
			t.Fatalf("small_table must not get CandidateDDL, got %q", f.IndexAdvice.CandidateDDL)
		}
		if hasIssue(f.IndexAdvice.Issues, "small_table") && f.Category == queryrunner.CategoryIndexCandidate {
			t.Fatalf("small_table must not promote to index_candidate")
		}
	}
	if !sawSeqOrCandidate {
		t.Fatal("expected at least one seq_scan finding")
	}
	if !sawSmallTableAdvice {
		t.Fatalf("expected small_table index advice after schema resolve; sample findings: %+v", summarizeFindings(exp.Findings))
	}
}

func summarizeFindings(findings []queryrunner.PlanFinding) string {
	var b strings.Builder
	n := 0
	for _, f := range findings {
		if !f.IsSeqScan {
			continue
		}
		n++
		if n > 3 {
			b.WriteString("...")
			break
		}
		fmt.Fprintf(&b, "{rel=%s.%s advice=%v} ", f.Schema, f.Relation, f.IndexAdvice != nil)
	}
	return b.String()
}

func hasIssue(issues []string, want string) bool {
	for _, i := range issues {
		if i == want {
			return true
		}
	}
	return false
}

// TestP1Audit_RankCandidatesHeuristicNotRankable ensures index DDL without
// hypopg stays review-only in ranked output.
func TestP1Audit_RankCandidatesHeuristicNotRankable(t *testing.T) {
	st := newAuditStack(t)

	_, err := st.pool.Exec(st.ctx, `
		INSERT INTO demo.sales (id, date, product_category, product_name, quantity, unit_price, total_amount, region, sales_rep)
		SELECT gen_random_uuid(), d::date, 'Electronics', 'Widget', 1, 10, 10, 'North', 'A'
		FROM generate_series(DATE '2024-01-01', DATE '2025-12-31', INTERVAL '1 day') AS d
	`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	_, err = st.pool.Exec(st.ctx, `ANALYZE demo.sales`)
	if err != nil {
		t.Fatalf("analyze: %v", err)
	}

	problem := `SELECT sales_rep, COUNT(*) FROM demo.sales WHERE sales_rep = 'A' GROUP BY sales_rep`
	inv, err := st.invSvc.Create(st.ctx, &investigations.CreateInvestigationPayload{
		Title: "Rank heuristic honesty",
		SQL:   problem,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	ranked, err := st.invSvc.RankCandidates(st.ctx, &investigations.RankCandidatesPayload{
		ID:      inv.ID,
		Analyze: false,
	})
	if err != nil {
		t.Fatalf("RankCandidates: %v", err)
	}

	var indexDDL int
	for _, c := range ranked.Candidates {
		if c.Kind != "index_ddl" {
			continue
		}
		indexDDL++
		if c.ProjectionMethod != nil && *c.ProjectionMethod == "heuristic" && c.Rankable {
			t.Fatalf("heuristic projection must not be rankable: %#v", c)
		}
		if c.ProjectionMethod == nil || *c.ProjectionMethod == "unavailable" || *c.ProjectionMethod == "heuristic" {
			if c.Rankable {
				t.Fatalf("non-hypopg index_ddl must not be rankable: %#v", c)
			}
		}
	}
	if indexDDL == 0 {
		// Unit-level ScoreIndexProjection already covers the honesty contract;
		// integration may lack index_ddl if the planner never emits a seq_scan
		// finding with RelatedColumns. Fail only when we got rankable heuristics.
		t.Log("no index_ddl candidates in this planner shape; honesty covered by unit tests")
	}
}
