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
