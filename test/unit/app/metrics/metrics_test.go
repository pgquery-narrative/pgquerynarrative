package metrics_test

import (
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
)

func TestCalculateMetrics_TimeSeries_AdvancedFields(t *testing.T) {
	columns := []string{"month", "revenue"}
	profiles := []metrics.ColumnProfile{
		{Name: "month", Type: metrics.ColumnTypeDate, IsTimeSeries: true},
		{Name: "revenue", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{"2025-01", 100.0},
		{"2025-02", 105.0},
		{"2025-03", 98.0},
		{"2025-04", 110.0},
		{"2025-05", 115.0},
		{"2025-06", 120.0},
		{"2025-07", 118.0},
		{"2025-08", 125.0},
	}
	m := metrics.CalculateMetrics(columns, rows, profiles, nil)
	if m == nil {
		t.Fatal("expected non-nil metrics")
	}
	if len(m.TimeSeries) == 0 {
		t.Fatal("expected time series for revenue")
	}
	ts, ok := m.TimeSeries["revenue"]
	if !ok {
		t.Fatal("expected revenue in time series")
	}
	if ts.CurrentPeriod != 125.0 {
		t.Errorf("current period = %v, want 125", ts.CurrentPeriod)
	}
	if len(ts.Periods) == 0 {
		t.Error("expected periods to be populated")
	}
	if ts.MovingAverage == nil {
		t.Error("expected moving average (8 periods >= 3)")
	}
	if ts.TrendSummary == nil {
		t.Error("expected trend summary (>= 2 periods)")
	}
	if ts.TrendSummary != nil && ts.TrendSummary.Summary == "" {
		t.Error("expected non-empty trend summary")
	}
	if ts.NextPeriodForecast == nil {
		t.Error("expected next period forecast when trend summary is present")
	}
	// With 8 periods (>= 3), confidence interval should be present
	if ts.ForecastCILower == nil || ts.ForecastCIUpper == nil {
		t.Error("expected forecast CI when time series has >= 3 periods")
	}
	if ts.ForecastCILower != nil && ts.ForecastCIUpper != nil && *ts.ForecastCILower > *ts.ForecastCIUpper {
		t.Error("expected forecast_ci_lower <= forecast_ci_upper")
	}
}

func TestCalculateMetrics_TimeSeries_AnomalyDetection(t *testing.T) {
	columns := []string{"month", "value"}
	profiles := []metrics.ColumnProfile{
		{Name: "month", Type: metrics.ColumnTypeDate, IsTimeSeries: true},
		{Name: "value", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{"2025-01", 10.0},
		{"2025-02", 11.0},
		{"2025-03", 10.5},
		{"2025-04", 1000.0},
		{"2025-05", 10.0},
		{"2025-06", 11.0},
	}
	m := metrics.CalculateMetrics(columns, rows, profiles, nil)
	ts, ok := m.TimeSeries["value"]
	if !ok {
		t.Fatal("expected time series for value")
	}
	if len(ts.Anomalies) == 0 {
		t.Error("expected at least one anomaly (1000 is outlier)")
	}
}

func TestLinearRegression(t *testing.T) {
	y := []float64{1, 3, 5, 7, 9}
	slope, intercept := metrics.LinearRegression(y)
	if slope < 1.99 || slope > 2.01 {
		t.Errorf("slope = %v, want ~2", slope)
	}
	if intercept < 0.99 || intercept > 1.01 {
		t.Errorf("intercept = %v, want ~1", intercept)
	}
}

func TestMeanAndStd(t *testing.T) {
	vals := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	mean, std := metrics.MeanAndStd(vals)
	if mean < 4.99 || mean > 5.01 {
		t.Errorf("mean = %v, want 5", mean)
	}
	if std < 2.0 || std > 2.1 {
		t.Errorf("std = %v, want ~2", std)
	}
}

func TestCalculateMetrics_DataQualityAndStats(t *testing.T) {
	columns := []string{"a", "b"}
	profiles := []metrics.ColumnProfile{
		{Name: "a", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
		{Name: "b", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{10.0, 20.0},
		{12.0, 20.0},
		{nil, 22.0},
	}
	m := metrics.CalculateMetrics(columns, rows, profiles, nil)
	if len(m.DataQuality) != 2 {
		t.Errorf("data quality: got %d columns, want 2", len(m.DataQuality))
	}
	if q, ok := m.DataQuality["a"]; ok {
		if q.NullCount != 1 || q.TotalRows != 3 || q.DistinctCount != 2 {
			t.Errorf("data quality a: nulls=%d total=%d distinct=%d", q.NullCount, q.TotalRows, q.DistinctCount)
		}
	}
	if agg, ok := m.Aggregates["a"]; ok {
		if agg.StdDev == nil {
			t.Error("expected std dev for numeric measure a")
		}
	}
}

func TestTrendFromChangePct(t *testing.T) {
	up := 10.0
	down := -10.0
	flat := 0.2
	if metrics.TrendFromChangePct(&up, 0.5) != "up" {
		t.Error("expected up")
	}
	if metrics.TrendFromChangePct(&down, 0.5) != "down" {
		t.Error("expected down")
	}
	if metrics.TrendFromChangePct(&flat, 0.5) != "flat" {
		t.Error("expected flat")
	}
}

func TestCalculateMetrics_TimeSeries_PeriodLabelsAndPreviousPeriod(t *testing.T) {
	columns := []string{"month", "revenue"}
	profiles := []metrics.ColumnProfile{
		{Name: "month", Type: metrics.ColumnTypeDate, IsTimeSeries: true},
		{Name: "revenue", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{"2025-01", 80.0},
		{"2025-02", 120.0},
	}
	m := metrics.CalculateMetrics(columns, rows, profiles, nil)
	if len(m.TimeSeries) == 0 {
		t.Fatal("expected time series")
	}
	ts := m.TimeSeries["revenue"]
	if ts.PreviousPeriod == nil {
		t.Error("expected previous_period when two periods exist")
	}
	if *ts.PreviousPeriod != 80.0 {
		t.Errorf("previous_period = %v, want 80", *ts.PreviousPeriod)
	}
	if ts.CurrentPeriod != 120.0 {
		t.Errorf("current_period = %v, want 120", ts.CurrentPeriod)
	}
	if m.CurrentPeriodLabel == "" {
		t.Error("expected current period label")
	}
	if m.PreviousPeriodLabel == "" {
		t.Error("expected previous period label")
	}
}

func TestCalculateMetrics_Correlations(t *testing.T) {
	columns := []string{"a", "b", "c"}
	profiles := []metrics.ColumnProfile{
		{Name: "a", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
		{Name: "b", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
		{Name: "c", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := make([][]interface{}, 12)
	for i := 0; i < 12; i++ {
		x := float64(i)
		rows[i] = []interface{}{x, x * 2, x * 3}
	}
	opts := &metrics.Options{MinRowsForCorrelation: 10}
	m := metrics.CalculateMetrics(columns, rows, profiles, opts)
	if len(m.Correlations) == 0 {
		t.Fatal("expected correlations (2+ measures, 12 rows >= 10)")
	}
	// a vs b, a vs c, b vs c = 3 pairs
	if len(m.Correlations) != 3 {
		t.Errorf("expected 3 correlation pairs, got %d", len(m.Correlations))
	}
	for _, c := range m.Correlations {
		const eps = 1e-9
		if c.Pearson < -1-eps || c.Pearson > 1+eps || c.Spearman < -1-eps || c.Spearman > 1+eps {
			t.Errorf("correlation out of range: pearson=%v spearman=%v", c.Pearson, c.Spearman)
		}
	}
}

func TestCalculateMetrics_NoTimeSeries(t *testing.T) {
	columns := []string{"category", "total"}
	profiles := []metrics.ColumnProfile{
		{Name: "category", Type: metrics.ColumnTypeText, IsDimension: true},
		{Name: "total", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{"A", 10.0},
		{"B", 20.0},
	}
	m := metrics.CalculateMetrics(columns, rows, profiles, nil)
	if len(m.TimeSeries) != 0 {
		t.Errorf("expected no time series, got %d", len(m.TimeSeries))
	}
}

func TestCalculateMetrics_Correlations_ConstantColumn(t *testing.T) {
	// One constant column -> Pearson/Spearman NaN replaced with 0 and clamped.
	columns := []string{"a", "b"}
	profiles := []metrics.ColumnProfile{
		{Name: "a", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
		{Name: "b", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := make([][]interface{}, 15)
	for i := 0; i < 15; i++ {
		rows[i] = []interface{}{5.0, float64(i)} // a constant, b varying
	}
	opts := &metrics.Options{MinRowsForCorrelation: 10}
	m := metrics.CalculateMetrics(columns, rows, profiles, opts)
	if len(m.Correlations) != 1 {
		t.Fatalf("expected 1 correlation pair, got %d", len(m.Correlations))
	}
	c := m.Correlations[0]
	if c.Pearson != 0 || c.Spearman != 0 {
		t.Errorf("constant column: expected 0,0 got pearson=%v spearman=%v", c.Pearson, c.Spearman)
	}
	if c.Pearson < -1 || c.Pearson > 1 || c.Spearman < -1 || c.Spearman > 1 {
		t.Errorf("correlations must be in [-1,1]: pearson=%v spearman=%v", c.Pearson, c.Spearman)
	}
}

func TestCalculateMetrics_ConfigurableOptions(t *testing.T) {
	columns := []string{"month", "value"}
	profiles := []metrics.ColumnProfile{
		{Name: "month", Type: metrics.ColumnTypeDate, IsTimeSeries: true},
		{Name: "value", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{"2025-01", 10.0},
		{"2025-02", 11.0},
		{"2025-03", 10.5},
		{"2025-04", 1000.0},
		{"2025-05", 10.0},
		{"2025-06", 11.0},
	}
	// Default sigma 2.0: 1000 is an anomaly
	mDefault := metrics.CalculateMetrics(columns, rows, profiles, nil)
	tsDefault := mDefault.TimeSeries["value"]
	if len(tsDefault.Anomalies) == 0 {
		t.Error("expected at least one anomaly with default sigma")
	}
	// Stricter sigma 5.0: fewer points flagged (1000 may still be flagged)
	optsStrict := &metrics.Options{AnomalySigma: 5.0}
	optsStrict.ApplyDefaults()
	mStrict := metrics.CalculateMetrics(columns, rows, profiles, optsStrict)
	_ = mStrict.TimeSeries["value"]
	// Custom MA window 2
	optsMA := &metrics.Options{MovingAvgWindow: 2}
	optsMA.ApplyDefaults()
	mMA := metrics.CalculateMetrics(columns, rows, profiles, optsMA)
	tsMA := mMA.TimeSeries["value"]
	if tsMA.MovingAverage == nil {
		t.Error("expected moving average with window 2 (6 points)")
	}
}

func TestCalculateMetrics_TimeSeries_AnomalyMethod_IsolationForest(t *testing.T) {
	columns := []string{"month", "value"}
	profiles := []metrics.ColumnProfile{
		{Name: "month", Type: metrics.ColumnTypeDate, IsTimeSeries: true},
		{Name: "value", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{"2025-01", 10.0},
		{"2025-02", 11.0},
		{"2025-03", 10.5},
		{"2025-04", 1000.0},
		{"2025-05", 10.0},
		{"2025-06", 11.0},
	}
	opts := &metrics.Options{AnomalyMethod: "isolation_forest"}
	opts.ApplyDefaults()
	m := metrics.CalculateMetrics(columns, rows, profiles, opts)
	ts, ok := m.TimeSeries["value"]
	if !ok {
		t.Fatal("expected time series for value")
	}
	if len(ts.Anomalies) == 0 {
		t.Error("expected at least one anomaly with isolation_forest (1000 is outlier)")
	}
}

func TestCalculateMetrics_Cohorts(t *testing.T) {
	columns := []string{"cohort_month", "period_index", "revenue"}
	profiles := []metrics.ColumnProfile{
		{Name: "cohort_month", Type: metrics.ColumnTypeText, IsDimension: true},
		{Name: "period_index", Type: metrics.ColumnTypeNumeric, IsDimension: true},
		{Name: "revenue", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	rows := [][]interface{}{
		{"2025-01", 0, 100.0},
		{"2025-01", 1, 80.0},
		{"2025-01", 2, 60.0},
		{"2025-02", 0, 120.0},
		{"2025-02", 1, 90.0},
	}
	m := metrics.CalculateMetrics(columns, rows, profiles, nil)
	if len(m.Cohorts) == 0 {
		t.Fatal("expected cohorts when cohort column present")
	}
	if len(m.Cohorts) != 2 {
		t.Errorf("expected 2 cohorts, got %d", len(m.Cohorts))
	}
	for _, c := range m.Cohorts {
		if len(c.Periods) == 0 {
			t.Errorf("cohort %q has no periods", c.CohortLabel)
		}
		if c.CohortLabel == "2025-01" && len(c.Periods) != 3 {
			t.Errorf("cohort 2025-01: expected 3 periods, got %d", len(c.Periods))
		}
		if c.CohortLabel == "2025-02" && len(c.Periods) != 2 {
			t.Errorf("cohort 2025-02: expected 2 periods, got %d", len(c.Periods))
		}
	}
}
