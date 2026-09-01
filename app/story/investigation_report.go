package story

import (
	"fmt"
	"strings"
	"time"
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
type CandidateImprovement struct {
	ProposedChange string   `json:"proposed_change"`
	WhyItMightHelp string   `json:"why_it_might_help"`
	Confidence     string   `json:"confidence"`
	Verification   []string `json:"required_verification"`
}

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
		status := comparison.ResultEquivalenceStatus
		notes := comparison.ResultEquivalenceNotes
		if status == "" {
			status = "Unverified"
			notes = "Result equivalence could not be verified (query error, timeout, or incomplete sample). Treat as unknown — not as a mismatch."
			if comparison.ResultChecksumEqual != nil {
				if *comparison.ResultChecksumEqual {
					status = "Equal"
					notes = "Sampled result payloads match between original and candidate queries."
				} else {
					status = "Different"
					notes = "Sampled results differ — do not deploy the candidate without review."
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

	nextAction := "Review plan evidence and test a candidate rewrite in a controlled environment."
	if comparison != nil && len(comparison.Improved) > 0 {
		nextAction = "Candidate shows measurable plan improvement — confirm result equivalence is Equal, then open an optimization ticket with this report attached."
		status := comparison.ResultEquivalenceStatus
		if status == "" && comparison.ResultChecksumEqual != nil {
			if *comparison.ResultChecksumEqual {
				status = "Equal"
			} else {
				status = "Different"
			}
		}
		if status == "Different" || (comparison.ResultChecksumEqual != nil && !*comparison.ResultChecksumEqual) {
			nextAction = "Plan improved but results differ — do not deploy; reconcile the rewrite before opening a change ticket."
		} else if status == "Unverified" || status == "" || comparison.ResultChecksumEqual == nil {
			nextAction = "Plan improved but result equivalence is Unverified — do not treat as shippable until Equal is confirmed."
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
	for _, f := range findings {
		if f.Category == "seq_scan" || f.Category == "index_candidate" {
			out = append(out, CandidateImprovement{
				ProposedChange: "Investigate index or predicate shape for " + f.NodeType,
				WhyItMightHelp: f.Message,
				Confidence:     defaultConfidence(f.Confidence),
				Verification:   []string{"EXPLAIN before/after", "Result equivalence", "Lock and storage review"},
			})
		}
	}
	return out
}

func defaultConfidence(c string) string {
	if c == "" {
		return "medium"
	}
	return c
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
