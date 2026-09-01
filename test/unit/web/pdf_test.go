package web_test

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/web"
)

func TestBuildReportPDF_ProducesValidPDF(t *testing.T) {
	r := &reports.Report{
		ID:          "test-id",
		SQL:         "SELECT region, SUM(total_amount) FROM demo.sales GROUP BY region",
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "llama3.2",
		LlmProvider: "ollama",
		Narrative: &reports.NarrativeContent{
			Headline:        "Sales by region",
			Takeaways:       []string{"North leads revenue"},
			Recommendations: []string{"Review pricing in South"},
		},
		Metrics: &reports.MetricsData{},
	}
	var buf bytes.Buffer
	if err := web.BuildReportPDF(&buf, r); err != nil {
		t.Fatalf("BuildReportPDF: %v", err)
	}
	out := buf.Bytes()
	if len(out) < 100 {
		t.Fatalf("PDF too small: %d bytes", len(out))
	}
	if !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Fatalf("expected PDF header, got prefix %q", out[:min(20, len(out))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Investigation reports take a dedicated PDF path (writeInvestigationPDF). Exercise
// it with a non-SELECT candidate rewrite, collapsible partition findings, top-level
// PostgreSQL evidence, and extra Narrative.Recommendations so the review follow-ups
// stay covered.
func TestBuildReportPDF_InvestigationReport(t *testing.T) {
	planFindings := make([]any, 0, 6)
	for month := 1; month <= 6; month++ {
		planFindings = append(planFindings, map[string]any{
			"category": "partition_pruning",
			"message":  fmt.Sprintf("Seq scan on demo.sales_2023_%02d (estimated cost 120.0)", month),
		})
	}
	r := &reports.Report{
		ID:          "investigation-pdf-1",
		SQL:         "SELECT * FROM demo.sales",
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "evidence-template",
		LlmProvider: "pgquerynarrative",
		Narrative: &reports.NarrativeContent{
			Headline:        "Query Investigation: sales",
			Recommendations: []string{"Validate an index on customer_id in staging.", "Consider partition-wise aggregation."},
			Limitations:     []string{"EXPLAIN only; no controlled comparison run."},
		},
		Metrics: &reports.MetricsData{
			Investigation: map[string]any{
				"executive_summary":   "Investigation of \"sales\" identified 6 execution-plan finding(s) with high impact.",
				"postgresql_evidence": []any{"pg_stat_statements mean execution time: 2200.0ms"},
				"plan_findings":       planFindings,
				"candidate_improvements": []any{
					map[string]any{
						"proposed_change":   "DELETE FROM demo.sales WHERE created_at < now() - interval '2 years'",
						"why_it_might_help": "Shrinks the scanned partitions.",
					},
				},
				"recommended_next_action": "Validate an index on customer_id in staging.",
			},
		},
	}
	var buf bytes.Buffer
	if err := web.BuildReportPDF(&buf, r); err != nil {
		t.Fatalf("BuildReportPDF: %v", err)
	}
	if out := buf.Bytes(); len(out) < 100 || !bytes.HasPrefix(out, []byte("%PDF")) {
		t.Fatalf("expected a valid PDF, got %d bytes", len(out))
	}
}
