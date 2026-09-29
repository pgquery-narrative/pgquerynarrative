package web

import (
	"strings"
	"testing"
)

func TestFormatInvestigationHTML_InvestigateHintIsNotPre(t *testing.T) {
	payload := map[string]any{
		"candidate_improvements": []any{
			map[string]any{
				"kind":              "sql_rewrite",
				"proposed_change":   "SELECT 1 FROM demo.sales WHERE date >= $1 AND date < $1 + INTERVAL '1 month'",
				"why_it_might_help": "Sargable range predicate.",
			},
			map[string]any{
				"kind":              "investigate_hint",
				"proposed_change":   "Investigate index or predicate shape for Seq Scan",
				"why_it_might_help": "Sequential scan on sales_2024_01",
			},
		},
	}
	html := formatInvestigationHTML(payload)

	if !strings.Contains(html, "<pre>SELECT 1 FROM demo.sales") {
		t.Errorf("real SQL rewrite should render in <pre>:\n%s", html)
	}
	if strings.Contains(html, "<pre>Investigate index or predicate shape") {
		t.Errorf("investigate_hint must not render as <pre> (looks like a statement):\n%s", html)
	}
	if !strings.Contains(html, "<p>Investigate index or predicate shape for Seq Scan</p>") {
		t.Errorf("investigate_hint should render as plain text:\n%s", html)
	}
}

// TestBuildExportHTML_StylesRenderAsCSS guards against html/template's contextual autoescaper
// silently dropping the <style> block: a plain string in that context isn't trusted as CSS, so
// it gets replaced with the sentinel "ZgotmplZ" instead of erroring, which previously shipped
// every exported HTML report with zero styling.
func TestBuildExportHTML_StylesRenderAsCSS(t *testing.T) {
	html := buildExportHTML("SELECT 1", "2026-01-01T00:00:00Z", "<p>body</p>")

	if strings.Contains(html, "ZgotmplZ") {
		t.Fatalf("exported HTML contains the html/template escaping-failure sentinel; Styles must be template.CSS:\n%s", html)
	}
	if !strings.Contains(html, "body.export-body {") {
		t.Errorf("exported HTML is missing the expected CSS rules:\n%s", html)
	}
}
