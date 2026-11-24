package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/internal/auth"
	"github.com/pgquerynarrative/pgquerynarrative/internal/config"
	"github.com/pgquerynarrative/pgquerynarrative/internal/db"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/service"
	"github.com/pgquerynarrative/pgquerynarrative/internal/story"
	"github.com/pgquerynarrative/pgquerynarrative/test/helpers"
)

// Bug: GetShared (and the equivalent Viewer-role path through Get) cleared
// only report.SQL. The same query text and captured PostgREST bind values are
// also embedded in report.Metrics.Investigation (SourceQuery,
// CandidateImprovements[].ProposedChange, Provenance.PostgREST.Binds) and in
// report.Narrative.Drivers (the bind-values evidence line) — generated
// alongside report.SQL from the same investigation, but never redacted
// alongside it. An unauthenticated share-link visitor, or an authenticated
// Viewer, could read the full query and its captured parameter values straight
// out of the narrative or metrics even with SQL exposure disabled.
//
// Two further leaks of the same kind, fixed alongside the above: a plan
// finding's Message/Evidence can embed the literal EXPLAIN filter clause
// verbatim (planFindingMessage/seqScanEvidence in queryrunner), which also
// reaches CandidateImprovements[].WhyItMightHelp and Narrative.Takeaways; and
// PostgREST.Failure is a verbatim captured production PostgreSQL error that
// is not a bind value at all, so it cannot be caught by filtering the Binds
// list.
//
// This generates a real investigation report (story.BuildInvestigationReport)
// with a distinctive source SQL marker, candidate SQL marker, captured bind
// secret, literal plan-filter secret and captured-failure secret, stores it
// exactly as Generate would, and checks every reader's serialized JSON
// response for every marker.
func TestSharedAndViewerReportsNeverLeakEmbeddedSQLOrBinds(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitForPostgres(t, ctx, connStr)
	helpers.RunMigrations(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const (
		topLevelSQL  = "SELECT * FROM demo.sales -- marker_top_8f3c1b"
		sourceSQL    = "SELECT secret_column_marker_9e21ab FROM demo.sales"
		candidateSQL = "SELECT secret_column_marker_9e21ab FROM demo.sales WHERE 1=1 -- candidate_marker_44b7cd"
		bindSecret   = "super-secret-bind-value-c7f1de"
		// A failed comparison's Notes carries PostgreSQL's own error message
		// verbatim (see compareResultEquivalence's Unverified branches), which
		// can echo a literal value straight out of the query — here standing
		// in for an echoed bind value a type-mismatch error quoted back.
		equivalenceErrorSecret = "echoed-literal-secret-5fa2"
		// A plan finding's Message/Evidence embeds the literal EXPLAIN filter
		// clause verbatim (see planFindingMessage/seqScanEvidence in
		// queryrunner) — shaped here exactly like that real output.
		filterSecret = "secret-filter-literal-d4e9"
		// PostgREST.Failure is a verbatim captured production PostgreSQL error;
		// unlike bindSecret, it is never in the Binds list at all.
		failureSecret = "duplicate key value violates unique constraint secret-detail-b2a7"
	)

	failure := "query execution failed: SQLSTATE 23505: " + failureSecret
	report, narrative := story.BuildInvestigationReport(
		"Investigation", sourceSQL, candidateSQL, "fp-leak-test", "default",
		story.StatInput{}, []story.PlanFindingInput{
			{
				Category:   "seq_scan",
				Confidence: "medium",
				Message: fmt.Sprintf(
					"Sequential scan on demo.sales (estimated cost 120.00) — filter: (status = '%s'::text) — consider a btree index on filtered or joined columns",
					filterSecret,
				),
				Evidence: []string{"Total Cost=120.00", "Plan Rows=500", "Filter=(status = '" + filterSecret + "'::text)"},
			},
		}, &story.ComparisonInput{
			ResultEquivalenceStatus: "Unverified",
			ResultEquivalenceNotes:  "sample run failed on candidate SQL: query execution failed: SQLSTATE 22P02: invalid input syntax for type integer: \"" + equivalenceErrorSecret + "\"",
		},
		story.InvestigationProvenance{PostgREST: &story.PostgRESTEvidence{
			Method: "GET", Endpoint: "/orders", Role: "web_anon",
			EndpointEquivalence: "not_claimed",
			Binds:               []string{bindSecret},
			Failure:             &failure,
		}},
	)
	narrativeJSON, err := json.Marshal(narrative)
	if err != nil {
		t.Fatal(err)
	}
	metricsJSON, err := json.Marshal(map[string]any{"investigation": report})
	if err != nil {
		t.Fatal(err)
	}

	// Sanity check: the fixture really does carry every marker somewhere,
	// before any redaction runs. The bind secret appears in both evidence
	// surfaces (BuildInvestigationReport sets NarrativeContent.Drivers to the
	// exact same lines as InvestigationReport.PostgreSQLEvidence); the source
	// and candidate SQL only appear in the investigation metrics, not the
	// narrative.
	if !strings.Contains(string(narrativeJSON), bindSecret) {
		t.Fatalf("fixture setup is broken: bind secret missing from narrative: %s", narrativeJSON)
	}
	if !strings.Contains(string(narrativeJSON), filterSecret) || !strings.Contains(string(narrativeJSON), failureSecret) {
		t.Fatalf("fixture setup is broken: filter/failure secret missing from narrative: %s", narrativeJSON)
	}
	for _, marker := range []string{sourceSQL, candidateSQL, bindSecret, equivalenceErrorSecret, filterSecret, failureSecret} {
		if !strings.Contains(string(metricsJSON), marker) {
			t.Fatalf("fixture setup is broken: %q missing from metrics: %s", marker, metricsJSON)
		}
	}

	var reportID string
	err = pool.QueryRow(ctx, `
		INSERT INTO app.reports (sql, narrative_md, narrative_json, metrics, llm_model, llm_provider, organization_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, topLevelSQL, "leak test", narrativeJSON, metricsJSON, "test", "integration", auth.DefaultOrganizationID).Scan(&reportID)
	if err != nil {
		t.Fatal(err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)
	appDB := db.NewOrgScoped(pool)
	svc := service.NewReportsService(pool, appDB, runner, noopLLM{}, config.MetricsConfig{})
	svc.SetShareLinksEnabled(true)

	adminCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "admin-1", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})
	viewerCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "viewer-1", OrgID: auth.DefaultOrganizationID, Role: auth.RoleViewer})

	assertNoMarkers := func(t *testing.T, label string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", label, err)
		}
		for _, marker := range []string{sourceSQL, candidateSQL, bindSecret, topLevelSQL, equivalenceErrorSecret, filterSecret, failureSecret} {
			if strings.Contains(string(b), marker) {
				t.Errorf("%s: leaked marker %q in response: %s", label, marker, b)
			}
		}
	}
	assertHasMarkers := func(t *testing.T, label string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", label, err)
		}
		for _, marker := range []string{sourceSQL, candidateSQL, bindSecret, equivalenceErrorSecret, filterSecret, failureSecret} {
			if !strings.Contains(string(b), marker) {
				t.Errorf("%s: expected marker %q present for authorized access, missing from: %s", label, marker, b)
			}
		}
	}

	t.Run("share link with SQL exposure disabled (default)", func(t *testing.T) {
		link, err := svc.CreateShare(adminCtx, &reports.CreateSharePayload{ReportID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.GetShared(ctx, &reports.GetSharedPayload{Token: link.Token})
		if err != nil {
			t.Fatal(err)
		}
		assertNoMarkers(t, "GetShared", got)
	})

	t.Run("authenticated viewer role", func(t *testing.T) {
		got, err := svc.Get(viewerCtx, &reports.GetPayload{ID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		assertNoMarkers(t, "Get as viewer", got)
	})

	t.Run("authenticated admin keeps permitted access", func(t *testing.T) {
		got, err := svc.Get(adminCtx, &reports.GetPayload{ID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		assertHasMarkers(t, "Get as admin", got)
	})

	t.Run("share link with SQL exposure explicitly enabled", func(t *testing.T) {
		svc.SetShareLinkExposeSQL(true)
		t.Cleanup(func() { svc.SetShareLinkExposeSQL(false) })
		link, err := svc.CreateShare(adminCtx, &reports.CreateSharePayload{ReportID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.GetShared(ctx, &reports.GetSharedPayload{Token: link.Token})
		if err != nil {
			t.Fatal(err)
		}
		assertHasMarkers(t, "GetShared with exposure enabled", got)
	})
}

// Bug: redactLiteralFilterText found the " — filter: " marker a plan
// finding's Message embedded, then treated the *next* " — " in the
// remainder as the boundary where planFindingMessage's own trailing
// guidance clause began, splicing everything from that point back into the
// supposedly redacted output. A SQL string literal can contain that exact
// sequence — nothing stops a WHERE predicate's value from doing so — so a
// secret quoted in a captured literal survived redaction.
//
// Unlike the sibling test above (which hand-shapes a PlanFindingInput),
// this drives the real EXPLAIN pipeline against a real table and a real
// literal containing the delimiter, through ReportsService.GetShared and
// Get as Viewer exactly as reported, and checks the complete serialized
// response.
func TestSharedAndViewerReportsNeverLeakEmbeddedDelimiterInFilterLiteral(t *testing.T) {
	ctx := context.Background()
	container := helpers.RunPostgresContainer(t, ctx)
	t.Cleanup(func() { _ = container.Terminate(ctx) })
	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	waitForPostgres(t, ctx, connStr)
	helpers.RunMigrations(t, connStr)

	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	const secret = "secret-filter-literal-d4e9"
	const literal = "prefix — " + secret
	if _, err := pool.Exec(ctx, `
		CREATE SCHEMA IF NOT EXISTS demo;
		CREATE TABLE IF NOT EXISTS demo.review_redaction (account text);
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO demo.review_redaction (account) VALUES ($1), ('other-row-1'), ('other-row-2');
	`, literal); err != nil {
		t.Fatal(err)
	}

	validator := queryrunner.NewValidator([]string{"demo"}, 10000)
	runner := queryrunner.NewRunner(pool, validator, 1000, 30*time.Second)

	sourceSQL := fmt.Sprintf("SELECT account FROM demo.review_redaction WHERE account = '%s'", strings.ReplaceAll(literal, "'", "''"))
	explainResult, err := runner.Explain(ctx, sourceSQL, false)
	if err != nil {
		t.Fatalf("EXPLAIN failed: %v", err)
	}
	if len(explainResult.Findings) == 0 {
		t.Fatal("fixture setup is broken: expected at least one plan finding (a seq scan on an unindexed filtered column)")
	}

	var findings []story.PlanFindingInput
	sawSecretInRealEvidence := false
	for _, f := range explainResult.Findings {
		findings = append(findings, story.PlanFindingInput{
			NodeType:   f.NodeType,
			Category:   f.Category,
			Confidence: f.Confidence,
			Message:    f.Message,
			Evidence:   f.Evidence,
		})
		for _, e := range f.Evidence {
			if strings.Contains(e, secret) {
				sawSecretInRealEvidence = true
			}
		}
	}
	if !sawSecretInRealEvidence {
		t.Fatalf("fixture setup is broken: expected the real EXPLAIN output to carry the secret in a finding's Evidence, got: %+v", explainResult.Findings)
	}

	report, narrative := story.BuildInvestigationReport(
		"Investigation", sourceSQL, "", "fp-real-explain", "default",
		story.StatInput{}, findings, nil, story.InvestigationProvenance{},
	)
	narrativeJSON, err := json.Marshal(narrative)
	if err != nil {
		t.Fatal(err)
	}
	metricsJSON, err := json.Marshal(map[string]any{"investigation": report})
	if err != nil {
		t.Fatal(err)
	}

	var reportID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO app.reports (sql, narrative_md, narrative_json, metrics, llm_model, llm_provider, organization_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, sourceSQL, "redaction test", narrativeJSON, metricsJSON, "test", "integration", auth.DefaultOrganizationID).Scan(&reportID); err != nil {
		t.Fatal(err)
	}

	appDB := db.NewOrgScoped(pool)
	svc := service.NewReportsService(pool, appDB, runner, noopLLM{}, config.MetricsConfig{})
	svc.SetShareLinksEnabled(true)

	adminCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "admin-1", OrgID: auth.DefaultOrganizationID, Role: auth.RoleAdmin})
	viewerCtx := auth.WithPrincipal(ctx, auth.Principal{UserID: "viewer-1", OrgID: auth.DefaultOrganizationID, Role: auth.RoleViewer})

	assertSecretAbsent := func(t *testing.T, label string, v any) {
		t.Helper()
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("%s: marshal: %v", label, err)
		}
		if strings.Contains(string(b), secret) {
			t.Errorf("%s: leaked secret in response: %s", label, b)
		}
	}

	t.Run("share link with SQL exposure disabled (default)", func(t *testing.T) {
		link, err := svc.CreateShare(adminCtx, &reports.CreateSharePayload{ReportID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.GetShared(ctx, &reports.GetSharedPayload{Token: link.Token})
		if err != nil {
			t.Fatal(err)
		}
		assertSecretAbsent(t, "GetShared", got)
	})

	t.Run("authenticated viewer role", func(t *testing.T) {
		got, err := svc.Get(viewerCtx, &reports.GetPayload{ID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		assertSecretAbsent(t, "Get as viewer", got)
	})

	// Positive control: authorized access is not over-redacted. The real
	// EXPLAIN finding's Evidence carries the secret for an admin.
	t.Run("authenticated admin keeps permitted access", func(t *testing.T) {
		got, err := svc.Get(adminCtx, &reports.GetPayload{ID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), secret) {
			t.Fatalf("admin access over-redacted: expected secret present, got: %s", b)
		}
	})

	// Positive control: an explicitly exposed share link is not over-redacted.
	t.Run("share link with SQL exposure explicitly enabled", func(t *testing.T) {
		svc.SetShareLinkExposeSQL(true)
		t.Cleanup(func() { svc.SetShareLinkExposeSQL(false) })
		link, err := svc.CreateShare(adminCtx, &reports.CreateSharePayload{ReportID: reportID})
		if err != nil {
			t.Fatal(err)
		}
		got, err := svc.GetShared(ctx, &reports.GetSharedPayload{Token: link.Token})
		if err != nil {
			t.Fatal(err)
		}
		b, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), secret) {
			t.Fatalf("exposed share link over-redacted: expected secret present, got: %s", b)
		}
	})
}
