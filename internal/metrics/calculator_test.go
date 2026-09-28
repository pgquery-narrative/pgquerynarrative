package metrics

import (
	"math"
	"testing"
	"time"
)

func ptr(f float64) *float64 { return &f }

func approx(t *testing.T, got, want, tol float64, label string) {
	t.Helper()
	if math.IsNaN(got) || math.Abs(got-want) > tol {
		t.Errorf("%s: got %v, want %v (±%v)", label, got, want, tol)
	}
}

func TestTrendFromChangePct(t *testing.T) {
	cases := []struct {
		name      string
		changePct *float64
		threshold float64
		want      string
	}{
		{"nil is flat", nil, 0.5, "flat"},
		{"below default threshold", ptr(0.3), 0, "flat"},
		{"positive above threshold", ptr(4.1), 0.5, "up"},
		{"negative above threshold", ptr(-4.1), 0.5, "down"},
		{"custom threshold suppresses", ptr(3), 5, "flat"},
		{"custom threshold trips", ptr(6), 5, "up"},
		{"at threshold is not flat (strict <)", ptr(0.5), 0.5, "up"},
		{"just under threshold is flat", ptr(0.49), 0.5, "flat"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := TrendFromChangePct(c.changePct, c.threshold); got != c.want {
				t.Fatalf("got %q want %q", got, c.want)
			}
		})
	}
}

func TestClampCorrelation(t *testing.T) {
	if got := clampCorrelation(-1.5); got != -1 {
		t.Errorf("under: got %v", got)
	}
	if got := clampCorrelation(1.5); got != 1 {
		t.Errorf("over: got %v", got)
	}
	if got := clampCorrelation(0.42); got != 0.42 {
		t.Errorf("in range: got %v", got)
	}
}

func TestMeanAndStd(t *testing.T) {
	if m, s := MeanAndStd(nil); m != 0 || s != 0 {
		t.Errorf("empty: got mean=%v std=%v", m, s)
	}
	if m, s := MeanAndStd([]float64{7}); m != 7 || s != 0 {
		t.Errorf("single: got mean=%v std=%v", m, s)
	}
	// population std of {2,4,4,4,5,5,7,9} is 2, mean is 5
	m, s := MeanAndStd([]float64{2, 4, 4, 4, 5, 5, 7, 9})
	approx(t, m, 5, 1e-9, "mean")
	approx(t, s, 2, 1e-9, "std")
}

func TestPearsonCorrelation(t *testing.T) {
	if got := pearsonCorrelation([]float64{1, 2, 3}, []float64{1, 2}); got != 0 {
		t.Errorf("length mismatch should be 0, got %v", got)
	}
	if got := pearsonCorrelation([]float64{1, 1, 1, 1}, []float64{1, 2, 3, 4}); got != 0 {
		t.Errorf("zero-variance x should be 0, got %v", got)
	}
	approx(t, pearsonCorrelation([]float64{1, 2, 3, 4}, []float64{2, 4, 6, 8}), 1, 1e-9, "perfect positive")
	approx(t, pearsonCorrelation([]float64{1, 2, 3, 4}, []float64{8, 6, 4, 2}), -1, 1e-9, "perfect negative")
}

func TestSpearmanCorrelation(t *testing.T) {
	// monotonic but non-linear → Spearman 1, Pearson < 1
	x := []float64{1, 2, 3, 4, 5}
	y := []float64{1, 4, 9, 16, 25}
	approx(t, spearmanCorrelation(x, y), 1, 1e-9, "monotonic spearman")
	if p := pearsonCorrelation(x, y); p >= 0.999 {
		t.Errorf("pearson on quadratic should be <1, got %v", p)
	}
}

func TestRankHandlesTies(t *testing.T) {
	// values: 10, 20, 20, 40 → ranks 1, 2.5, 2.5, 4
	got := rank([]float64{10, 20, 20, 40})
	want := []float64{1, 2.5, 2.5, 4}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("rank[%d]: got %v want %v (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestLinearRegression(t *testing.T) {
	if s, ic := LinearRegression([]float64{5}); s != 0 || ic != 0 {
		t.Errorf("single point: got slope=%v intercept=%v", s, ic)
	}
	// y = 3x + 2 for x = 0..4
	s, ic := LinearRegression([]float64{2, 5, 8, 11, 14})
	approx(t, s, 3, 1e-9, "slope")
	approx(t, ic, 2, 1e-9, "intercept")
}

func TestCalculateAggregates(t *testing.T) {
	rows := [][]interface{}{
		{"a", 10.0},
		{"b", 20.0},
		{"c", 30.0},
		{"d", nil}, // skipped
		{"e", "x"}, // skipped (non-numeric)
	}
	agg := calculateAggregates(rows, 1)
	if agg.Count != 3 {
		t.Fatalf("count: got %d want 3", agg.Count)
	}
	approx(t, *agg.Sum, 60, 1e-9, "sum")
	approx(t, *agg.Avg, 20, 1e-9, "avg")
	approx(t, *agg.Min, 10, 1e-9, "min")
	approx(t, *agg.Max, 30, 1e-9, "max")
	if agg.StdDev == nil {
		t.Fatal("expected non-nil stddev for spread data")
	}
}

func TestSimpleExponentialSmoothing(t *testing.T) {
	if !math.IsNaN(simpleExponentialSmoothing(nil, 0.3)) {
		t.Error("empty series should be NaN")
	}
	if !math.IsNaN(simpleExponentialSmoothing([]float64{1, 2}, 0)) {
		t.Error("alpha 0 should be NaN")
	}
	// alpha = 1 → forecast equals the last observation
	approx(t, simpleExponentialSmoothing([]float64{1, 2, 3, 9}, 1), 9, 1e-9, "alpha=1")
}

func TestHoltForecast(t *testing.T) {
	if !math.IsNaN(holtForecast([]float64{1}, 0.3, 0.1)) {
		t.Error("too short should be NaN")
	}
	// a clean linear ramp should extrapolate to roughly the next step
	f := holtForecast([]float64{10, 20, 30, 40, 50}, 0.5, 0.5)
	approx(t, f, 60, 5, "linear ramp next step")
}

func TestGetStringValue(t *testing.T) {
	if getStringValue(nil) != "" {
		t.Error("nil")
	}
	if getStringValue("hi") != "hi" {
		t.Error("string")
	}
	ts := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
	if getStringValue(ts) != "2026-01-02" {
		t.Errorf("time: got %q", getStringValue(ts))
	}
	if getStringValue(42) != "" {
		t.Error("int falls through to empty")
	}
}

func TestGetCohortPeriodLabel(t *testing.T) {
	cases := map[interface{}]string{
		nil:        "",
		"q1":       "q1",
		int(3):     "3",
		int64(9):   "9",
		float64(2): "2",
	}
	for in, want := range cases {
		if got := getCohortPeriodLabel(in); got != want {
			t.Errorf("%v: got %q want %q", in, got, want)
		}
	}
}

func TestOptionsApplyDefaults(t *testing.T) {
	o := &Options{}
	o.ApplyDefaults()
	if o.TrendThresholdPercent != 0.5 || o.AnomalySigma != 2.0 || o.AnomalyMethod != "zscore" ||
		o.TrendPeriods != 6 || o.MovingAvgWindow != 3 || o.ConfidenceLevel != 0.95 {
		t.Fatalf("defaults not applied: %+v", o)
	}
	// idempotent + preserves explicit values
	o2 := &Options{TrendThresholdPercent: 2, AnomalyMethod: "isolation_forest"}
	o2.ApplyDefaults()
	o2.ApplyDefaults()
	if o2.TrendThresholdPercent != 2 || o2.AnomalyMethod != "isolation_forest" {
		t.Fatalf("explicit values clobbered: %+v", o2)
	}
}

func TestCalculateMetrics_EndToEnd(t *testing.T) {
	columns := []string{"category", "month", "revenue"}
	profiles := []ColumnProfile{
		{Name: "category", Type: ColumnTypeText, IsDimension: true},
		{Name: "month", Type: ColumnTypeDate, IsTimeSeries: true, IsDimension: true},
		{Name: "revenue", Type: ColumnTypeNumeric, IsMeasure: true},
	}
	mk := func(cat string, mon string, rev float64) []interface{} {
		d, _ := time.Parse("2006-01-02", mon)
		return []interface{}{cat, d, rev}
	}
	rows := [][]interface{}{
		mk("A", "2026-01-01", 100),
		mk("B", "2026-01-01", 50),
		mk("A", "2026-02-01", 120),
		mk("B", "2026-02-01", 55),
		mk("A", "2026-03-01", 130),
		mk("B", "2026-03-01", 60),
	}

	m := CalculateMetrics(columns, rows, profiles, nil)

	agg, ok := m.Aggregates["revenue"]
	if !ok {
		t.Fatal("no revenue aggregate")
	}
	if agg.Count != 6 {
		t.Errorf("agg count: got %d want 6", agg.Count)
	}
	approx(t, *agg.Sum, 515, 1e-9, "revenue sum")
	if len(m.TopCategories["revenue"]) == 0 {
		t.Error("expected top categories for revenue")
	}
	if _, ok := m.DataQuality["category"]; !ok {
		t.Error("expected data quality for category")
	}
	if len(m.TimeSeries) == 0 {
		t.Error("expected a time-series metric")
	}

	// empty input → empty (but non-nil) maps, no panic
	empty := CalculateMetrics(columns, nil, profiles, nil)
	if empty == nil || len(empty.Aggregates) != 0 || len(empty.TimeSeries) != 0 {
		t.Errorf("empty rows should yield empty metrics, got %+v", empty)
	}
}
