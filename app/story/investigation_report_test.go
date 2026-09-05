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
