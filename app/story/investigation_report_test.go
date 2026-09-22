package story

import (
	"strings"
	"testing"
)

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
