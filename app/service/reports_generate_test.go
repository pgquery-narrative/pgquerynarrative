package service

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/charts"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
)

func TestTailPeriodPoints(t *testing.T) {
	pts := []metrics.PeriodPoint{{Label: "a"}, {Label: "b"}, {Label: "c"}, {Label: "d"}}
	if got := tailPeriodPoints(pts, 0); got != nil {
		t.Errorf("n=0 → nil, got %v", got)
	}
	if got := tailPeriodPoints(nil, 3); got != nil {
		t.Errorf("empty → nil, got %v", got)
	}
	if got := tailPeriodPoints(pts, 10); len(got) != 4 {
		t.Errorf("n>len → all, got %d", len(got))
	}
	got := tailPeriodPoints(pts, 2)
	if len(got) != 2 || got[0].Label != "c" || got[1].Label != "d" {
		t.Errorf("tail 2 → [c d], got %v", got)
	}
}

func TestNeighboringPoints(t *testing.T) {
	pts := []metrics.PeriodPoint{{Label: "0"}, {Label: "1"}, {Label: "2"}, {Label: "3"}, {Label: "4"}, {Label: "5"}}
	// centered window
	got := neighboringPoints(pts, "3", 1)
	if len(got) != 3 || got[0].Label != "2" || got[2].Label != "4" {
		t.Errorf("radius 1 around 3 → [2 3 4], got %v", got)
	}
	// clamps at the left edge
	got = neighboringPoints(pts, "0", 2)
	if len(got) != 3 || got[0].Label != "0" {
		t.Errorf("radius 2 around 0 → [0 1 2], got %v", got)
	}
	// unknown label → tail of 5
	got = neighboringPoints(pts, "nope", 1)
	if len(got) != 5 || got[0].Label != "1" {
		t.Errorf("unknown label → tail 5, got %v", got)
	}
}

func TestNormalizeOneSentence(t *testing.T) {
	cases := map[string]string{
		"  the trend is up  ":     "the trend is up.",
		"- bullet point":          "bullet point.",
		"1. numbered item":        "numbered item.",
		"already done!":           "already done!",
		"question?":               "question?",
		"first line\nsecond line": "first line.",
		"":                        "",
		"   ":                     "",
	}
	for in, want := range cases {
		if got := normalizeOneSentence(in); got != want {
			t.Errorf("normalizeOneSentence(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStrPtrIfNotEmpty(t *testing.T) {
	if strPtrIfNotEmpty("   ") != nil {
		t.Error("blank → nil")
	}
	if p := strPtrIfNotEmpty("x"); p == nil || *p != "x" {
		t.Errorf("non-blank → ptr, got %v", p)
	}
}

func TestFormatFloatPtr(t *testing.T) {
	if got := formatFloatPtr(nil); got != "—" {
		t.Errorf("nil → em dash, got %q", got)
	}
	v := 1234.5
	if got := formatFloatPtr(&v); got == "—" || got == "" {
		t.Errorf("value → formatted, got %q", got)
	}
}

func TestBuildFallbackNarrative(t *testing.T) {
	n := buildFallbackNarrative(42, []string{"add an index"})
	if n.Headline == "" || len(n.Takeaways) == 0 {
		t.Fatal("expected headline + takeaways")
	}
	if len(n.Recommendations) != 1 || n.Recommendations[0] != "add an index" {
		t.Errorf("perf suggestions should flow into recommendations, got %v", n.Recommendations)
	}
	if got := buildFallbackNarrative(0, nil); len(got.Recommendations) != 0 {
		t.Errorf("no suggestions → no recommendations, got %v", got.Recommendations)
	}
}

func TestParseChartRecommendation(t *testing.T) {
	ct, reason := parseChartRecommendation(`{"chart_type":"BAR","reason":"categorical"}`)
	if ct != "bar" || reason != "categorical" {
		t.Errorf("json path: got (%q,%q)", ct, reason)
	}
	ct, _ = parseChartRecommendation("I'd suggest a line chart here")
	if ct != "line" {
		t.Errorf("prose path: got %q", ct)
	}
	ct, _ = parseChartRecommendation("no idea")
	if ct != "" {
		t.Errorf("unrecognized → empty, got %q", ct)
	}
}

func TestNormalizeChartTypeAndLabel(t *testing.T) {
	if normalizeChartType("  Bar ") != "bar" {
		t.Error("trims + lowercases")
	}
	if normalizeChartType("scatter") != "" {
		t.Error("unknown → empty")
	}
	if chartTypeLabel("pie") != "Pie chart" {
		t.Error("pie label")
	}
	if chartTypeLabel("bogus") != "" {
		t.Error("unknown label → empty")
	}
}

func TestSuggestToReports(t *testing.T) {
	in := []charts.Suggestion{
		{ChartType: "line", Label: "Trend", Reason: "time series"},
		{ChartType: "bar", Label: "By category", Reason: "categorical"},
	}
	out := suggestToReports(in)
	if len(out) != 2 || out[0].ChartType != "line" || out[1].Label != "By category" {
		t.Fatalf("mapping mismatch: %+v", out)
	}
	if suggestToReports(nil) != nil {
		t.Error("nil in → nil out")
	}
}

func TestBuildChartRecommendationPrompt(t *testing.T) {
	p := buildChartRecommendationPrompt("Revenue by month",
		[]string{"month", "revenue"}, []string{"date", "numeric"}, 12,
		[]charts.Suggestion{{ChartType: "line", Reason: "time series"}})
	for _, want := range []string{"Revenue by month", "month", "revenue", "line"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt missing %q:\n%s", want, p)
		}
	}
}
