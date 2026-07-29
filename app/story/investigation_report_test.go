package story

import "testing"

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

func ptrInt64(v int64) *int64 { return &v }
