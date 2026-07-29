package web_test

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/web"
)

func TestFormatReportHTML_ChartSuggestions(t *testing.T) {
	r := &reports.Report{
		ID:      "test-id",
		SQL:     "SELECT 1",
		Metrics: &reports.MetricsData{},
		ChartSuggestions: []*reports.ChartSuggestion{
			{ChartType: "bar", Label: "Bar chart", Reason: "Category data"},
			{ChartType: "area", Label: "Area chart", Reason: "Time series"},
		},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "test",
		LlmProvider: "test",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Suggested charts") {
		t.Error("expected 'Suggested charts' section")
	}
	if !strings.Contains(html, "Bar chart") || !strings.Contains(html, "Area chart") {
		t.Error("expected chart labels in HTML")
	}
	if !strings.Contains(html, "chart-suggestions") {
		t.Error("expected chart-suggestions class")
	}
}

func TestFormatReportHTML_DataQuality(t *testing.T) {
	r := &reports.Report{
		ID:  "test-id",
		SQL: "SELECT 1",
		Metrics: &reports.MetricsData{
			DataQuality: map[string]*reports.ColumnQualityData{
				"col1": {NullCount: 0, DistinctCount: 5, TotalRows: 10, NullPct: 0},
			},
		},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "test",
		LlmProvider: "test",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Data quality") {
		t.Error("expected 'Data quality' section")
	}
	if !strings.Contains(html, "data-quality-table") {
		t.Error("expected data-quality-table class")
	}
	if !strings.Contains(html, "col1") {
		t.Error("expected column name in data quality")
	}
}

func TestFormatReportHTML_PerfSuggestions(t *testing.T) {
	r := &reports.Report{
		ID:  "test-id",
		SQL: "SELECT 1",
		Metrics: &reports.MetricsData{
			PerfSuggestions: []string{"Result set is large (limit applied)."},
		},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "test",
		LlmProvider: "test",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Performance suggestions") {
		t.Error("expected 'Performance suggestions' section")
	}
	if !strings.Contains(html, "limit applied") {
		t.Error("expected perf suggestion text")
	}
	if !strings.Contains(html, "perf-suggestions") {
		t.Error("expected perf-suggestions class")
	}
}

func TestFormatReportHTML_TimeSeriesVsPreviousPeriod(t *testing.T) {
	prev := 80.0
	changePct := 50.0
	r := &reports.Report{
		ID:  "test-id",
		SQL: "SELECT 1",
		Metrics: &reports.MetricsData{
			TimeSeries: map[string]*reports.TimeSeriesData{
				"revenue": {
					CurrentPeriod:    120,
					PreviousPeriod:   &prev,
					ChangePercentage: &changePct,
					Trend:            "up",
				},
			},
			PeriodCurrentLabel:  strPtr("2025-02"),
			PeriodPreviousLabel: strPtr("2025-01"),
		},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "test",
		LlmProvider: "test",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Vs previous period") {
		t.Error("expected 'Vs previous period' section")
	}
	if !strings.Contains(html, "period-comparison") {
		t.Error("expected period-comparison class")
	}
	if !strings.Contains(html, "revenue") {
		t.Error("expected measure name")
	}
}

func TestFormatReportHTML_NextPeriodForecast(t *testing.T) {
	forecast := 130.0
	r := &reports.Report{
		ID:  "test-id",
		SQL: "SELECT 1",
		Metrics: &reports.MetricsData{
			TimeSeries: map[string]*reports.TimeSeriesData{
				"revenue": {
					CurrentPeriod:      120,
					Trend:              "up",
					NextPeriodForecast: &forecast,
					TrendSummary:       &reports.TrendSummaryData{Summary: "Increasing trend.", Direction: "increasing"},
					Periods:            []*reports.PeriodPointData{{Label: "2025-01", Value: 100}, {Label: "2025-02", Value: 120}},
				},
			},
		},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "test",
		LlmProvider: "test",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Next period forecast") {
		t.Error("expected 'Next period forecast' in HTML")
	}
	if !strings.Contains(html, "next-period-forecast") {
		t.Error("expected next-period-forecast class")
	}
}

func TestFormatReportHTML_Narrative(t *testing.T) {
	r := &reports.Report{
		ID:      "test-id",
		SQL:     "SELECT 1",
		Metrics: &reports.MetricsData{},
		Narrative: &reports.NarrativeContent{
			Headline:  "Test Headline",
			Takeaways: []string{"Takeaway one.", "Takeaway two."},
		},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "test",
		LlmProvider: "test",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Test Headline") {
		t.Error("expected headline in HTML")
	}
	if !strings.Contains(html, "Takeaway one") || !strings.Contains(html, "Takeaway two") {
		t.Error("expected takeaways in HTML")
	}
	if !strings.Contains(html, "report-narrative") {
		t.Error("expected report-narrative class")
	}
}

func TestFormatReportHTML_ReportMeta(t *testing.T) {
	r := &reports.Report{
		ID:          "abc-123",
		SQL:         "SELECT 1",
		Metrics:     &reports.MetricsData{},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "ollama",
		LlmProvider: "ollama",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Report: ") || !strings.Contains(html, "abc-123") {
		t.Error("expected report id in meta")
	}
	if !strings.Contains(html, "Model:") || !strings.Contains(html, "ollama") {
		t.Error("expected model in meta")
	}
}

func TestFormatReportHTML_InvestigationReport(t *testing.T) {
	r := &reports.Report{
		ID:  "investigation-1",
		SQL: "SELECT * FROM demo.orders",
		Metrics: &reports.MetricsData{
			Investigation: map[string]any{
				"executive_summary": "Seq scan dominates a hot query.",
				"impact": map[string]any{
					"severity": "high",
					"summary":  "Affected workload: mean 2200ms per call, 40 calls observed.",
				},
				"postgresql_evidence": []any{"pg_stat_statements mean execution time: 2200.0ms"},
				"plan_findings": []any{
					map[string]any{
						"category": "scan",
						"message":  "Sequential scan on demo.orders.",
						"evidence": []any{"Rows Removed by Filter: 120000"},
					},
				},
				"recommended_next_action": "Validate an index on customer_id in staging.",
			},
		},
		CreatedAt:   "2025-01-01T00:00:00Z",
		LlmModel:    "evidence-template",
		LlmProvider: "pgquerynarrative",
	}
	html := web.FormatReportHTML(r)
	if !strings.Contains(html, "Executive summary") {
		t.Error("expected investigation executive summary section")
	}
	if !strings.Contains(html, "Seq scan dominates a hot query") {
		t.Error("expected investigation summary content")
	}
	if !strings.Contains(html, "Execution-plan findings") {
		t.Error("expected investigation findings section")
	}
	if !strings.Contains(html, "Validate an index on customer_id in staging") {
		t.Error("expected next action in investigation HTML")
	}
}

func strPtr(s string) *string { return &s }
