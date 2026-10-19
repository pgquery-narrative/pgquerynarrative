package story

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestPostgRESTReportKeepsEvidenceClocksAndLimitsSeparate(t *testing.T) {
	httpMs, txMs, statementMs, cost := 90.0, 60.0, 40.0, 1234.0
	qid, searchPath := "123", `"api", "public"`
	p := &PostgRESTEvidence{
		Method: "GET", Endpoint: "/orders", Role: "web_anon", QueryID: &qid, SearchPath: &searchPath,
		HTTPDurationMs: &httpMs, ServerTimingTransactionMs: &txMs, StatementDurationMs: &statementMs, PlannerTotalCost: &cost,
		EndpointEquivalence: "not_claimed", ReplayLimitations: []string{"Captured RLS claims were not replayed."},
	}
	r, narrative := BuildInvestigationReport("Captured read", "SELECT 1", "", "hash", "default", StatInput{}, nil, nil, InvestigationProvenance{PostgREST: p})
	joined := strings.Join(r.PostgreSQLEvidence, "\n")
	for _, want := range []string{"imported log evidence", "HTTP request time", "90.0ms", "PostgREST transaction", "60.0ms", "execute phase", "40.0ms", "1234.00", "not a time"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report omitted %q: %s", want, joined)
		}
	}
	if !strings.Contains(strings.Join(narrative.Limitations, " "), "Endpoint equivalence is not claimed") || r.EquivalenceValidation != nil {
		t.Fatal("a captured request without a comparison must not acquire an equivalence claim")
	}
	b, err := json.Marshal(r.Provenance)
	if err != nil || !strings.Contains(string(b), `"query_id":"123"`) || !strings.Contains(string(b), `"endpoint_equivalence":"not_claimed"`) {
		t.Fatalf("report provenance incomplete: %s, %v", b, err)
	}
}

func TestBuildInvestigationReport(t *testing.T) {
	mean := 8400.0
	stat := StatInput{MeanTimeMs: &mean, Calls: ptrInt64(1200)}
	findings := []PlanFindingInput{{
		Category:   "seq_scan",
		Confidence: "high",
		Message:    "Sequential scan on demo.sales",
	}}
	inv, narrative := BuildInvestigationReport(
		"Slow dashboard", "SELECT 1", "", "abc123", "default",
		stat, findings, nil,
		InvestigationProvenance{GeneratedBy: "test"},
	)
	if inv.ReportType != "query_investigation" {
		t.Fatalf("unexpected type %q", inv.ReportType)
	}
	if narrative.Headline == "" {
		t.Fatal("expected headline")
	}
	if inv.Impact.Severity != "critical" {
		t.Fatalf("expected critical severity, got %q", inv.Impact.Severity)
	}
}

// A 50-partition scan produces one seq_scan finding per partition — before
// the B-06 fix, that meant 50 near-identical "Investigate index or predicate
// shape for Seq Scan" candidate_improvements in the report. Pins that the
// report instead groups by (Category, NodeType), reports an occurrence count,
// and caps at maxInvestigateHints distinct groups.
func TestBuildInvestigationReport_DedupesRepeatedInvestigateHints(t *testing.T) {
	var findings []PlanFindingInput
	for i := 0; i < 50; i++ {
		findings = append(findings, PlanFindingInput{
			NodeType:   "Seq Scan",
			Category:   "seq_scan",
			Confidence: "high",
			Message:    "Sequential scan on a partition",
		})
	}
	// A distinct group beyond the cap must still be dropped, not silently merged
	// into the Seq Scan group.
	for i := 0; i < 2; i++ {
		findings = append(findings, PlanFindingInput{
			NodeType:   "Bitmap Heap Scan",
			Category:   "index_candidate",
			Confidence: "medium",
			Message:    "No covering index",
		})
	}
	report, _ := BuildInvestigationReport("t", "SELECT 1", "", "fp", "conn", StatInput{}, findings, nil, InvestigationProvenance{})

	if got := len(report.CandidateImprovements); got > maxInvestigateHints {
		t.Fatalf("expected at most %d candidate_improvements, got %d", maxInvestigateHints, got)
	}
	found := false
	for _, c := range report.CandidateImprovements {
		if c.Kind == CandidateKindInvestigateHint && strings.Contains(c.ProposedChange, "Seq Scan") {
			found = true
			if !strings.Contains(c.WhyItMightHelp, "× 50 occurrences") {
				t.Fatalf("expected the Seq Scan group to report 50 occurrences, got: %s", c.WhyItMightHelp)
			}
		}
	}
	if !found {
		t.Fatalf("expected one grouped Seq Scan hint, got: %#v", report.CandidateImprovements)
	}
}

// More than maxInvestigateHints distinct (Category, NodeType) groups must be capped, and the
// groups that survive must be the highest-occurrence ones, not the first-seen ones (CodeRabbit
// flagged this exact gap: the cap had no regression test).
func TestGroupedInvestigateHints_CapsAndKeepsHighestOccurrenceGroups(t *testing.T) {
	specs := []struct {
		nodeType string
		category string
		count    int
	}{
		{"A", "seq_scan", 3},
		{"B", "seq_scan", 10},
		{"C", "seq_scan", 1}, // lowest count: must be dropped by the cap
		{"D", "index_candidate", 7},
		{"E", "index_candidate", 2}, // second-lowest count: must be dropped by the cap
		{"F", "seq_scan", 5},
		{"G", "index_candidate", 4},
	}
	var findings []PlanFindingInput
	for _, s := range specs {
		for i := 0; i < s.count; i++ {
			findings = append(findings, PlanFindingInput{NodeType: s.nodeType, Category: s.category, Message: "m-" + s.nodeType})
		}
	}

	out := groupedInvestigateHints(findings)
	if len(out) != maxInvestigateHints {
		t.Fatalf("expected exactly %d groups (7 distinct groups > cap), got %d: %#v", maxInvestigateHints, len(out), out)
	}
	wantOrder := []string{"B", "D", "F", "G", "A"} // descending occurrence count: 10,7,5,4,3
	for i, want := range wantOrder {
		got := out[i].ProposedChange
		if got != "Investigate index or predicate shape for "+want {
			t.Fatalf("position %d: got %q, want NodeType %s (order must be by descending occurrence count)", i, got, want)
		}
	}
	for _, o := range out {
		if strings.HasSuffix(o.ProposedChange, " C") || strings.HasSuffix(o.ProposedChange, " E") {
			t.Fatalf("the two lowest-occurrence groups must be dropped by the cap, not kept: %#v", o)
		}
	}
}

func TestBuildInvestigationReport_UnverifiedBlocksShipAdvice(t *testing.T) {
	cmp := &ComparisonInput{
		Improved:                []string{"Partition pruning"},
		ResultEquivalenceStatus: "Unverified",
		ResultEquivalenceNotes:  "COUNT(*) matched but sample failed",
	}
	inv, _ := BuildInvestigationReport(
		"Slow", "SELECT 1", "SELECT 2", "fp", "default",
		StatInput{}, nil, cmp,
		InvestigationProvenance{},
	)
	if inv.EquivalenceValidation == nil || inv.EquivalenceValidation.Status != "Unverified" {
		t.Fatalf("expected Unverified: %+v", inv.EquivalenceValidation)
	}
	next := strings.ToLower(inv.RecommendedNextAction)
	if !strings.Contains(next, "not verified") || !strings.Contains(next, "not treat as shippable") {
		t.Fatalf("next action should block on an unverified equivalence: %q", inv.RecommendedNextAction)
	}
}

func TestBuildInvestigationReport_SampleMatchAllowsShipAdviceWithCaveat(t *testing.T) {
	cmp := &ComparisonInput{
		Improved:                []string{"Index scan"},
		ResultEquivalenceStatus: "SampleMatch",
		ResultEquivalenceNotes:  "COUNT(*) matched; deterministic 1000-row sample matched",
	}
	inv, _ := BuildInvestigationReport(
		"Slow", "SELECT 1", "SELECT 2", "fp", "default",
		StatInput{}, nil, cmp,
		InvestigationProvenance{},
	)
	if inv.EquivalenceValidation.Status != "SampleMatch" {
		t.Fatalf("status=%s", inv.EquivalenceValidation.Status)
	}
	next := strings.ToLower(inv.RecommendedNextAction)
	if strings.Contains(next, "not treat as shippable") || !strings.Contains(next, "representative parameter set") {
		t.Fatalf("SampleMatch should advise a re-check, not block: %q", inv.RecommendedNextAction)
	}
}

func TestBuildInvestigationReport_LegacyEqualIsTreatedAsVerifiedEqual(t *testing.T) {
	cmp := &ComparisonInput{
		Improved:                []string{"Partition pruning"},
		ResultEquivalenceStatus: "Equal", // pre-PR vocabulary
	}
	inv, _ := BuildInvestigationReport(
		"Slow", "SELECT 1", "SELECT 2", "fp", "default",
		StatInput{}, nil, cmp,
		InvestigationProvenance{},
	)
	if inv.EquivalenceValidation.Status != "VerifiedEqual" {
		t.Fatalf("legacy Equal should normalize to VerifiedEqual, got %q", inv.EquivalenceValidation.Status)
	}
	if strings.Contains(strings.ToLower(inv.RecommendedNextAction), "not treat as shippable") {
		t.Fatalf("legacy Equal must not block ship advice: %q", inv.RecommendedNextAction)
	}
}

func TestBuildInvestigationReport_VerifiedEqualAllowsShipAdvice(t *testing.T) {
	cmp := &ComparisonInput{
		Improved:                []string{"Partition pruning"},
		ResultEquivalenceStatus: "VerifiedEqual",
		ResultEquivalenceNotes:  "Full result compared",
	}
	inv, _ := BuildInvestigationReport(
		"Slow", "SELECT 1", "SELECT 2", "fp", "default",
		StatInput{}, nil, cmp,
		InvestigationProvenance{},
	)
	next := strings.ToLower(inv.RecommendedNextAction)
	if !strings.Contains(next, "open an optimization ticket") || strings.Contains(next, "not treat as shippable") {
		t.Fatalf("VerifiedEqual should advise opening a ticket: %q", inv.RecommendedNextAction)
	}
}

func TestBuildInvestigationReport_DifferentBlocksShipAdvice(t *testing.T) {
	f := false
	cmp := &ComparisonInput{
		Improved:                []string{"Lower cost"},
		ResultChecksumEqual:     &f,
		ResultEquivalenceStatus: "Different",
		ResultEquivalenceNotes:  "COUNT(*) differs",
	}
	inv, _ := BuildInvestigationReport(
		"Slow", "SELECT 1", "SELECT 2", "fp", "default",
		StatInput{}, nil, cmp,
		InvestigationProvenance{},
	)
	if inv.EquivalenceValidation.Status != "Different" {
		t.Fatalf("status=%s", inv.EquivalenceValidation.Status)
	}
	if !strings.Contains(strings.ToLower(inv.RecommendedNextAction), "differ") {
		t.Fatalf("next action=%q", inv.RecommendedNextAction)
	}
}

func ptrInt64(v int64) *int64 { return &v }

// The report must state the values its plans were measured with.
// A replayed role never carries the captured request's JWT claims (SET LOCAL
// ROLE only — see queryrunner.ReplayContext); the report must never claim
// otherwise, or a reader could wrongly conclude claim-keyed RLS (e.g.
// auth.uid()) was faithfully reproduced.
func TestPostgRESTReportNeverClaimsReplayedJWTClaims(t *testing.T) {
	role := "web_anon"
	p := &PostgRESTEvidence{
		Method: "GET", Endpoint: "/orders", Role: role, EndpointEquivalence: "not_claimed",
		ReplayRole: &role,
		ReplayLimitations: []string{
			"Request headers, cookies and JWT claims (request.jwt.claims) are not replayed even under the impersonated role, because the capture is unverified log text; RLS policies keyed on claims (for example auth.uid()) can return different rows than the real request.",
		},
	}
	r, _ := BuildInvestigationReport("Captured read", "SELECT 1", "", "hash", "default", StatInput{}, nil, nil, InvestigationProvenance{PostgREST: p})
	joined := strings.Join(r.PostgreSQLEvidence, "\n")
	if strings.Contains(joined, "with its JWT claims") {
		t.Fatalf("report falsely claims the replay carries JWT claims: %s", joined)
	}
	if !strings.Contains(joined, `captured role "web_anon"`) || !strings.Contains(joined, "not replayed") {
		t.Fatalf("report must name the replay role and state claims are not replayed: %s", joined)
	}
}

func TestPostgRESTReportStatesCapturedBinds(t *testing.T) {
	long := strings.Repeat("é", 100)
	p := &PostgRESTEvidence{Method: "GET", Endpoint: "/orders", Role: "web_anon", EndpointEquivalence: "not_claimed",
		Binds: []string{"O'Reilly", "3", long}}
	r, _ := BuildInvestigationReport("Captured read", "SELECT $1, $2, $3", "", "hash", "default", StatInput{}, nil, nil, InvestigationProvenance{PostgREST: p})
	joined := strings.Join(r.PostgreSQLEvidence, "\n")
	if !strings.Contains(joined, "$1 = 'O''Reilly', $2 = '3', $3 = '") || !strings.Contains(joined, "(200 bytes total)") {
		t.Fatalf("captured binds missing or misquoted: %s", joined)
	}
	if !utf8.ValidString(joined) {
		t.Fatal("shortened value split a multi-byte character")
	}
}

// RedactSQL is what a viewer denied raw SQL access must be shown instead of
// the full report — it has to remove every field that carries the query text
// or captured bind values while leaving the rest of the evidence (findings,
// timings, endpoint/role, replay limitations) intact.
func TestRedactSQLRemovesQueryTextAndBindsOnly(t *testing.T) {
	const secret = "super-secret-bind-value"
	p := &PostgRESTEvidence{
		Method: "GET", Endpoint: "/orders", Role: "web_anon", EndpointEquivalence: "not_claimed",
		Binds:             []string{secret},
		ReplayLimitations: []string{"Captured RLS claims were not replayed."},
	}
	report, narrative := BuildInvestigationReport(
		"Investigation", "SELECT secret_col FROM t", "SELECT secret_col FROM t WHERE 1=1", "fp1", "default",
		StatInput{}, []PlanFindingInput{{Category: "seq_scan", Message: "Sequential scan on demo.sales"}}, nil,
		InvestigationProvenance{PostgREST: p},
	)

	redacted := RedactSQL(*report)
	redactedNarrative := RedactSQLFromNarrativeDrivers(narrative.Drivers)

	b, _ := json.Marshal(redacted)
	nb, _ := json.Marshal(redactedNarrative)
	for _, blob := range []string{string(b), string(nb)} {
		if strings.Contains(blob, secret) {
			t.Fatalf("captured bind secret survived redaction: %s", blob)
		}
		if strings.Contains(blob, "SELECT secret_col FROM t") {
			t.Fatalf("source/candidate SQL survived redaction: %s", blob)
		}
	}
	if redacted.SourceQuery != "" {
		t.Fatalf("SourceQuery must be cleared, got %q", redacted.SourceQuery)
	}
	sawSQLRewrite := false
	for _, c := range redacted.CandidateImprovements {
		if c.Kind == CandidateKindSQLRewrite || c.Kind == CandidateKindIndexDDL {
			sawSQLRewrite = true
			if c.ProposedChange != "" {
				t.Fatalf("a sql_rewrite/index_ddl ProposedChange must be cleared, got %+v", c)
			}
		}
	}
	if !sawSQLRewrite {
		t.Fatal("test fixture did not produce a sql_rewrite candidate to redact")
	}
	if redacted.Provenance.PostgREST == nil || len(redacted.Provenance.PostgREST.Binds) != 0 {
		t.Fatalf("Provenance.PostgREST.Binds must be cleared, got %+v", redacted.Provenance.PostgREST)
	}

	// Non-sensitive evidence must survive: a finding message, the endpoint/role
	// line, and the replay limitations are not the query or its bind values.
	for _, want := range []string{"Sequential scan on demo.sales", "/orders", `"web_anon"`} {
		if !strings.Contains(string(b), want) {
			t.Fatalf("redaction over-removed non-sensitive evidence %q: %s", want, b)
		}
	}
	if !strings.Contains(string(nb), "/orders") {
		t.Fatalf("redaction over-removed non-sensitive narrative evidence: %s", nb)
	}
}

// Bug: EquivalenceValidation.Notes is free text built from whatever error a
// failed comparison raised (compareResultEquivalence's Unverified branches),
// which can echo a literal value straight out of a PostgreSQL error message
// (e.g. a type-mismatch error quoting the bad value) — for a
// PostgREST-captured investigation, quite possibly a captured bind value.
// RedactSQL did not touch this field at all.
func TestRedactSQLScrubsUnverifiedEquivalenceNotes(t *testing.T) {
	const secret = "super-secret-bind-value"
	report, _ := BuildInvestigationReport(
		"Investigation", "SELECT secret_col FROM t", "SELECT secret_col FROM t WHERE 1=1", "fp1", "default",
		StatInput{}, nil, &ComparisonInput{
			ResultEquivalenceStatus: "Unverified",
			ResultEquivalenceNotes:  fmt.Sprintf(`COUNT(*) matched (1 rows) but sample run failed on candidate SQL: query execution failed: SQLSTATE 22P02: invalid input syntax for type integer: %q. Unverified — not a mismatch.`, secret),
		},
		InvestigationProvenance{},
	)
	if report.EquivalenceValidation == nil || !strings.Contains(report.EquivalenceValidation.Notes, secret) {
		t.Fatalf("test fixture is broken: secret not present before redaction: %+v", report.EquivalenceValidation)
	}

	redacted := RedactSQL(*report)
	if redacted.EquivalenceValidation == nil {
		t.Fatal("EquivalenceValidation must survive redaction (status still needed), just with Notes scrubbed")
	}
	if strings.Contains(redacted.EquivalenceValidation.Notes, secret) {
		t.Fatalf("captured bind secret survived redaction in EquivalenceValidation.Notes: %q", redacted.EquivalenceValidation.Notes)
	}
	if redacted.EquivalenceValidation.Status != "Unverified" {
		t.Fatalf("Status must survive redaction, got %q", redacted.EquivalenceValidation.Status)
	}

	// A non-Unverified status's Notes is always a fixed template over counts
	// and booleans (see compareResultEquivalence) — never a raw error — and
	// must be left alone.
	matched, _ := BuildInvestigationReport(
		"Investigation", "SELECT secret_col FROM t", "", "fp1", "default",
		StatInput{}, nil, &ComparisonInput{
			ResultEquivalenceStatus: "VerifiedEqual",
			ResultEquivalenceNotes:  "Full result compared: every row matched between original and candidate.",
		},
		InvestigationProvenance{},
	)
	redactedMatched := RedactSQL(*matched)
	if redactedMatched.EquivalenceValidation == nil || redactedMatched.EquivalenceValidation.Notes == "" {
		t.Fatalf("a VerifiedEqual status's safe Notes must not be touched, got %+v", redactedMatched.EquivalenceValidation)
	}
}

// Bug: RedactSQL/RedactSQLFromNarrativeDrivers only ever cleared SourceQuery,
// a sql_rewrite/index_ddl ProposedChange, and the captured Binds list plus
// its one evidence line. Two other paths still carried a literal value out
// of the query into a restricted viewer's response:
//
//  1. A plan finding's Message/Evidence embeds the literal EXPLAIN filter
//     clause verbatim (see planFindingMessage/seqScanEvidence in
//     queryrunner), which flows into PlanFindings, PostgreSQLEvidence,
//     Narrative.Drivers (the same lines), and CandidateImprovements[].WhyItMightHelp
//     (groupedInvestigateHints copies a seq_scan/index_candidate finding's
//     Message verbatim) and from there into Narrative.Takeaways.
//  2. PostgREST.Failure is a verbatim captured production PostgreSQL error
//     (not a bind value — it is never in the Binds list) that is both
//     serialized directly under Provenance.PostgREST.Failure and quoted into
//     a PostgreSQLEvidence/Drivers line.
//
// This reproduces both with realistic report construction — a
// PlanFindingInput shaped exactly like queryrunner's real output, and a
// PostgREST.Failure populated the way investigations_postgrest.go populates
// it — and checks every redaction surface.
func TestRedactSQLScrubsLiteralFilterTextAndCapturedFailure(t *testing.T) {
	const (
		filterSecret  = "secret-filter-literal-7af2"
		failureSecret = "duplicate key value violates unique constraint secret-detail-c91a"
	)
	findings := []PlanFindingInput{
		{
			Category:   "seq_scan",
			Confidence: "medium",
			Message: fmt.Sprintf(
				"Sequential scan on demo.sales (estimated cost 120.00) — filter: (status = '%s'::text) — consider a btree index on filtered or joined columns",
				filterSecret,
			),
			Evidence: []string{
				"Total Cost=120.00",
				"Plan Rows=500",
				"Filter=(status = '" + filterSecret + "'::text)",
			},
		},
	}
	failure := fmt.Sprintf("query execution failed: SQLSTATE 23505: %s", failureSecret)
	p := &PostgRESTEvidence{
		Method: "GET", Endpoint: "/orders", Role: "web_anon", EndpointEquivalence: "not_claimed",
		Failure: &failure,
	}

	report, narrative := BuildInvestigationReport(
		"Investigation", "SELECT secret_col FROM t", "SELECT secret_col FROM t WHERE 1=1", "fp1", "default",
		StatInput{}, findings, nil, InvestigationProvenance{PostgREST: p},
	)

	// Sanity check: the fixture really does carry both secrets before any
	// redaction runs, across every surface the fix must cover.
	preRedaction, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{filterSecret, failureSecret} {
		if !strings.Contains(string(preRedaction), marker) {
			t.Fatalf("fixture setup is broken: %q missing from unredacted report: %s", marker, preRedaction)
		}
	}
	if !strings.Contains(strings.Join(narrative.Drivers, "\n"), filterSecret) {
		t.Fatal("fixture setup is broken: filter secret missing from narrative drivers")
	}
	if !strings.Contains(strings.Join(narrative.Drivers, "\n"), failureSecret) {
		t.Fatal("fixture setup is broken: failure secret missing from narrative drivers")
	}
	sawFilterInTakeaway := false
	for _, tk := range narrative.Takeaways {
		if strings.Contains(tk, filterSecret) {
			sawFilterInTakeaway = true
		}
	}
	if !sawFilterInTakeaway {
		t.Fatalf("fixture setup is broken: filter secret missing from narrative takeaways: %v", narrative.Takeaways)
	}

	redacted := RedactSQL(*report)
	redactedDrivers := RedactSQLFromNarrativeDrivers(narrative.Drivers)
	redactedTakeaways := RedactSQLFromNarrativeTakeaways(narrative.Takeaways)

	redactedJSON, err := json.Marshal(redacted)
	if err != nil {
		t.Fatal(err)
	}
	driversJSON, _ := json.Marshal(redactedDrivers)
	takeawaysJSON, _ := json.Marshal(redactedTakeaways)
	for _, blob := range []string{string(redactedJSON), string(driversJSON), string(takeawaysJSON)} {
		for _, marker := range []string{filterSecret, failureSecret} {
			if strings.Contains(blob, marker) {
				t.Fatalf("secret %q survived redaction: %s", marker, blob)
			}
		}
	}
	if redacted.Provenance.PostgREST == nil || redacted.Provenance.PostgREST.Failure != nil {
		t.Fatalf("Provenance.PostgREST.Failure must be cleared, got %+v", redacted.Provenance.PostgREST)
	}

	// Non-sensitive evidence must survive: the table name, the cost, and the
	// generic guidance clause are not literal query values.
	for _, want := range []string{"Sequential scan on demo.sales", "estimated cost 120.00", "consider a btree index", "filter withheld", "Total Cost=120.00", "Plan Rows=500"} {
		if !strings.Contains(string(redactedJSON), want) {
			t.Fatalf("redaction over-removed non-sensitive evidence %q: %s", want, redactedJSON)
		}
	}
}
