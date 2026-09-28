package service

import (
	"testing"

	reportsapi "github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
)

func f64(v float64) *float64 { return &v }
func i32(v int32) *int32     { return &v }
func sptr(v string) *string  { return &v }

func TestToDashboardNarrative(t *testing.T) {
	if got := toDashboardNarrative(nil); got != nil {
		t.Fatalf("nil in, nil out; got %+v", got)
	}
	in := &reportsapi.NarrativeContent{
		Headline:        "Revenue fell 12% month over month",
		Takeaways:       []string{"EMEA drove the decline"},
		Drivers:         []string{"fewer orders"},
		Limitations:     []string{"partial month"},
		Recommendations: []string{"segment by channel"},
	}
	got := toDashboardNarrative(in)
	if got.Headline != in.Headline || len(got.Takeaways) != 1 || len(got.Drivers) != 1 ||
		len(got.Limitations) != 1 || len(got.Recommendations) != 1 {
		t.Errorf("narrative not carried across: %+v", got)
	}
}

func TestToDashboardListConvertersDropNilEntries(t *testing.T) {
	t.Run("chart suggestions", func(t *testing.T) {
		got := toDashboardChartSuggestions([]*reportsapi.ChartSuggestion{
			nil,
			{ChartType: "line", Label: "Revenue by month", Reason: "time series"},
			nil,
		})
		if len(got) != 1 {
			t.Fatalf("expected 1 suggestion, got %d", len(got))
		}
		if got[0].ChartType != "line" || got[0].Label != "Revenue by month" || got[0].Reason != "time series" {
			t.Errorf("fields not carried across: %+v", got[0])
		}
	})

	t.Run("period points", func(t *testing.T) {
		got := toDashboardPeriodPoints([]*reportsapi.PeriodPointData{nil, {Label: "2026-07", Value: 1200}})
		if len(got) != 1 || got[0].Label != "2026-07" || got[0].Value != 1200 {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("anomaly points", func(t *testing.T) {
		explanation := "a promotion ran that week"
		got := toDashboardAnomalyPoints([]*reportsapi.AnomalyPointData{
			nil,
			{PeriodLabel: "2026-07", Value: 9000, Reason: "3.4σ above the mean", Explanation: &explanation},
		})
		if len(got) != 1 {
			t.Fatalf("expected 1 point, got %d", len(got))
		}
		if got[0].Reason != "3.4σ above the mean" || got[0].Explanation == nil || *got[0].Explanation != explanation {
			t.Errorf("got %+v", got[0])
		}
	})

	t.Run("cohort periods", func(t *testing.T) {
		got := toDashboardCohortPeriods([]*reportsapi.CohortPeriodPointData{nil, {PeriodLabel: "M1", Value: 0.62}})
		if len(got) != 1 || got[0].PeriodLabel != "M1" {
			t.Errorf("got %+v", got)
		}
	})

	t.Run("empty input yields an empty, non-nil slice", func(t *testing.T) {
		// The API encodes these as JSON arrays; a nil slice would serialise as null.
		if got := toDashboardChartSuggestions(nil); got == nil || len(got) != 0 {
			t.Errorf("expected an empty slice, got %v", got)
		}
		if got := toDashboardPeriodPoints(nil); got == nil || len(got) != 0 {
			t.Errorf("expected an empty slice, got %v", got)
		}
	})
}

func TestToDashboardTrendSummary(t *testing.T) {
	if got := toDashboardTrendSummary(nil); got != nil {
		t.Fatalf("nil in, nil out; got %+v", got)
	}
	in := &reportsapi.TrendSummaryData{
		Direction: "down", Slope: f64(-14.2), PeriodsUsed: i32(6), Summary: "declining since March",
	}
	got := toDashboardTrendSummary(in)
	if got.Direction != "down" || got.Slope == nil || *got.Slope != -14.2 ||
		got.PeriodsUsed == nil || *got.PeriodsUsed != 6 || got.Summary != in.Summary {
		t.Errorf("trend summary not carried across: %+v", got)
	}
}

func TestToDashboardMetrics(t *testing.T) {
	t.Run("nil in, nil out", func(t *testing.T) {
		if got := toDashboardMetrics(nil); got != nil {
			t.Fatalf("got %+v", got)
		}
	})

	t.Run("nil map and slice entries are dropped, keys are preserved", func(t *testing.T) {
		in := &reportsapi.MetricsData{
			Aggregates: map[string]*reportsapi.AggregateData{
				"revenue": {Sum: f64(1000), Avg: f64(50), Count: i32(20)},
				"dropped": nil,
			},
			TopCategories: map[string][]*reportsapi.TopCategoryData{
				"region": {nil, {Category: "EMEA", Value: 400, Percentage: 40}},
			},
			TimeSeries: map[string]*reportsapi.TimeSeriesData{
				"revenue": {
					CurrentPeriod: 1200,
					Trend:         "down",
					Periods:       []*reportsapi.PeriodPointData{nil, {Label: "2026-07", Value: 1200}},
					Anomalies:     []*reportsapi.AnomalyPointData{nil},
					TrendSummary:  &reportsapi.TrendSummaryData{Direction: "down"},
				},
				"dropped": nil,
			},
			Correlations: []*reportsapi.CorrelationPairData{nil, {ColumnA: "qty", ColumnB: "revenue", Pearson: 0.91}},
			Cohorts: []*reportsapi.CohortMetricData{nil, {
				CohortLabel: "2026-01",
				Periods:     []*reportsapi.CohortPeriodPointData{{PeriodLabel: "M0", Value: 1}},
			}},
			DataQuality: map[string]*reportsapi.ColumnQualityData{
				"region":  {NullCount: 3, DistinctCount: 5, TotalRows: 100, NullPct: 3},
				"dropped": nil,
			},
			PeriodCurrentLabel:  sptr("2026-07"),
			PeriodPreviousLabel: sptr("2026-06"),
			PerfSuggestions:     []string{"add an index on date"},
		}

		got := toDashboardMetrics(in)

		if len(got.Aggregates) != 1 || got.Aggregates["revenue"] == nil {
			t.Errorf("aggregates = %+v, want only the non-nil 'revenue' entry", got.Aggregates)
		}
		if len(got.TopCategories["region"]) != 1 || got.TopCategories["region"][0].Category != "EMEA" {
			t.Errorf("top categories = %+v", got.TopCategories)
		}
		if len(got.TimeSeries) != 1 {
			t.Fatalf("time series = %+v, want only 'revenue'", got.TimeSeries)
		}
		ts := got.TimeSeries["revenue"]
		if ts == nil || ts.CurrentPeriod != 1200 || ts.Trend != "down" {
			t.Fatalf("time series revenue = %+v", ts)
		}
		if len(ts.Periods) != 1 {
			t.Errorf("nested nil period point not dropped: %+v", ts.Periods)
		}
		if len(ts.Anomalies) != 0 {
			t.Errorf("nested nil anomaly not dropped: %+v", ts.Anomalies)
		}
		if ts.TrendSummary == nil || ts.TrendSummary.Direction != "down" {
			t.Errorf("nested trend summary = %+v", ts.TrendSummary)
		}
		if len(got.Correlations) != 1 || got.Correlations[0].Pearson != 0.91 {
			t.Errorf("correlations = %+v", got.Correlations)
		}
		if len(got.Cohorts) != 1 || len(got.Cohorts[0].Periods) != 1 {
			t.Errorf("cohorts = %+v", got.Cohorts)
		}
		if len(got.DataQuality) != 1 || got.DataQuality["region"].NullCount != 3 {
			t.Errorf("data quality = %+v", got.DataQuality)
		}
		if got.PeriodCurrentLabel == nil || *got.PeriodCurrentLabel != "2026-07" ||
			got.PeriodPreviousLabel == nil || *got.PeriodPreviousLabel != "2026-06" {
			t.Errorf("period labels not carried across: %+v", got)
		}
		if len(got.PerfSuggestions) != 1 {
			t.Errorf("perf suggestions = %+v", got.PerfSuggestions)
		}
	})

	t.Run("an empty metrics payload yields initialised maps, not nils", func(t *testing.T) {
		got := toDashboardMetrics(&reportsapi.MetricsData{})
		if got.Aggregates == nil || got.TopCategories == nil || got.TimeSeries == nil || got.DataQuality == nil {
			t.Errorf("maps must be initialised so the API emits {} rather than null: %+v", got)
		}
	})
}
