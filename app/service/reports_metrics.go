package service

import (
	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// BuildPerfSuggestions returns performance suggestions from query result. Exported for testing.
func BuildPerfSuggestions(r *queryrunner.Result) []string {
	var suggestions []string
	if r.ExecutionTimeMs > 2000 {
		suggestions = append(suggestions, "Query took over 2s; consider adding filters or indexes.")
	}
	if r.RowCount >= 1000 {
		suggestions = append(suggestions, "Result set is large (limit applied); consider narrowing date range or dimensions.")
	}
	return suggestions
}

// ConvertMetrics converts app metrics to API type. Exported for testing.
func ConvertMetrics(m *metrics.Metrics) *reports.MetricsData {
	aggregates := make(map[string]*reports.AggregateData, len(m.Aggregates))
	for col, agg := range m.Aggregates {
		count := clampInt32(agg.Count)
		ad := &reports.AggregateData{
			Sum:   agg.Sum,
			Avg:   agg.Avg,
			Min:   agg.Min,
			Max:   agg.Max,
			Count: &count,
		}
		if agg.StdDev != nil {
			ad.StdDev = agg.StdDev
		}
		aggregates[col] = ad
	}

	topCategories := make(map[string][]*reports.TopCategoryData, len(m.TopCategories))
	for col, cats := range m.TopCategories {
		categoryData := make([]*reports.TopCategoryData, len(cats))
		for i, cat := range cats {
			categoryData[i] = &reports.TopCategoryData{
				Category:   cat.Category,
				Value:      cat.Value,
				Percentage: cat.Percentage,
			}
		}
		topCategories[col] = categoryData
	}

	timeSeries := make(map[string]*reports.TimeSeriesData, len(m.TimeSeries))
	for col, ts := range m.TimeSeries {
		tsData := &reports.TimeSeriesData{
			CurrentPeriod: ts.CurrentPeriod,
			Trend:         ts.Trend,
		}
		if ts.PreviousPeriod != nil {
			tsData.PreviousPeriod = ts.PreviousPeriod
		}
		if ts.Change != nil {
			tsData.Change = ts.Change
		}
		if ts.ChangePercentage != nil {
			tsData.ChangePercentage = ts.ChangePercentage
		}
		if len(ts.Periods) > 0 {
			tsData.Periods = make([]*reports.PeriodPointData, len(ts.Periods))
			for i := range ts.Periods {
				tsData.Periods[i] = &reports.PeriodPointData{
					Label: ts.Periods[i].Label,
					Value: ts.Periods[i].Value,
				}
			}
		}
		if ts.MovingAverage != nil {
			tsData.MovingAverage = ts.MovingAverage
		}
		if len(ts.Anomalies) > 0 {
			tsData.Anomalies = make([]*reports.AnomalyPointData, len(ts.Anomalies))
			for i := range ts.Anomalies {
				tsData.Anomalies[i] = &reports.AnomalyPointData{
					PeriodLabel: ts.Anomalies[i].PeriodLabel,
					Value:       ts.Anomalies[i].Value,
					Reason:      ts.Anomalies[i].Reason,
					Explanation: strPtrIfNotEmpty(ts.Anomalies[i].Explanation),
				}
			}
		}
		if ts.TrendSummary != nil {
			pu := clampInt32(ts.TrendSummary.PeriodsUsed)
			tsData.TrendSummary = &reports.TrendSummaryData{
				Direction:   ts.TrendSummary.Direction,
				Summary:     ts.TrendSummary.Summary,
				Slope:       &ts.TrendSummary.Slope,
				PeriodsUsed: &pu,
				Explanation: strPtrIfNotEmpty(ts.TrendSummary.Explanation),
			}
		}
		if ts.NextPeriodForecast != nil {
			tsData.NextPeriodForecast = ts.NextPeriodForecast
		}
		if ts.ForecastCILower != nil {
			tsData.ForecastCiLower = ts.ForecastCILower
		}
		if ts.ForecastCIUpper != nil {
			tsData.ForecastCiUpper = ts.ForecastCIUpper
		}
		if ts.PredictiveSummary != "" {
			tsData.PredictiveSummary = &ts.PredictiveSummary
		}
		if ts.ExponentialSmoothForecast != nil {
			tsData.ExponentialSmoothForecast = ts.ExponentialSmoothForecast
		}
		if ts.HoltForecast != nil {
			tsData.HoltForecast = ts.HoltForecast
		}
		if ts.SeasonalPeriod != 0 {
			sp := clampInt32(ts.SeasonalPeriod)
			tsData.SeasonalPeriod = &sp
		}
		if ts.SeasonallyAdjustedForecast != nil {
			tsData.SeasonallyAdjustedForecast = ts.SeasonallyAdjustedForecast
		}
		timeSeries[col] = tsData
	}

	correlations := make([]*reports.CorrelationPairData, len(m.Correlations))
	for i := range m.Correlations {
		c := &m.Correlations[i]
		correlations[i] = &reports.CorrelationPairData{
			ColumnA:  c.ColumnA,
			ColumnB:  c.ColumnB,
			Pearson:  c.Pearson,
			Spearman: c.Spearman,
		}
	}

	dataQuality := make(map[string]*reports.ColumnQualityData, len(m.DataQuality))
	for col, q := range m.DataQuality {
		dataQuality[col] = &reports.ColumnQualityData{
			NullCount:     clampInt32(q.NullCount),
			DistinctCount: clampInt32(q.DistinctCount),
			TotalRows:     clampInt32(q.TotalRows),
			NullPct:       q.NullPct,
		}
	}

	cohorts := make([]*reports.CohortMetricData, 0)
	if len(m.Cohorts) > 0 {
		cohorts = make([]*reports.CohortMetricData, len(m.Cohorts))
		for i := range m.Cohorts {
			co := &m.Cohorts[i]
			periods := make([]*reports.CohortPeriodPointData, len(co.Periods))
			for j := range co.Periods {
				periods[j] = &reports.CohortPeriodPointData{
					PeriodLabel: co.Periods[j].PeriodLabel,
					Value:       co.Periods[j].Value,
				}
			}
			cohorts[i] = &reports.CohortMetricData{
				CohortLabel:  co.CohortLabel,
				Periods:      periods,
				RetentionPct: co.RetentionPct,
			}
		}
	}
	out := &reports.MetricsData{
		Aggregates:      aggregates,
		TopCategories:   topCategories,
		TimeSeries:      timeSeries,
		Correlations:    correlations,
		Cohorts:         cohorts,
		DataQuality:     dataQuality,
		PerfSuggestions: m.PerfSuggestions,
	}
	if m.CurrentPeriodLabel != "" {
		out.PeriodCurrentLabel = &m.CurrentPeriodLabel
	}
	if m.PreviousPeriodLabel != "" {
		out.PeriodPreviousLabel = &m.PreviousPeriodLabel
	}
	return out
}
