package metrics

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

// TrendFromChangePct maps percent change to up/down/flat using the configured threshold.
func TrendFromChangePct(changePct *float64, trendThresholdPercent float64) string {
	if changePct == nil {
		return "flat"
	}
	if trendThresholdPercent <= 0 {
		trendThresholdPercent = 0.5
	}
	if math.Abs(*changePct) < trendThresholdPercent {
		return "flat"
	}
	if *changePct > 0 {
		return "up"
	}
	return "down"
}

// CalculateMetrics computes all metrics from query results.
// If opts is nil, default options are used (0.5% trend threshold, 2.0 sigma, 6 trend periods, 3-period MA).
func CalculateMetrics(columns []string, rows [][]interface{}, profiles []ColumnProfile, opts *Options) *Metrics {
	if opts == nil {
		opts = &Options{}
	}
	opts.ApplyDefaults()

	metrics := &Metrics{
		Profiles:      profiles,
		Aggregates:    make(map[string]Aggregates),
		TopCategories: make(map[string][]TopCategory),
		TimeSeries:    make(map[string]TimeSeriesMetric),
		DataQuality:   make(map[string]ColumnQuality),
	}

	if len(rows) == 0 {
		return metrics
	}

	// Data quality (nulls, distinct) for all columns
	metrics.calculateDataQuality(columns, rows)

	// Calculate aggregates for numeric/measure columns (includes std dev)
	for i, profile := range profiles {
		if profile.IsMeasure && profile.Type == ColumnTypeNumeric {
			agg := calculateAggregates(rows, i)
			metrics.Aggregates[profile.Name] = agg
		}
	}

	// Calculate top categories for grouped data
	metrics.calculateTopCategories(columns, rows, profiles)

	// Calculate time-series metrics (includes predictive next-period forecast)
	metrics.calculateTimeSeries(columns, rows, profiles, opts)

	// Correlations between numeric measure columns (when enough rows and ≥2 measures)
	metrics.calculateCorrelations(columns, rows, profiles, opts)

	// Cohort analysis when cohort dimension is present
	metrics.calculateCohorts(columns, rows, profiles)

	return metrics
}

// calculateAggregates computes sum, avg, min, max, count for a numeric column
func calculateAggregates(rows [][]interface{}, colIndex int) Aggregates {
	var sum float64
	var count int
	var min, max *float64

	vals := make([]float64, 0, len(rows))
	for _, row := range rows {
		if colIndex >= len(row) {
			continue
		}

		val, ok := GetNumericValue(row[colIndex])
		if !ok {
			continue
		}

		sum += val
		count++
		vals = append(vals, val)

		if min == nil || val < *min {
			min = &val
		}
		if max == nil || val > *max {
			max = &val
		}
	}

	agg := Aggregates{
		Sum:   &sum,
		Min:   min,
		Max:   max,
		Count: count,
	}

	if count > 0 {
		avg := sum / float64(count)
		agg.Avg = &avg
		_, std := MeanAndStd(vals)
		if std > 0 {
			agg.StdDev = &std
		}
	}

	return agg
}

// calculateTopCategories identifies top categories by measure value
func (m *Metrics) calculateTopCategories(columns []string, rows [][]interface{}, profiles []ColumnProfile) {
	// Find dimension columns (categories) and measure columns
	dimensionCols := make([]int, 0, len(profiles))
	measureCols := make([]int, 0, len(profiles))

	for i, profile := range profiles {
		if profile.IsDimension {
			dimensionCols = append(dimensionCols, i)
		}
		if profile.IsMeasure {
			measureCols = append(measureCols, i)
		}
	}

	// For each measure, find top categories
	for _, measureIdx := range measureCols {
		measureName := columns[measureIdx]

		// Group by first dimension column
		if len(dimensionCols) == 0 {
			continue
		}

		dimensionIdx := dimensionCols[0]
		categoryMap := make(map[string]float64)

		// Aggregate values by category
		for _, row := range rows {
			if measureIdx >= len(row) || dimensionIdx >= len(row) {
				continue
			}

			category := getStringValue(row[dimensionIdx])
			value, ok := GetNumericValue(row[measureIdx])
			if !ok {
				continue
			}

			categoryMap[category] += value
		}

		// Convert to slice and sort
		categories := make([]TopCategory, 0, len(categoryMap))
		var total float64
		for cat, val := range categoryMap {
			total += val
			categories = append(categories, TopCategory{
				Category: cat,
				Value:    val,
			})
		}

		// Sort by value descending
		sort.Slice(categories, func(i, j int) bool {
			return categories[i].Value > categories[j].Value
		})

		// Calculate percentages and limit to top 10
		topN := 10
		if len(categories) < topN {
			topN = len(categories)
		}

		for i := 0; i < topN; i++ {
			if total > 0 {
				categories[i].Percentage = (categories[i].Value / total) * 100
			}
		}

		m.TopCategories[measureName] = categories[:topN]
	}
}

// calculateTimeSeries buckets rows by period in Go and compares the last two periods.
// Period-over-period for POST /queries/run prefers SQL window functions (LAG) in
// app/queryrunner/period_comparison.go; this path remains the fallback when SQL
// comparison is unavailable (e.g. profiling cannot detect time + measure columns).
func (m *Metrics) calculateTimeSeries(columns []string, rows [][]interface{}, profiles []ColumnProfile, opts *Options) {
	var timeColIdx = -1
	measureCols := make([]int, 0, len(profiles))

	for i, profile := range profiles {
		if profile.IsTimeSeries {
			timeColIdx = i
		}
		if profile.IsMeasure {
			measureCols = append(measureCols, i)
		}
	}

	if timeColIdx == -1 || len(measureCols) == 0 {
		return
	}

	if len(rows) < 2 {
		return
	}

	// Group by period and calculate totals
	periodTotals := make(map[string]map[int]float64) // period -> measureCol -> total
	periodTimes := make(map[string]time.Time)        // period -> parsed time for sorting

	for _, row := range rows {
		if timeColIdx >= len(row) {
			continue
		}

		periodStr := getStringValue(row[timeColIdx])
		if periodStr == "" {
			continue
		}

		// Try to parse as time for proper sorting
		var periodTime time.Time
		var err error
		timeFormats := []string{
			time.RFC3339,
			"2006-01-02T15:04:05Z07:00",
			"2006-01-02 15:04:05",
			"2006-01-02",
		}
		for _, format := range timeFormats {
			if t, parseErr := time.Parse(format, periodStr); parseErr == nil {
				periodTime = t
				err = nil
				break
			}
		}
		if err != nil {
			// Use string as-is if parsing fails
			periodTime = time.Time{}
		}

		if periodTotals[periodStr] == nil {
			periodTotals[periodStr] = make(map[int]float64)
		}
		if !periodTime.IsZero() {
			periodTimes[periodStr] = periodTime
		}

		for _, measureIdx := range measureCols {
			if measureIdx >= len(row) {
				continue
			}

			value, ok := GetNumericValue(row[measureIdx])
			if ok {
				periodTotals[periodStr][measureIdx] += value
			}
		}
	}

	// Get periods sorted by time if available, otherwise by string
	periods := make([]string, 0, len(periodTotals))
	for p := range periodTotals {
		periods = append(periods, p)
	}

	if len(periods) < 2 {
		return
	}

	// Sort periods by time if available, otherwise by string
	sort.Slice(periods, func(i, j int) bool {
		ti, hasTimeI := periodTimes[periods[i]]
		tj, hasTimeJ := periodTimes[periods[j]]
		if hasTimeI && hasTimeJ {
			return ti.Before(tj)
		}
		if hasTimeI {
			return true
		}
		if hasTimeJ {
			return false
		}
		return periods[i] < periods[j]
	})

	// Use last two periods for comparison
	currentPeriod := periods[len(periods)-1]
	previousPeriod := periods[len(periods)-2]
	m.CurrentPeriodLabel = currentPeriod
	m.PreviousPeriodLabel = previousPeriod

	maxPeriods := opts.MaxTimeSeriesPeriods
	if maxPeriods <= 0 {
		maxPeriods = 24
	}
	movingAvgWindow := opts.MovingAvgWindow
	anomalySigma := opts.AnomalySigma
	trendPeriods := opts.TrendPeriods
	trendThresholdPercent := opts.TrendThresholdPercent

	// Build ordered values per measure (one float per period)
	measureValues := make(map[string][]float64)
	for _, measureIdx := range measureCols {
		measureName := columns[measureIdx]
		vals := make([]float64, 0, len(periods))
		for _, p := range periods {
			vals = append(vals, periodTotals[p][measureIdx])
		}
		measureValues[measureName] = vals
	}

	// Calculate metrics for each measure (including advanced)
	for _, measureIdx := range measureCols {
		measureName := columns[measureIdx]
		values := measureValues[measureName]

		current := periodTotals[currentPeriod][measureIdx]
		previous := periodTotals[previousPeriod][measureIdx]

		change := current - previous
		var changePct *float64
		if previous != 0 {
			pct := (change / previous) * 100
			changePct = &pct
		}

		trend := TrendFromChangePct(changePct, trendThresholdPercent)

		ts := TimeSeriesMetric{
			CurrentPeriod:    current,
			PreviousPeriod:   &previous,
			Change:           &change,
			ChangePercentage: changePct,
			Trend:            trend,
		}

		// Advanced: last N periods as Periods (newest last)
		nPeriods := len(periods)
		if nPeriods > maxPeriods {
			nPeriods = maxPeriods
		}
		start := len(periods) - nPeriods
		if start < 0 {
			start = 0
		}
		ts.Periods = make([]PeriodPoint, 0, nPeriods)
		for i := start; i < len(periods); i++ {
			ts.Periods = append(ts.Periods, PeriodPoint{
				Label: periods[i],
				Value: values[i],
			})
		}

		// Moving average (SMA) for latest period
		if len(values) >= movingAvgWindow {
			var sum float64
			for j := len(values) - movingAvgWindow; j < len(values); j++ {
				sum += values[j]
			}
			ma := sum / float64(movingAvgWindow)
			ts.MovingAverage = &ma
		}

		// Anomaly detection: z-score or isolation forest per opts.AnomalyMethod
		if len(values) >= 3 {
			if opts.AnomalyMethod == "isolation_forest" {
				ts.Anomalies = anomaliesIsolationForest(values, periods, 0.6)
			} else {
				mean, std := MeanAndStd(values)
				if std > 0 {
					for i, v := range values {
						z := (v - mean) / std
						if math.Abs(z) >= anomalySigma {
							reason := "High"
							if z < 0 {
								reason = "Low"
							}
							ts.Anomalies = append(ts.Anomalies, AnomalyPoint{
								PeriodLabel: periods[i],
								Value:       v,
								Reason:      formatAnomalyReason(reason, z, anomalySigma),
							})
						}
					}
				}
			}
		}

		// Trend summary: linear regression over last trendPeriods (or all)
		nTrend := trendPeriods
		if len(values) < nTrend {
			nTrend = len(values)
		}
		if nTrend >= 2 {
			trendStart := len(values) - nTrend
			if trendStart < 0 {
				trendStart = 0
				nTrend = len(values)
			}
			y := values[trendStart:]
			slope, _ := LinearRegression(y)
			avgVal, _ := MeanAndStd(y)
			direction := "stable"
			if avgVal != 0 && math.Abs(slope) > 1e-10 {
				pctSlope := (slope / math.Abs(avgVal)) * 100
				if pctSlope > trendThresholdPercent {
					direction = "increasing"
				} else if pctSlope < -trendThresholdPercent {
					direction = "decreasing"
				}
			}
			summary := formatTrendSummary(direction, slope, avgVal, nTrend)
			ts.TrendSummary = &TrendSummary{
				Direction:   direction,
				Slope:       slope,
				PeriodsUsed: nTrend,
				Summary:     summary,
			}
			// Point forecast and optional confidence interval
			if len(y) > 0 {
				forecast := y[len(y)-1] + slope
				ts.NextPeriodForecast = &forecast
				if nTrend >= 3 {
					lower, upper := forecastCI(y, opts.ConfidenceLevel)
					if lower <= upper {
						ts.ForecastCILower = &lower
						ts.ForecastCIUpper = &upper
					}
				}
				ts.PredictiveSummary = formatPredictiveSummary(forecast, nTrend, ts.ForecastCILower, ts.ForecastCIUpper)
				// Exponential smoothing and Holt forecasts (when enough points)
				if nTrend >= 2 {
					if ses := simpleExponentialSmoothing(y, opts.SmoothingAlpha); !math.IsNaN(ses) {
						ts.ExponentialSmoothForecast = &ses
					}
					if nTrend >= 3 {
						if holt := holtForecast(y, opts.SmoothingAlpha, opts.SmoothingBeta); !math.IsNaN(holt) {
							ts.HoltForecast = &holt
						}
					}
					// Seasonality: detect period and optional seasonally adjusted forecast
					if nTrend >= opts.MinPeriodsForSeasonality {
						if p := detectSeasonalPeriod(y, opts.MaxSeasonalLag); p > 0 {
							ts.SeasonalPeriod = p
							if adj := seasonallyAdjustedForecast(y, p, slope); !math.IsNaN(adj) {
								ts.SeasonallyAdjustedForecast = &adj
							}
						}
					}
				}
			}
		}

		m.TimeSeries[measureName] = ts
	}
}

// calculateCorrelations computes Pearson and Spearman correlations between numeric measure columns.
// Only when ≥2 measure columns and ≥ opts.MinRowsForCorrelation rows (with pairwise complete data).
func (m *Metrics) calculateCorrelations(columns []string, rows [][]interface{}, profiles []ColumnProfile, opts *Options) {
	measureCols := make([]int, 0, len(profiles))
	for i, p := range profiles {
		if p.IsMeasure && p.Type == ColumnTypeNumeric {
			measureCols = append(measureCols, i)
		}
	}
	if len(measureCols) < 2 || len(rows) < opts.MinRowsForCorrelation {
		return
	}
	// Build matrix of values (row-major), skipping rows with any missing measure
	type rowVals struct {
		vals []float64
	}
	var validRows []rowVals
	for _, row := range rows {
		vals := make([]float64, 0, len(measureCols))
		ok := true
		for _, ci := range measureCols {
			if ci >= len(row) {
				ok = false
				break
			}
			v, o := GetNumericValue(row[ci])
			if !o {
				ok = false
				break
			}
			vals = append(vals, v)
		}
		if ok && len(vals) == len(measureCols) {
			validRows = append(validRows, rowVals{vals})
		}
	}
	if len(validRows) < opts.MinRowsForCorrelation {
		return
	}
	// Extract column vectors
	colVals := make([][]float64, len(measureCols))
	for j := range measureCols {
		colVals[j] = make([]float64, len(validRows))
		for i, r := range validRows {
			colVals[j][i] = r.vals[j]
		}
	}
	// Pairwise correlations (canonical order: col A < col B by name)
	names := make([]string, len(measureCols))
	for i, ci := range measureCols {
		names[i] = columns[ci]
	}
	for i := 0; i < len(measureCols); i++ {
		for j := i + 1; j < len(measureCols); j++ {
			pearson := pearsonCorrelation(colVals[i], colVals[j])
			spearman := spearmanCorrelation(colVals[i], colVals[j])
			if math.IsNaN(pearson) {
				pearson = 0
			}
			if math.IsNaN(spearman) {
				spearman = 0
			}
			pearson = clampCorrelation(pearson)
			spearman = clampCorrelation(spearman)
			m.Correlations = append(m.Correlations, CorrelationPair{
				ColumnA:  names[i],
				ColumnB:  names[j],
				Pearson:  pearson,
				Spearman: spearman,
			})
		}
	}
}

// clampCorrelation clamps r to [-1, 1] (handles floating-point drift).
func clampCorrelation(r float64) float64 {
	if r < -1 {
		return -1
	}
	if r > 1 {
		return 1
	}
	return r
}

// pearsonCorrelation returns Pearson r between x and y. Assumes same length.
func pearsonCorrelation(x, y []float64) float64 {
	n := len(x)
	if n != len(y) || n < 2 {
		return 0
	}
	mx, sx := MeanAndStd(x)
	my, sy := MeanAndStd(y)
	if sx <= 0 || sy <= 0 {
		return 0
	}
	var cov float64
	for i := 0; i < n; i++ {
		cov += (x[i] - mx) * (y[i] - my)
	}
	cov /= float64(n)
	return cov / (sx * sy)
}

// spearmanCorrelation returns Spearman rank correlation (Pearson on ranks).
func spearmanCorrelation(x, y []float64) float64 {
	n := len(x)
	if n != len(y) || n < 2 {
		return 0
	}
	rx := rank(x)
	ry := rank(y)
	return pearsonCorrelation(rx, ry)
}

// rank returns ranks of values (1-based, average for ties).
func rank(v []float64) []float64 {
	n := len(v)
	r := make([]float64, n)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return v[idx[a]] < v[idx[b]] })
	// Assign ranks; handle ties by averaging
	for i := 0; i < n; {
		j := i
		for j+1 < n && v[idx[j+1]] == v[idx[j]] {
			j++
		}
		avgRank := float64(i+j+2) / 2
		for k := i; k <= j; k++ {
			r[idx[k]] = avgRank
		}
		i = j + 1
	}
	return r
}

// calculateCohorts fills Cohorts when a cohort dimension is present (column name contains "cohort").
// Expects rows with (cohort_label, period_label, measure_values...). Periods are sorted per cohort.
func (m *Metrics) calculateCohorts(columns []string, rows [][]interface{}, profiles []ColumnProfile) {
	dimensionCols := make([]int, 0, len(profiles))
	measureCols := make([]int, 0, len(profiles))
	var cohortColIdx, periodColIdx = -1, -1
	for i, p := range profiles {
		if p.IsDimension {
			dimensionCols = append(dimensionCols, i)
			if strings.Contains(strings.ToLower(columns[i]), "cohort") {
				cohortColIdx = i
			}
		}
		if p.IsMeasure && p.Type == ColumnTypeNumeric {
			measureCols = append(measureCols, i)
		}
	}
	if cohortColIdx == -1 || len(measureCols) == 0 {
		return
	}
	// Period column: another dimension (e.g. period_index) or first time column
	for _, i := range dimensionCols {
		if i != cohortColIdx {
			periodColIdx = i
			break
		}
	}
	if periodColIdx == -1 {
		for i, p := range profiles {
			if p.IsTimeSeries && i != cohortColIdx {
				periodColIdx = i
				break
			}
		}
	}
	if periodColIdx == -1 {
		return
	}
	// Aggregate by (cohort, period) -> sum per measure
	type key struct {
		cohort string
		period string
	}
	agg := make(map[key]map[int]float64) // key -> measureColIdx -> sum
	for _, row := range rows {
		if cohortColIdx >= len(row) || periodColIdx >= len(row) {
			continue
		}
		cohortLabel := getStringValue(row[cohortColIdx])
		periodLabel := getCohortPeriodLabel(row[periodColIdx])
		if cohortLabel == "" {
			continue
		}
		k := key{cohort: cohortLabel, period: periodLabel}
		if agg[k] == nil {
			agg[k] = make(map[int]float64)
		}
		for _, mi := range measureCols {
			if mi < len(row) {
				if v, ok := GetNumericValue(row[mi]); ok {
					agg[k][mi] += v
				}
			}
		}
	}
	// Group by cohort, collect periods and values (use first measure only for Cohorts)
	if len(measureCols) == 0 {
		return
	}
	measureIdx := measureCols[0]
	cohortToPeriods := make(map[string][]CohortPeriodPoint)
	for k, sums := range agg {
		v, ok := sums[measureIdx]
		if !ok {
			continue
		}
		cohortToPeriods[k.cohort] = append(cohortToPeriods[k.cohort], CohortPeriodPoint{
			PeriodLabel: k.period,
			Value:       v,
		})
	}
	// Sort periods within each cohort (numeric if possible, else string)
	for cohort, points := range cohortToPeriods {
		sort.Slice(points, func(a, b int) bool {
			na, ea := strconv.ParseFloat(points[a].PeriodLabel, 64)
			nb, eb := strconv.ParseFloat(points[b].PeriodLabel, 64)
			if ea == nil && eb == nil {
				return na < nb
			}
			return points[a].PeriodLabel < points[b].PeriodLabel
		})
		cohortToPeriods[cohort] = points
	}
	// Build ordered cohort list and optional retention
	cohortOrder := make([]string, 0, len(cohortToPeriods))
	for c := range cohortToPeriods {
		cohortOrder = append(cohortOrder, c)
	}
	sort.Strings(cohortOrder)
	for _, cohortLabel := range cohortOrder {
		points := cohortToPeriods[cohortLabel]
		if len(points) == 0 {
			continue
		}
		cm := CohortMetric{CohortLabel: cohortLabel, Periods: points}
		if len(points) >= 2 && points[0].Value > 0 {
			lastVal := points[len(points)-1].Value
			ret := (lastVal / points[0].Value) * 100
			cm.RetentionPct = &ret
		}
		m.Cohorts = append(m.Cohorts, cm)
	}
}

// calculateDataQuality fills DataQuality for each column (nulls, distinct count).
func (m *Metrics) calculateDataQuality(columns []string, rows [][]interface{}) {
	if len(rows) == 0 {
		return
	}
	total := len(rows)
	for colIdx, colName := range columns {
		nullCount := 0
		seen := make(map[string]struct{})
		for _, row := range rows {
			if colIdx >= len(row) {
				continue
			}
			v := row[colIdx]
			if v == nil {
				nullCount++
				continue
			}
			key := fmt.Sprintf("%v", v)
			seen[key] = struct{}{}
		}
		nullPct := 0.0
		if total > 0 {
			nullPct = float64(nullCount) / float64(total) * 100
		}
		m.DataQuality[colName] = ColumnQuality{
			NullCount:     nullCount,
			DistinctCount: len(seen),
			TotalRows:     total,
			NullPct:       nullPct,
		}
	}
}

// MeanAndStd returns mean and population standard deviation. Exported for testing.
func MeanAndStd(values []float64) (mean, std float64) {
	if len(values) == 0 {
		return 0, 0
	}
	var sum float64
	for _, v := range values {
		sum += v
	}
	mean = sum / float64(len(values))
	var sqSum float64
	for _, v := range values {
		d := v - mean
		sqSum += d * d
	}
	variance := sqSum / float64(len(values))
	if variance <= 0 {
		return mean, 0
	}
	return mean, math.Sqrt(variance)
}

func formatAnomalyReason(highLow string, z, sigma float64) string {
	zAbs := math.Abs(z)
	if zAbs < 10 {
		return highLow + ": " + formatOneDecimal(zAbs) + "σ from mean (threshold " + formatOneDecimal(sigma) + "σ)"
	}
	return highLow + ": extreme value (" + formatOneDecimal(zAbs) + "σ from mean)"
}

// anomaliesIsolationForest flags anomalies using Isolation Forest on the value series.
// scoreThreshold (e.g. 0.6) is the anomaly score above which a point is flagged; higher = more selective.
func anomaliesIsolationForest(values []float64, periods []string, scoreThreshold float64) []AnomalyPoint {
	n := len(values)
	if n != len(periods) || n < 3 {
		return nil
	}
	const numTrees = 100
	subsampleSize := 256
	if n < subsampleSize {
		subsampleSize = n
	}
	if subsampleSize < 2 {
		return nil
	}
	// c(n) = average path length in BST for n nodes; use 2*H(n-1)-2(n-1)/n approximation
	c := 2.0*(math.Log(float64(subsampleSize-1))+0.5772156649) - 2.0*float64(subsampleSize-1)/float64(subsampleSize)
	if c < 1e-6 {
		c = 1
	}
	scores := make([]float64, n)
	subVals := make([]float64, subsampleSize)
	for t := 0; t < numTrees; t++ {
		sub := make([]int, 0, subsampleSize)
		seen := make(map[int]struct{})
		for len(sub) < subsampleSize {
			i := rand.Intn(n) // #nosec G404 -- Isolation Forest subsampling does not require crypto-grade randomness
			if _, ok := seen[i]; !ok {
				seen[i] = struct{}{}
				sub = append(sub, i)
			}
		}
		for j, i := range sub {
			subVals[j] = values[i]
		}
		sort.Float64s(subVals)
		split := subVals[subsampleSize/2] // median so tree is deterministic for this subsample
		for i, v := range values {
			pathLen := isolationForestPathLength(v, values, sub, split, 0, 8)
			scores[i] += pathLen
		}
	}
	var out []AnomalyPoint
	for i := range scores {
		avgPath := scores[i] / numTrees
		score := math.Pow(2, -avgPath/c)
		if score > scoreThreshold {
			reason := "High"
			if i > 0 && values[i] < values[i-1] {
				reason = "Low"
			} else if i < n-1 && values[i] < values[i+1] {
				reason = "Low"
			}
			out = append(out, AnomalyPoint{
				PeriodLabel: periods[i],
				Value:       values[i],
				Reason:      "Isolation Forest: " + reason + " anomaly (score " + formatOneDecimal(score) + ")",
			})
		}
	}
	return out
}

// isolationForestPathLength returns the path length (depth) when routing value through one tree.
// sub is the subsample indices, split is root split; tree uses median of subset at each node (deterministic).
func isolationForestPathLength(value float64, values []float64, sub []int, split float64, depth, maxDepth int) float64 {
	if depth >= maxDepth || len(sub) <= 1 {
		return float64(depth)
	}
	left, right := make([]int, 0, len(sub)), make([]int, 0, len(sub))
	for _, i := range sub {
		if values[i] <= split {
			left = append(left, i)
		} else {
			right = append(right, i)
		}
	}
	if value <= split {
		if len(left) <= 1 {
			return float64(depth + 1)
		}
		leftVals := make([]float64, len(left))
		for j, i := range left {
			leftVals[j] = values[i]
		}
		sort.Float64s(leftVals)
		nextSplit := leftVals[len(leftVals)/2]
		return isolationForestPathLength(value, values, left, nextSplit, depth+1, maxDepth)
	}
	if len(right) <= 1 {
		return float64(depth + 1)
	}
	rightVals := make([]float64, len(right))
	for j, i := range right {
		rightVals[j] = values[i]
	}
	sort.Float64s(rightVals)
	nextSplit := rightVals[len(rightVals)/2]
	return isolationForestPathLength(value, values, right, nextSplit, depth+1, maxDepth)
}

func formatOneDecimal(x float64) string {
	return strconv.FormatFloat(x, 'f', 1, 64)
}

// LinearRegression returns slope and intercept for y = slope*x + intercept (x = 0,1,2,...). Exported for testing.
func LinearRegression(y []float64) (slope, intercept float64) {
	n := float64(len(y))
	if n < 2 {
		return 0, 0
	}
	var sumX, sumY, sumXY, sumX2 float64
	for i, v := range y {
		x := float64(i)
		sumX += x
		sumY += v
		sumXY += x * v
		sumX2 += x * x
	}
	denom := n*sumX2 - sumX*sumX
	if math.Abs(denom) < 1e-20 {
		return 0, sumY / n
	}
	slope = (n*sumXY - sumX*sumY) / denom
	intercept = (sumY - slope*sumX) / n
	return slope, intercept
}

func formatTrendSummary(direction string, slope, avgVal float64, periodsUsed int) string {
	if periodsUsed == 0 {
		return ""
	}
	if math.Abs(avgVal) < 1e-10 {
		return direction + " over last " + strconv.Itoa(periodsUsed) + " periods (absolute change ~" + formatOneDecimal(slope) + " per period)."
	}
	pctPerPeriod := (slope / math.Abs(avgVal)) * 100
	switch direction {
	case "increasing":
		return "Increasing ~" + formatOneDecimal(pctPerPeriod) + "% per period over last " + strconv.Itoa(periodsUsed) + " periods."
	case "decreasing":
		return "Decreasing ~" + formatOneDecimal(-pctPerPeriod) + "% per period over last " + strconv.Itoa(periodsUsed) + " periods."
	default:
		return "Stable over last " + strconv.Itoa(periodsUsed) + " periods."
	}
}

// forecastCI returns the lower and upper bounds of the confidence interval for the one-step-ahead
// linear regression forecast. y is the series used for regression (x = 0,1,...,len(y)-1).
// confidence should be in (0, 1), e.g. 0.95. Returns (lower, upper); if calculation fails, upper < lower.
func forecastCI(y []float64, confidence float64) (lower, upper float64) {
	n := len(y)
	if n < 3 || confidence <= 0 || confidence >= 1 {
		return 0, -1
	}
	slope, intercept := LinearRegression(y)
	// Residual sum of squares
	var rss float64
	for i, v := range y {
		fit := intercept + slope*float64(i)
		rss += (v - fit) * (v - fit)
	}
	df := n - 2
	if df < 1 {
		return 0, -1
	}
	s := math.Sqrt(rss / float64(df)) // residual standard error
	if s <= 0 || math.IsInf(s, 0) {
		return 0, -1
	}
	// Next period: x_new = n. SE = s * sqrt(1 + 1/n + (x_new - x_mean)^2 / S_xx)
	// x_mean = (n-1)/2, S_xx = n(n^2-1)/12, (x_new - x_mean)^2 = (n+1)^2/4
	xMean := float64(n-1) / 2
	sxx := float64(n) * (float64(n)*float64(n) - 1) / 12
	if sxx < 1e-20 {
		return 0, -1
	}
	xNew := float64(n)
	se := s * math.Sqrt(1+1/float64(n)+(xNew-xMean)*(xNew-xMean)/sxx)
	forecast := intercept + slope*xNew
	tCrit := tQuantileTwoTailed(df, confidence)
	halfWidth := tCrit * se
	return forecast - halfWidth, forecast + halfWidth
}

// tQuantileTwoTailed returns the two-tailed critical value for the given df and confidence level (e.g. 0.95).
// For df > 30 uses normal approximation; for df <= 30 uses t-table for 0.95 or normal approx for 0.90/0.99.
func tQuantileTwoTailed(df int, confidence float64) float64 {
	tTable95 := []float64{0, 12.71, 4.30, 3.18, 2.78, 2.57, 2.45, 2.36, 2.31, 2.26, 2.23, 2.20, 2.18, 2.16, 2.14, 2.13, 2.12, 2.11, 2.10, 2.09, 2.09, 2.08, 2.07, 2.07, 2.06, 2.06, 2.06, 2.05, 2.05, 2.05, 2.04}
	if confidence < 0.5 || confidence >= 1 {
		confidence = 0.95
	}
	// Normal approximation critical values (two-tailed)
	var normCrit float64
	switch {
	case confidence >= 0.98 && confidence <= 0.995:
		normCrit = 2.576 // ~0.99
	case confidence >= 0.94 && confidence <= 0.96:
		normCrit = 1.96 // 0.95
	case confidence >= 0.88 && confidence <= 0.92:
		normCrit = 1.645 // ~0.90
	default:
		normCrit = 1.96
	}
	if df <= 0 {
		return normCrit
	}
	if df > 30 {
		return normCrit
	}
	if confidence >= 0.94 && confidence <= 0.96 && df < len(tTable95) {
		return tTable95[df]
	}
	return normCrit
}

// simpleExponentialSmoothing returns the one-step-ahead forecast using SES: s_0 = y_0, s_t = alpha*y_t + (1-alpha)*s_{t-1}; forecast = s_n.
func simpleExponentialSmoothing(y []float64, alpha float64) float64 {
	if len(y) == 0 || alpha <= 0 || alpha > 1 {
		return math.NaN()
	}
	s := y[0]
	for i := 1; i < len(y); i++ {
		s = alpha*y[i] + (1-alpha)*s
	}
	return s
}

// holtForecast returns the one-step-ahead forecast using Holt's method: level L_t, trend T_t; forecast = L_n + T_n.
func holtForecast(y []float64, alpha, beta float64) float64 {
	if len(y) < 2 || alpha <= 0 || alpha > 1 || beta < 0 || beta > 1 {
		return math.NaN()
	}
	L := y[0]
	T := y[1] - y[0]
	for i := 1; i < len(y); i++ {
		Lnew := alpha*y[i] + (1-alpha)*(L+T)
		T = beta*(Lnew-L) + (1-beta)*T
		L = Lnew
	}
	return L + T
}

// detectSeasonalPeriod returns a seasonal period (2–maxLag) that minimizes residual variance after removing trend and seasonal component, or 0 if none.
func detectSeasonalPeriod(y []float64, maxLag int) int {
	n := len(y)
	if n < 4 || maxLag < 2 {
		return 0
	}
	if maxLag > n/2 {
		maxLag = n / 2
	}
	slope, intercept := LinearRegression(y)
	detrended := make([]float64, n)
	for i := range y {
		detrended[i] = y[i] - (intercept + slope*float64(i))
	}
	bestPeriod := 0
	bestVar := math.Inf(1)
	candidates := []int{2, 3, 4, 6, 12}
	for _, p := range candidates {
		if p > maxLag || n < p*2 {
			continue
		}
		// Seasonal indices: mean by phase
		indices := make([]float64, p)
		counts := make([]int, p)
		for i, v := range detrended {
			phase := i % p
			indices[phase] += v
			counts[phase]++
		}
		for i := range indices {
			if counts[i] > 0 {
				indices[i] /= float64(counts[i])
			}
		}
		// Residual variance
		var resSum float64
		for i, v := range detrended {
			res := v - indices[i%p]
			resSum += res * res
		}
		variance := resSum / float64(n)
		if variance < bestVar {
			bestVar = variance
			bestPeriod = p
		}
	}
	return bestPeriod
}

// seasonallyAdjustedForecast returns trend forecast + seasonal component for next period.
func seasonallyAdjustedForecast(y []float64, period int, slope float64) float64 {
	n := len(y)
	if n < period || period < 1 {
		return math.NaN()
	}
	_, intercept := LinearRegression(y)
	trendForecast := intercept + slope*float64(n)
	detrended := make([]float64, n)
	for i := range y {
		detrended[i] = y[i] - (intercept + slope*float64(i))
	}
	indices := make([]float64, period)
	counts := make([]int, period)
	for i, v := range detrended {
		phase := i % period
		indices[phase] += v
		counts[phase]++
	}
	for i := range indices {
		if counts[i] > 0 {
			indices[i] /= float64(counts[i])
		}
	}
	nextPhase := n % period
	return trendForecast + indices[nextPhase]
}

func formatPredictiveSummary(forecast float64, periodsUsed int, ciLower, ciUpper *float64) string {
	if periodsUsed == 0 {
		return ""
	}
	s := "Next period forecast: " + formatOneDecimal(forecast) + " (linear trend over " + strconv.Itoa(periodsUsed) + " periods)"
	if ciLower != nil && ciUpper != nil && *ciLower <= *ciUpper {
		s += ". Within range " + formatOneDecimal(*ciLower) + "–" + formatOneDecimal(*ciUpper) + " (confidence interval)."
	} else {
		s += "."
	}
	return s
}

// getStringValue converts an interface{} to string
func getStringValue(val interface{}) string {
	if val == nil {
		return ""
	}

	switch v := val.(type) {
	case string:
		return v
	case time.Time:
		return v.Format("2006-01-02")
	default:
		return ""
	}
}

// getCohortPeriodLabel converts an interface{} to string for cohort period (supports numeric 0,1,2...).
func getCohortPeriodLabel(val interface{}) string {
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case time.Time:
		return v.Format("2006-01-02")
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(v), 'f', -1, 32)
	}
	return fmt.Sprintf("%v", val)
}
