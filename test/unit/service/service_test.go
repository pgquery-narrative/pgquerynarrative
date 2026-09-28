package service_test

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/internal/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/internal/queryrunner"
	"github.com/pgquerynarrative/pgquerynarrative/internal/service"
)

func TestBuildPerfSuggestions_None(t *testing.T) {
	r := &queryrunner.Result{ExecutionTimeMs: 100, RowCount: 500}
	got := service.BuildPerfSuggestions(r)
	if len(got) != 0 {
		t.Errorf("expected no suggestions, got %v", got)
	}
}

func TestBuildPerfSuggestions_LimitApplied(t *testing.T) {
	r := &queryrunner.Result{ExecutionTimeMs: 50, RowCount: 1000}
	got := service.BuildPerfSuggestions(r)
	if len(got) == 0 {
		t.Fatal("expected at least one suggestion for 1000 rows")
	}
	hasLimit := false
	for _, s := range got {
		if strings.Contains(s, "limit") || strings.Contains(s, "large") {
			hasLimit = true
			break
		}
	}
	if !hasLimit {
		t.Errorf("expected suggestion about limit/large result set, got %v", got)
	}
}

func TestBuildPerfSuggestions_SlowQuery(t *testing.T) {
	r := &queryrunner.Result{ExecutionTimeMs: 2500, RowCount: 10}
	got := service.BuildPerfSuggestions(r)
	if len(got) == 0 {
		t.Fatal("expected at least one suggestion for slow query")
	}
	hasSlow := false
	for _, s := range got {
		if strings.Contains(s, "2s") || strings.Contains(s, "filters") || strings.Contains(s, "indexes") {
			hasSlow = true
			break
		}
	}
	if !hasSlow {
		t.Errorf("expected suggestion about slow query, got %v", got)
	}
}

func TestBuildPerfSuggestions_Both(t *testing.T) {
	r := &queryrunner.Result{ExecutionTimeMs: 3000, RowCount: 1000}
	got := service.BuildPerfSuggestions(r)
	if len(got) < 2 {
		t.Errorf("expected at least 2 suggestions, got %v", got)
	}
}

func TestConvertMetrics_AggregatesWithStdDev(t *testing.T) {
	m := &metrics.Metrics{
		Aggregates: map[string]metrics.Aggregates{
			"total": {Sum: floatPtr(100), Avg: floatPtr(25), Count: 4, StdDev: floatPtr(5.2)},
		},
		DataQuality: map[string]metrics.ColumnQuality{},
	}
	out := service.ConvertMetrics(m)
	if out.Aggregates["total"].StdDev == nil {
		t.Error("expected std_dev in aggregate")
	}
	if *out.Aggregates["total"].StdDev != 5.2 {
		t.Errorf("std_dev = %v, want 5.2", *out.Aggregates["total"].StdDev)
	}
}

func TestConvertMetrics_DataQuality(t *testing.T) {
	m := &metrics.Metrics{
		Aggregates: map[string]metrics.Aggregates{},
		DataQuality: map[string]metrics.ColumnQuality{
			"col1": {NullCount: 2, DistinctCount: 5, TotalRows: 10, NullPct: 20.0},
		},
	}
	out := service.ConvertMetrics(m)
	if out.DataQuality["col1"] == nil {
		t.Fatal("expected data_quality for col1")
	}
	if out.DataQuality["col1"].NullCount != 2 || out.DataQuality["col1"].DistinctCount != 5 ||
		out.DataQuality["col1"].TotalRows != 10 || out.DataQuality["col1"].NullPct != 20.0 {
		t.Errorf("data_quality col1 = %+v", out.DataQuality["col1"])
	}
}

func TestConvertMetrics_PerfSuggestions(t *testing.T) {
	m := &metrics.Metrics{
		Aggregates:      map[string]metrics.Aggregates{},
		DataQuality:     map[string]metrics.ColumnQuality{},
		PerfSuggestions: []string{"Query took over 2s."},
	}
	out := service.ConvertMetrics(m)
	if len(out.PerfSuggestions) != 1 || out.PerfSuggestions[0] != "Query took over 2s." {
		t.Errorf("perf_suggestions = %v", out.PerfSuggestions)
	}
}

func TestConvertMetrics_TimeSeriesNextPeriodForecast(t *testing.T) {
	m := &metrics.Metrics{
		Aggregates:  map[string]metrics.Aggregates{},
		DataQuality: map[string]metrics.ColumnQuality{},
		TimeSeries: map[string]metrics.TimeSeriesMetric{
			"rev": {
				CurrentPeriod:      100,
				Trend:              "up",
				NextPeriodForecast: floatPtr(105.5),
			},
		},
	}
	out := service.ConvertMetrics(m)
	ts := out.TimeSeries["rev"]
	if ts == nil || ts.NextPeriodForecast == nil {
		t.Fatal("expected next_period_forecast in time series")
	}
	if *ts.NextPeriodForecast != 105.5 {
		t.Errorf("next_period_forecast = %v, want 105.5", *ts.NextPeriodForecast)
	}
}

func TestConvertMetrics_TimeSeriesExplanations(t *testing.T) {
	m := &metrics.Metrics{
		Aggregates:  map[string]metrics.Aggregates{},
		DataQuality: map[string]metrics.ColumnQuality{},
		TimeSeries: map[string]metrics.TimeSeriesMetric{
			"rev": {
				CurrentPeriod: 100,
				Trend:         "up",
				TrendSummary: &metrics.TrendSummary{
					Direction:   "increasing",
					Summary:     "Increasing over recent periods",
					Slope:       2.5,
					PeriodsUsed: 6,
					Explanation: "Revenue is likely rising due to sustained recent demand.",
				},
				Anomalies: []metrics.AnomalyPoint{
					{
						PeriodLabel: "2025-03",
						Value:       190,
						Reason:      "High: 2.2σ above mean",
						Explanation: "A campaign spike likely drove this jump.",
					},
				},
			},
		},
	}
	out := service.ConvertMetrics(m)
	ts := out.TimeSeries["rev"]
	if ts == nil || ts.TrendSummary == nil {
		t.Fatal("expected trend summary in time series")
	}
	if ts.TrendSummary.Explanation == nil || *ts.TrendSummary.Explanation == "" {
		t.Fatal("expected trend explanation in API metrics")
	}
	if len(ts.Anomalies) != 1 || ts.Anomalies[0].Explanation == nil || *ts.Anomalies[0].Explanation == "" {
		t.Fatal("expected anomaly explanation in API metrics")
	}
}

func TestConvertMetrics_PeriodLabels(t *testing.T) {
	m := &metrics.Metrics{
		Aggregates:          map[string]metrics.Aggregates{},
		DataQuality:         map[string]metrics.ColumnQuality{},
		CurrentPeriodLabel:  "2025-02",
		PreviousPeriodLabel: "2025-01",
	}
	out := service.ConvertMetrics(m)
	if out.PeriodCurrentLabel == nil || *out.PeriodCurrentLabel != "2025-02" {
		t.Errorf("period_current_label = %v", out.PeriodCurrentLabel)
	}
	if out.PeriodPreviousLabel == nil || *out.PeriodPreviousLabel != "2025-01" {
		t.Errorf("period_previous_label = %v", out.PeriodPreviousLabel)
	}
}

func floatPtr(f float64) *float64 { return &f }
