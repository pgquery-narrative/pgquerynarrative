package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
)

func TestPdfText(t *testing.T) {
	cases := map[string]string{
		"plain ascii":                 "plain ascii",
		"em—dash and “quotes”":        "em—dash and “quotes”", // preserved, no charset filter
		"greek σ cyrillic д":          "greek σ cyrillic д",
		"tab\tand\nnewline kept":      "tab\tand\nnewline kept",
		"bell \x07 and \x00 nul gone": "bell  and  nul gone",
		"replacement � char":          "replacement  char",
	}
	for in, want := range cases {
		if got := pdfText(in); got != want {
			t.Errorf("pdfText(%q) = %q, want %q", in, got, want)
		}
	}
}

func baseReport() *reports.Report {
	return &reports.Report{
		ID:           "r1",
		SQL:          "SELECT 1",
		ConnectionID: "default",
		CreatedAt:    "2026-09-02T00:00:00Z",
		LlmProvider:  "anthropic",
		LlmModel:     "claude-sonnet-5",
	}
}

func TestBuildReportPDF_Basic(t *testing.T) {
	r := baseReport()
	r.Narrative = &reports.NarrativeContent{
		Headline:        "Revenue rollup scans every partition",
		Takeaways:       []string{"All 36 monthly partitions are read", "Add a range predicate"},
		Recommendations: []string{"Rewrite DATE_TRUNC(month, ...) = $1 as a half-open range"},
	}
	var buf bytes.Buffer
	if err := BuildReportPDF(&buf, r); err != nil {
		t.Fatalf("BuildReportPDF: %v", err)
	}
	out := buf.Bytes()
	if !bytes.HasPrefix(out, []byte("%PDF-")) {
		t.Fatalf("output is not a PDF (%d bytes)", len(out))
	}
	if !bytes.Contains(out, []byte("%%EOF")) {
		t.Error("PDF has no EOF trailer")
	}
	if len(out) < 2000 {
		t.Errorf("PDF suspiciously small: %d bytes", len(out))
	}
}

// The Latin-1 renderer replaced every non-Latin-1 rune with '?', so a narrative
// full of unique Unicode glyphs produced the same bytes as one full of '?'.
// With the embedded Go fonts, those glyphs are subset into the file, so the
// Unicode variant must be materially larger than an ASCII-only one.
func TestBuildReportPDF_EmbedsUnicodeGlyphs(t *testing.T) {
	ascii := baseReport()
	ascii.Narrative = &reports.NarrativeContent{
		Headline:  "aaaaa",
		Takeaways: []string{strings.Repeat("a ", 40)},
	}
	uni := baseReport()
	uni.Narrative = &reports.NarrativeContent{
		Headline: "Ελληνικά — Кириллица — 日本語 — ①②③ → ✓",
		Takeaways: []string{
			"σ ± µ ° ¶ † ‡ • … ‰ ′ ″ ‹ › « » — – ‘ ’ “ ” € £ ¥ ← ↑ → ↓ ⇒ ∑ ∆ √ ≈ ≠ ≤ ≥",
			"Ζωντανά δεδομένα και πλήρης υποστήριξη Unicode στην αναφορά PDF.",
		},
	}

	var a, u bytes.Buffer
	if err := BuildReportPDF(&a, ascii); err != nil {
		t.Fatalf("ascii pdf: %v", err)
	}
	if err := BuildReportPDF(&u, uni); err != nil {
		t.Fatalf("unicode pdf: %v", err)
	}
	if u.Len() <= a.Len()+500 {
		t.Errorf("unicode PDF (%d B) not meaningfully larger than ascii PDF (%d B) — glyphs may not be embedding",
			u.Len(), a.Len())
	}
}

func TestBuildReportPDF_Investigation(t *testing.T) {
	r := baseReport()
	r.Narrative = &reports.NarrativeContent{Headline: "Investigation — DATE_TRUNC defeats pruning"}
	r.Metrics = &reports.MetricsData{
		Investigation: map[string]any{
			"executive_summary":       "The predicate wraps the partition key, so the planner can’t prune.",
			"recommended_next_action": "Ship the half-open range rewrite.",
			"plan_findings": []any{
				map[string]any{"category": "partitioning", "message": "No pruning on demo.sales — 36 partitions scanned"},
			},
		},
	}
	var buf bytes.Buffer
	if err := BuildReportPDF(&buf, r); err != nil {
		t.Fatalf("investigation pdf: %v", err)
	}
	if !bytes.HasPrefix(buf.Bytes(), []byte("%PDF-")) {
		t.Fatal("not a PDF")
	}
}
