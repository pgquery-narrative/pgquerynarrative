package queryrunner

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
)

// PeriodComparison is period-over-period metrics for one measure column.
type PeriodComparison struct {
	Measure          string
	Current          float64
	Previous         *float64
	Change           *float64
	ChangePercentage *float64
	Trend            string
}

// PeriodComparisonOutput is the parsed result of SQL window-based period comparison.
type PeriodComparisonOutput struct {
	Comparisons         []PeriodComparison
	CurrentPeriodLabel  string
	PreviousPeriodLabel string
}

// BuildPeriodComparisonSQL wraps an aggregated time-series query with LAG window functions.
// timeCol is the period dimension; measureCols are numeric measures to compare period-over-period.
func BuildPeriodComparisonSQL(innerSQL, timeCol string, measureCols []string) (string, error) {
	innerSQL = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(innerSQL), ";"))
	if innerSQL == "" || timeCol == "" || len(measureCols) == 0 {
		return "", fmt.Errorf("period comparison requires inner SQL, time column, and at least one measure")
	}

	timeIdent := pgx.Identifier{timeCol}.Sanitize()
	var lagSelects []string
	lagSelects = append(lagSelects,
		fmt.Sprintf("ts.%s AS pgqn_period_label", timeIdent),
		fmt.Sprintf("LAG(ts.%s) OVER (ORDER BY ts.%s) AS pgqn_previous_period_label", timeIdent, timeIdent),
	)

	for i, measure := range measureCols {
		mIdent := pgx.Identifier{measure}.Sanitize()
		lag := fmt.Sprintf("LAG(ts.%s) OVER (ORDER BY ts.%s)", mIdent, timeIdent)
		lagSelects = append(lagSelects,
			fmt.Sprintf("ts.%s AS pgqn_m%d_current", mIdent, i),
			fmt.Sprintf("%s AS pgqn_m%d_previous", lag, i),
			fmt.Sprintf("ts.%s - %s AS pgqn_m%d_change", mIdent, lag, i),
			fmt.Sprintf(
				"CASE WHEN %s IS NOT NULL AND %s <> 0 THEN ((ts.%s - %s) / %s) * 100 ELSE NULL END AS pgqn_m%d_change_pct",
				lag, lag, mIdent, lag, lag, i,
			),
		)
	}

	return fmt.Sprintf(`
WITH pgqn_ts AS (
  SELECT * FROM (
    %s
  ) AS pgqn_inner
),
pgqn_lagged AS (
  SELECT
    %s
  FROM pgqn_ts AS ts
),
pgqn_latest AS (
  SELECT * FROM pgqn_lagged
  ORDER BY pgqn_period_label DESC NULLS LAST
  LIMIT 1
)
SELECT * FROM pgqn_latest`,
		innerSQL,
		strings.Join(lagSelects, ",\n    "),
	), nil
}

// PeriodComparison runs LAG/DATE_TRUNC-style period-over-period comparison in SQL.
// Returns nil when the shape is unsuitable or fewer than two periods exist.
func (r *Runner) PeriodComparison(ctx context.Context, innerSQL, timeCol string, measureCols []string, trendThresholdPercent float64) (*PeriodComparisonOutput, error) {
	if trendThresholdPercent <= 0 {
		trendThresholdPercent = 0.5
	}

	wrappedSQL, err := BuildPeriodComparisonSQL(innerSQL, timeCol, measureCols)
	if err != nil {
		return nil, err
	}
	if err := r.activeValidator(ctx).Validate(wrappedSQL); err != nil {
		return nil, fmt.Errorf("period comparison SQL validation failed: %w", err)
	}

	queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()

	pool := r.activePool(queryCtx)
	if pool == nil {
		return nil, fmt.Errorf("period comparison pool unavailable")
	}
	rows, tx, err := queryReadOnlyRows(queryCtx, pool, wrappedSQL)
	if err != nil {
		return nil, fmt.Errorf("period comparison query failed: %w", err)
	}
	defer rows.Close()
	defer func() { _ = tx.Rollback(queryCtx) }()

	fieldDescs := rows.FieldDescriptions()
	columns := make([]ColumnInfo, len(fieldDescs))
	for i, field := range fieldDescs {
		columns[i] = ColumnInfo{Name: string(field.Name)}
	}

	var resultRows [][]interface{}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, fmt.Errorf("failed to read period comparison row: %w", err)
		}
		resultRows = append(resultRows, values)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("period comparison commit failed: %w", err)
	}

	return ParsePeriodComparisonRow(columns, resultRows, measureCols, trendThresholdPercent)
}

// ParsePeriodComparisonRow maps the single-row SQL output to PeriodComparisonOutput.
func ParsePeriodComparisonRow(columns []ColumnInfo, rows [][]interface{}, measureCols []string, trendThresholdPercent float64) (*PeriodComparisonOutput, error) {
	if len(rows) == 0 {
		return nil, nil
	}
	if trendThresholdPercent <= 0 {
		trendThresholdPercent = 0.5
	}

	colIndex := make(map[string]int, len(columns))
	for i, c := range columns {
		colIndex[strings.ToLower(c.Name)] = i
	}

	row := rows[0]
	currentLabel := periodLabelAt(colIndex, row, "pgqn_period_label")
	previousLabel := periodLabelAt(colIndex, row, "pgqn_previous_period_label")
	if currentLabel == "" || previousLabel == "" {
		return nil, nil
	}

	out := &PeriodComparisonOutput{
		CurrentPeriodLabel:  currentLabel,
		PreviousPeriodLabel: previousLabel,
		Comparisons:         make([]PeriodComparison, 0, len(measureCols)),
	}

	for i, measure := range measureCols {
		current, okCurrent := numericAt(colIndex, row, fmt.Sprintf("pgqn_m%d_current", i))
		previous, okPrevious := numericAt(colIndex, row, fmt.Sprintf("pgqn_m%d_previous", i))
		if !okCurrent || !okPrevious {
			continue
		}

		pc := PeriodComparison{
			Measure:  measure,
			Current:  current,
			Previous: &previous,
			Trend:    "flat",
		}
		change := current - previous
		pc.Change = &change
		if previous != 0 {
			pct := (change / previous) * 100
			pc.ChangePercentage = &pct
			pc.Trend = metrics.TrendFromChangePct(&pct, trendThresholdPercent)
		}
		out.Comparisons = append(out.Comparisons, pc)
	}

	if len(out.Comparisons) == 0 {
		return nil, nil
	}
	return out, nil
}

func colIdx(colIndex map[string]int, name string) (int, bool) {
	i, ok := colIndex[strings.ToLower(name)]
	return i, ok
}

func numericAt(colIndex map[string]int, row []interface{}, name string) (float64, bool) {
	i, ok := colIdx(colIndex, name)
	if !ok || i >= len(row) {
		return 0, false
	}
	return metrics.GetNumericValue(row[i])
}

func periodLabelAt(colIndex map[string]int, row []interface{}, name string) string {
	i, ok := colIdx(colIndex, name)
	if !ok || i >= len(row) {
		return ""
	}
	return formatPeriodLabel(row[i])
}

func formatPeriodLabel(val interface{}) string {
	if val == nil {
		return ""
	}
	switch v := val.(type) {
	case string:
		return v
	case time.Time:
		return v.Format("2006-01-02")
	case []byte:
		return string(v)
	default:
		if f, ok := metrics.GetNumericValue(val); ok {
			return strconv.FormatFloat(f, 'f', -1, 64)
		}
		s := fmt.Sprint(v)
		if s == "<nil>" {
			return ""
		}
		return s
	}
}

// PeriodColumnsFromProfiles returns the time-series column and measure columns for period comparison.
func PeriodColumnsFromProfiles(columnNames []string, profiles []metrics.ColumnProfile) (timeCol string, measureCols []string) {
	for i, p := range profiles {
		if i >= len(columnNames) {
			break
		}
		if p.IsTimeSeries && timeCol == "" {
			timeCol = columnNames[i]
		}
		if p.IsMeasure {
			measureCols = append(measureCols, columnNames[i])
		}
	}
	return timeCol, measureCols
}

// NearlyEqualPeriodComparison reports whether two period comparison slices match within epsilon.
func NearlyEqualPeriodComparison(a, b []PeriodComparison, epsilon float64) bool {
	if len(a) != len(b) {
		return false
	}
	if epsilon <= 0 {
		epsilon = 1e-6
	}
	byMeasure := make(map[string]PeriodComparison, len(b))
	for _, pc := range b {
		byMeasure[pc.Measure] = pc
	}
	for _, left := range a {
		right, ok := byMeasure[left.Measure]
		if !ok || left.Trend != right.Trend {
			return false
		}
		if math.Abs(left.Current-right.Current) > epsilon {
			return false
		}
		if !ptrNearlyEqual(left.Previous, right.Previous, epsilon) ||
			!ptrNearlyEqual(left.Change, right.Change, epsilon) ||
			!ptrNearlyEqual(left.ChangePercentage, right.ChangePercentage, epsilon) {
			return false
		}
	}
	return true
}

func ptrNearlyEqual(a, b *float64, epsilon float64) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return math.Abs(*a-*b) <= epsilon
}
