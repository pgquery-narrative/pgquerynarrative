package story

import (
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// InvestigationReport is a structured Query Investigation engineering artifact.
type InvestigationReport struct {
	ReportType            string                  `json:"report_type"`
	ExecutiveSummary      string                  `json:"executive_summary"`
	Impact                InvestigationImpact     `json:"impact"`
	SourceQuery           string                  `json:"source_query"`
	PostgreSQLEvidence    []string                `json:"postgresql_evidence"`
	PlanFindings          []InvestigationFinding  `json:"plan_findings"`
	CandidateImprovements []CandidateImprovement  `json:"candidate_improvements"`
	ControlledTestResults *ControlledTestResults  `json:"controlled_test_results,omitempty"`
	EquivalenceValidation *EquivalenceValidation  `json:"equivalence_validation,omitempty"`
	RisksAndTradeoffs     []string                `json:"risks_and_tradeoffs"`
	RecommendedNextAction string                  `json:"recommended_next_action"`
	Provenance            InvestigationProvenance `json:"provenance"`
}

// InvestigationImpact summarizes workload impact.
type InvestigationImpact struct {
	Severity    string   `json:"severity"`
	MeanTimeMs  *float64 `json:"mean_time_ms,omitempty"`
	TotalTimeMs *float64 `json:"total_time_ms,omitempty"`
	Calls       *int64   `json:"calls,omitempty"`
	Summary     string   `json:"summary"`
}

// InvestigationFinding is one evidence-backed plan observation.
type InvestigationFinding struct {
	Category    string   `json:"category"`
	Confidence  string   `json:"confidence"`
	Message     string   `json:"message"`
	Evidence    []string `json:"evidence,omitempty"`
	Investigate []string `json:"investigate,omitempty"`
}

// CandidateImprovement describes a proposed change requiring verification.
//
// ProposedChange is executable SQL only when Kind is CandidateKindSQLRewrite
// or CandidateKindIndexDDL — CandidateKindInvestigateHint entries carry a
// plain-English pointer ("Investigate index or predicate shape for Seq Scan"),
// not a statement. Consumers that render or export ProposedChange as SQL
// (report_export.go's Markdown/SQL builders) must check Kind first, or they
// end up fencing prose as ```sql, or worse, writing it into a .sql file with
// a trailing semicolon as if it were a real candidate.
type CandidateImprovement struct {
	Kind           string   `json:"kind"`
	ProposedChange string   `json:"proposed_change"`
	WhyItMightHelp string   `json:"why_it_might_help"`
	Confidence     string   `json:"confidence"`
	Verification   []string `json:"required_verification"`
}

const (
	CandidateKindSQLRewrite      = "sql_rewrite"
	CandidateKindIndexDDL        = "index_ddl"
	CandidateKindInvestigateHint = "investigate_hint"
)

// ControlledTestResults captures before/after comparison evidence.
type ControlledTestResults struct {
	Metrics  []ComparisonMetricRow `json:"metrics"`
	Improved []string              `json:"improved,omitempty"`
}

// ComparisonMetricRow is one before/after metric in a controlled test.
type ComparisonMetricRow struct {
	Evidence string `json:"evidence"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Change   string `json:"change"`
	// Caveat travels with the row into exported reports. A number that needs
	// qualifying on screen needs it just as much in a PDF handed to a reviewer.
	Caveat string `json:"caveat,omitempty"`
}

// EquivalenceValidation documents result equivalence checks.
type EquivalenceValidation struct {
	ChecksumEqual *bool  `json:"checksum_equal,omitempty"`
	Status        string `json:"status"`
	Notes         string `json:"notes"`
}

// InvestigationProvenance records environment and audit metadata.
type InvestigationProvenance struct {
	PostgreSQLVersion string `json:"postgresql_version,omitempty"`
	DatabaseIdentity  string `json:"database_identity,omitempty"`
	ConnectionType    string `json:"connection_type"`
	QueryFingerprint  string `json:"query_fingerprint,omitempty"`
	AnalysisTimestamp string `json:"analysis_timestamp"`
	ExplainAnalyze    bool   `json:"explain_analyze"`
	ResultsSampled    bool   `json:"results_sampled"`
	GeneratedBy       string `json:"generated_by,omitempty"`
	// PostgREST is set when the investigation was opened from a PostgREST request.
	PostgREST *PostgRESTEvidence `json:"postgrest,omitempty"`
}

// PostgRESTEvidence is the request provenance a report cites for an endpoint.
// Its timings are kept apart on purpose: HTTP wire time, PostgREST's transaction
// and the statement alone are different clocks, and the planner's total cost is
// an estimate, not a time.
type PostgRESTEvidence struct {
	Method                    string   `json:"method"`
	Endpoint                  string   `json:"endpoint"`
	Role                      string   `json:"role"`
	SearchPath                *string  `json:"search_path,omitempty"`
	QueryID                   *string  `json:"query_id,omitempty"`
	HTTPDurationMs            *float64 `json:"http_duration_ms,omitempty"`
	ServerTimingTransactionMs *float64 `json:"server_timing_transaction_ms,omitempty"`
	StatementDurationMs       *float64 `json:"statement_duration_ms,omitempty"`
	PlannerTotalCost          *float64 `json:"planner_total_cost,omitempty"`
	// Binds are the captured values every plan and comparison used.
	Binds []string `json:"binds,omitempty"`
	// SessionContextMissing: the log had no session context (slow-query-only
	// logging), so method, endpoint and role are unknown.
	SessionContextMissing bool              `json:"session_context_missing,omitempty"`
	Failure               *string           `json:"failure,omitempty"`
	ReplayRole            *string           `json:"replay_role,omitempty"`
	ReplaySearchPath      *string           `json:"replay_search_path,omitempty"`
	RequestSettings       map[string]string `json:"request_settings,omitempty"`
	PlanCacheNote         *string           `json:"plan_cache_note,omitempty"`
	ReplayLimitations     []string          `json:"replay_limitations"`
	// EndpointEquivalence is always "not_claimed" for a PostgREST replay: only
	// query-level comparisons are verified, not the endpoint's response.
	EndpointEquivalence string   `json:"endpoint_equivalence"`
	ApplyAt             []string `json:"apply_at"`
}

// PlanFindingInput is a simplified plan finding for report building.
type PlanFindingInput struct {
	NodeType   string
	Category   string
	Confidence string
	Message    string
	Evidence   []string
}

// ComparisonInput carries before/after comparison for report building.
type ComparisonInput struct {
	Metrics                 []ComparisonMetricRow
	Improved                []string
	ResultChecksumEqual     *bool
	ResultEquivalenceStatus string
	ResultEquivalenceNotes  string
	ResultBeforeRowCount    *int64
	ResultAfterRowCount     *int64
	ResultSampleRows        *int32
}

// StatInput carries pg_stat_statements context.
type StatInput struct {
	MeanTimeMs  *float64
	TotalTimeMs *float64
	Calls       *int64
}

// BuildInvestigationReport assembles an evidence-backed investigation report.
func BuildInvestigationReport(
	title, sql, candidateSQL, fingerprint, connectionID string,
	stat StatInput,
	findings []PlanFindingInput,
	comparison *ComparisonInput,
	provenance InvestigationProvenance,
) (*InvestigationReport, *NarrativeContent) {
	severity := impactSeverity(stat.MeanTimeMs)
	impactSummary := buildImpactSummary(stat)

	planFindings := make([]InvestigationFinding, 0, len(findings))
	pgEvidence := make([]string, 0, len(findings)+2)
	for _, f := range findings {
		pf := InvestigationFinding{
			Category:    f.Category,
			Confidence:  defaultConfidence(f.Confidence),
			Message:     f.Message,
			Evidence:    f.Evidence,
			Investigate: investigateHints(f.Category),
		}
		planFindings = append(planFindings, pf)
		if f.Message != "" {
			pgEvidence = append(pgEvidence, f.Message)
		}
	}
	if stat.MeanTimeMs != nil && *stat.MeanTimeMs > 1000 {
		pgEvidence = append(pgEvidence, fmt.Sprintf("pg_stat_statements mean execution time: %.1fms", *stat.MeanTimeMs))
	}

	candidates := buildCandidates(findings, candidateSQL, comparison)
	var controlled *ControlledTestResults
	var equiv *EquivalenceValidation
	if comparison != nil {
		controlled = &ControlledTestResults{Metrics: comparison.Metrics, Improved: comparison.Improved}
		status := normalizeEquivalenceStatus(comparison.ResultEquivalenceStatus)
		notes := comparison.ResultEquivalenceNotes
		if status == "" {
			// Legacy comparison with no status string — derive from the checksum,
			// which was only ever set true for a full-result compare.
			status = "Unverified"
			notes = "Result equivalence could not be verified (query error, timeout, or incomplete sample). Treat as unknown — not as a mismatch."
			if comparison.ResultChecksumEqual != nil {
				if *comparison.ResultChecksumEqual {
					status = "VerifiedEqual"
					notes = "Full result compared: every row matched between original and candidate."
				} else {
					status = "Different"
					notes = "Results differ — do not deploy the candidate without review."
				}
			}
		}
		if notes == "" {
			notes = "See result equivalence status."
		}
		equiv = &EquivalenceValidation{ChecksumEqual: comparison.ResultChecksumEqual, Status: status, Notes: notes}
	}

	risks := []string{
		"Index recommendations require review of write amplification and maintenance cost.",
		"Predicate rewrites must be validated against representative parameters.",
		"Plan improvements in development may not match production statistics.",
	}
	if candidateSQL == "" {
		risks = append(risks, "No candidate rewrite tested yet — treat findings as investigative, not prescriptive.")
	}
	if pg := provenance.PostgREST; pg != nil {
		header := []string{postgrestRequestLine(pg)}
		if pg.Failure != nil {
			header = append(header, fmt.Sprintf("The request statement failed in production: %s. PostgreSQL logs no duration for a failed statement.", *pg.Failure))
		}
		header = append(header, postgrestTimingLines(pg)...)
		if len(pg.Binds) > 0 {
			header = append(header, postgrestBindsLine(pg.Binds))
		}
		header = append(header, postgrestReplayLine(pg))
		if pg.PlanCacheNote != nil {
			header = append(header, "Plan cache: "+*pg.PlanCacheNote)
		}
		pgEvidence = append(header, pgEvidence...)
		pgEvidence = append(pgEvidence, pg.ApplyAt...)
		risks = append(risks, "Endpoint equivalence is not claimed: the PostgREST response was not replayed, so the comparison below is query-level.")
		risks = append(risks, pg.ReplayLimitations...)
	}

	nextAction := "Review plan evidence and test a candidate rewrite in a controlled environment."
	if comparison != nil && len(comparison.Improved) > 0 {
		status := normalizeEquivalenceStatus(comparison.ResultEquivalenceStatus)
		if status == "" && comparison.ResultChecksumEqual != nil {
			if *comparison.ResultChecksumEqual {
				status = "VerifiedEqual"
			} else {
				status = "Different"
			}
		}
		switch status {
		case "VerifiedEqual":
			nextAction = "Candidate shows measurable plan improvement and every row matched a full-result fingerprint — open an optimization ticket with this report attached. The fingerprint is order-independent and compares row text, so verify ordering and column types separately when they are part of the query's contract."
		case "SampleMatch":
			nextAction = "Candidate shows measurable plan improvement but full-result verification did not run — only a bounded sample matched. Treat as supporting evidence: re-check equivalence on a representative parameter set before deploying."
		case "Different":
			nextAction = "Plan improved but results differ — do not deploy; reconcile the rewrite before opening a change ticket."
		default: // Unverified, NotRequested, or empty
			nextAction = "Plan improved but result equivalence was not verified — do not treat as shippable until VerifiedEqual is confirmed."
		}
	}

	provenance.AnalysisTimestamp = time.Now().UTC().Format(time.RFC3339)
	provenance.ConnectionType = "read-only"
	if provenance.DatabaseIdentity == "" {
		provenance.DatabaseIdentity = connectionID
	}

	report := &InvestigationReport{
		ReportType:       "query_investigation",
		ExecutiveSummary: buildExecutiveSummary(title, severity, len(findings), comparison),
		Impact: InvestigationImpact{
			Severity:    severity,
			MeanTimeMs:  stat.MeanTimeMs,
			TotalTimeMs: stat.TotalTimeMs,
			Calls:       stat.Calls,
			Summary:     impactSummary,
		},
		SourceQuery:           sql,
		PostgreSQLEvidence:    pgEvidence,
		PlanFindings:          planFindings,
		CandidateImprovements: candidates,
		ControlledTestResults: controlled,
		EquivalenceValidation: equiv,
		RisksAndTradeoffs:     risks,
		RecommendedNextAction: nextAction,
		Provenance:            provenance,
	}

	narrative := &NarrativeContent{
		Headline:        fmt.Sprintf("Query Investigation: %s", title),
		Takeaways:       buildTakeaways(report),
		Drivers:         pgEvidence,
		Limitations:     risks,
		Recommendations: []string{nextAction},
	}
	return report, narrative
}

func postgrestRequestLine(pg *PostgRESTEvidence) string {
	if pg.SessionContextMissing {
		return "Captured PostgREST request statement (imported log evidence); its session context was not logged, so the HTTP method, endpoint and database role are unknown"
	}
	return fmt.Sprintf("Captured PostgREST request: %s %s, reported database role %q (imported log evidence)", pg.Method, pg.Endpoint, pg.Role)
}

// postgrestReplayLine states the context every replayed plan and comparison ran in.
func postgrestReplayLine(pg *PostgRESTEvidence) string {
	role := "the connection's own role"
	if pg.ReplayRole != nil && *pg.ReplayRole != "" {
		role = fmt.Sprintf("captured role %q (its JWT claims are not replayed)", *pg.ReplayRole)
	}
	path := "the connection's search_path"
	if pg.ReplaySearchPath != nil && *pg.ReplaySearchPath != "" {
		path = "search_path " + *pg.ReplaySearchPath
	}
	line := fmt.Sprintf("Replay context: %s, %s", role, path)
	keys := make([]string, 0, len(pg.RequestSettings))
	for k := range pg.RequestSettings {
		if k != "statement_timeout" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		pairs := make([]string, len(keys))
		for i, k := range keys {
			pairs[i] = k + "=" + pg.RequestSettings[k]
		}
		line += ", " + strings.Join(pairs, ", ")
	}
	return line
}

// postgrestBindsLine lists the captured values so the cited plans can be
// reproduced. Long values are shortened for display only.
func postgrestBindsLine(binds []string) string {
	parts := make([]string, len(binds))
	for i, b := range binds {
		shown := b
		if len(shown) > 120 {
			cut := 120
			for !utf8.RuneStart(shown[cut]) {
				cut--
			}
			shown = fmt.Sprintf("%s... (%d bytes total)", shown[:cut], len(b))
		}
		parts[i] = fmt.Sprintf("$%d = '%s'", i+1, strings.ReplaceAll(shown, "'", "''"))
	}
	return "Captured bind values (every plan and comparison used these): " + strings.Join(parts, ", ")
}

// postgrestTimingLines states each clock on its own line, with what it measures.
func postgrestTimingLines(pg *PostgRESTEvidence) []string {
	var out []string
	if pg.HTTPDurationMs != nil {
		out = append(out, fmt.Sprintf("HTTP request time (client-measured, includes network and PostgREST): %.1fms", *pg.HTTPDurationMs))
	}
	if pg.ServerTimingTransactionMs != nil {
		out = append(out, fmt.Sprintf("PostgREST transaction (Server-Timing): %.1fms, the whole database transaction including role setup", *pg.ServerTimingTransactionMs))
	}
	if pg.StatementDurationMs != nil {
		out = append(out, fmt.Sprintf("Request statement execute phase (PostgreSQL log): %.1fms; excludes parse and bind, where PostgreSQL plans a custom plan", *pg.StatementDurationMs))
	}
	if pg.PlannerTotalCost != nil {
		out = append(out, fmt.Sprintf("Planner estimate (EXPLAIN total cost): %.2f, a unitless estimate, not a time", *pg.PlannerTotalCost))
	}
	return out
}

func buildExecutiveSummary(title, severity string, findingCount int, comparison *ComparisonInput) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Investigation of %q identified %d execution-plan finding(s) with %s impact.", title, findingCount, severity))
	if comparison != nil && len(comparison.Improved) > 0 {
		b.WriteString(" A controlled candidate comparison shows improvements in: ")
		b.WriteString(strings.Join(comparison.Improved, ", "))
		b.WriteString(".")
	}
	return b.String()
}

func buildTakeaways(r *InvestigationReport) []string {
	out := []string{r.ExecutiveSummary}
	if r.Impact.Summary != "" {
		out = append(out, r.Impact.Summary)
	}
	for _, c := range r.CandidateImprovements {
		if c.WhyItMightHelp != "" {
			out = append(out, c.WhyItMightHelp)
		}
	}
	return out
}

func buildImpactSummary(stat StatInput) string {
	if stat.MeanTimeMs == nil && stat.TotalTimeMs == nil {
		return "Workload impact estimated from execution-plan evidence."
	}
	var parts []string
	if stat.MeanTimeMs != nil {
		parts = append(parts, fmt.Sprintf("mean %.1fms per call", *stat.MeanTimeMs))
	}
	if stat.TotalTimeMs != nil {
		parts = append(parts, fmt.Sprintf("total %.1fms accumulated", *stat.TotalTimeMs))
	}
	if stat.Calls != nil {
		parts = append(parts, fmt.Sprintf("%d calls observed", *stat.Calls))
	}
	return "Affected workload: " + strings.Join(parts, ", ") + "."
}

func impactSeverity(meanMs *float64) string {
	if meanMs == nil {
		return "medium"
	}
	switch {
	case *meanMs >= 5000:
		return "critical"
	case *meanMs >= 1000:
		return "high"
	case *meanMs >= 200:
		return "medium"
	default:
		return "low"
	}
}

func buildCandidates(findings []PlanFindingInput, candidateSQL string, comparison *ComparisonInput) []CandidateImprovement {
	var out []CandidateImprovement
	if candidateSQL != "" {
		why := "Candidate SQL rewrite tested via EXPLAIN comparison."
		confidence := "medium"
		if comparison != nil && len(comparison.Improved) > 0 {
			confidence = "high"
			why = "Controlled EXPLAIN comparison shows improvements: " + strings.Join(comparison.Improved, ", ")
		}
		out = append(out, CandidateImprovement{
			Kind:           CandidateKindSQLRewrite,
			ProposedChange: candidateSQL,
			WhyItMightHelp: why,
			Confidence:     confidence,
			Verification: []string{
				"Compare result equivalence (sampled rows)",
				"Test representative parameters",
				"Review write and maintenance cost before adding indexes",
			},
		})
	}
	out = append(out, groupedInvestigateHints(findings)...)
	return out
}

// maxInvestigateHints caps how many distinct "investigate this" pointers a
// report surfaces. A 50-partition table with the same problem on every
// partition produced 50 near-identical hints before this cap — one per
// occurrence group is enough for an engineer to act on.
const maxInvestigateHints = 5

// groupedInvestigateHints turns seq_scan / index_candidate findings into
// report pointers, one per distinct (Category, NodeType) group rather than
// one per finding — the same partitioned-table problem repeats once per
// partition, and a report should say that once with a count, not 50 times
// verbatim. Mirrors the rollup internal/queryrunner/diagnosis.go already does for
// the plan findings panel (rollupIncidental), reimplemented locally since
// this package takes a flat PlanFindingInput DTO and does not import
// queryrunner. Not to be confused with investigateHints(category string),
// the pre-existing per-category verification-hint helper below.
func groupedInvestigateHints(findings []PlanFindingInput) []CandidateImprovement {
	type group struct {
		nodeType    string
		message     string
		confidence  string
		occurrences int
	}
	var order []string
	groups := make(map[string]*group)
	for _, f := range findings {
		if f.Category != "seq_scan" && f.Category != "index_candidate" {
			continue
		}
		key := f.Category + "|" + f.NodeType
		g, ok := groups[key]
		if !ok {
			g = &group{nodeType: f.NodeType, message: f.Message, confidence: defaultConfidence(f.Confidence)}
			groups[key] = g
			order = append(order, key)
		}
		g.occurrences++
	}
	sort.SliceStable(order, func(i, j int) bool {
		return groups[order[i]].occurrences > groups[order[j]].occurrences
	})
	if len(order) > maxInvestigateHints {
		order = order[:maxInvestigateHints]
	}
	out := make([]CandidateImprovement, 0, len(order))
	for _, key := range order {
		g := groups[key]
		why := g.message
		if g.occurrences > 1 {
			why = fmt.Sprintf("%s (× %d occurrences)", why, g.occurrences)
		}
		out = append(out, CandidateImprovement{
			Kind:           CandidateKindInvestigateHint,
			ProposedChange: "Investigate index or predicate shape for " + g.nodeType,
			WhyItMightHelp: why,
			Confidence:     g.confidence,
			Verification:   []string{"EXPLAIN before/after", "Result equivalence", "Lock and storage review"},
		})
	}
	return out
}

func defaultConfidence(c string) string {
	if c == "" {
		return "medium"
	}
	return c
}

// normalizeEquivalenceStatus maps a stored equivalence status onto the current
// {VerifiedEqual, SampleMatch, Different, Unverified, NotRequested} vocabulary,
// tolerating the pre-PR literal "Equal" and any unexpected value. Empty stays
// empty so the caller's own defaulting runs.
func normalizeEquivalenceStatus(s string) string {
	switch s {
	case "VerifiedEqual", "SampleMatch", "Different", "Unverified", "NotRequested", "":
		return s
	case "Equal":
		return "VerifiedEqual"
	default:
		return "Unverified"
	}
}

// RedactSQL returns a copy of report with every field that carries raw SQL
// text or captured PostgREST bind values cleared — for a viewer (an
// unauthenticated share-link visitor, or a Viewer-role user) who must not see
// the underlying query. It clears SourceQuery, the ProposedChange of every
// CandidateImprovement whose Kind is executable SQL (an investigate_hint's
// plain-English pointer is not SQL and is left alone), Provenance.PostgREST's
// captured Binds, and the PostgreSQLEvidence line that states those bind
// values (see postgrestBindsLine — this is the exact text that line exists to
// show, so filtering by its own prefix removes the one line that leaks the
// values it quotes, not findings or timings). Findings, timings, the
// endpoint/role/method and replay limitations are untouched: they describe
// the investigation, not the query text, and a viewer denied the SQL can
// still use them.
func RedactSQL(report InvestigationReport) InvestigationReport {
	report.SourceQuery = ""
	if len(report.CandidateImprovements) > 0 {
		candidates := make([]CandidateImprovement, len(report.CandidateImprovements))
		for i, c := range report.CandidateImprovements {
			if c.Kind == CandidateKindSQLRewrite || c.Kind == CandidateKindIndexDDL {
				c.ProposedChange = ""
			}
			candidates[i] = c
		}
		report.CandidateImprovements = candidates
	}
	report.PostgreSQLEvidence = redactBindsEvidenceLine(report.PostgreSQLEvidence)
	if report.Provenance.PostgREST != nil {
		pg := *report.Provenance.PostgREST
		pg.Binds = nil
		report.Provenance.PostgREST = &pg
	}
	// EquivalenceValidation.Notes is free text built from whatever error a
	// failed comparison run raised (see compareResultEquivalence's Unverified
	// branches) — unlike every other status, which only ever reports counts
	// and booleans, an Unverified run's Notes can carry PostgreSQL's own
	// error message verbatim, and that message can echo back a literal value
	// from the query (e.g. a type-mismatch error quoting the bad value) —
	// for a PostgREST-captured investigation, quite possibly one of the
	// captured bind values this function exists to keep out of the report.
	// Every other status's Notes is a fixed template over counts/booleans
	// and is left alone.
	if report.EquivalenceValidation != nil && report.EquivalenceValidation.Status == "Unverified" {
		eq := *report.EquivalenceValidation
		eq.Notes = "Equivalence check could not be verified; details withheld because SQL access is restricted for this view."
		report.EquivalenceValidation = &eq
	}
	return report
}

// RedactSQLFromNarrativeDrivers strips the same bind-revealing line RedactSQL
// removes from PostgreSQLEvidence, from a NarrativeContent.Drivers slice
// built alongside an InvestigationReport — BuildInvestigationReport sets
// Drivers to the exact same lines (see pgEvidence).
func RedactSQLFromNarrativeDrivers(drivers []string) []string {
	return redactBindsEvidenceLine(drivers)
}

func redactBindsEvidenceLine(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if strings.HasPrefix(l, "Captured bind values (") {
			continue
		}
		out = append(out, l)
	}
	return out
}

func investigateHints(category string) []string {
	switch category {
	case "cardinality_misestimate", "stale_statistics":
		return []string{"Column statistics", "Correlated predicates", "Extended statistics", "Stale ANALYZE data"}
	case "seq_scan", "index_candidate":
		return []string{"Index definitions", "Predicate shape", "Selectivity estimates"}
	default:
		return []string{"Predicate selectivity", "Join order", "work_mem and spill behavior"}
	}
}
