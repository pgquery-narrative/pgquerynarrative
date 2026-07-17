package web_test

import (
	"bytes"
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
