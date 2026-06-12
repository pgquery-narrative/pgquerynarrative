package queryrunner_test

import (
	"strings"
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/app/metrics"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

func TestBuildPeriodComparisonSQL_ContainsLAG(t *testing.T) {
	inner := `SELECT date_trunc('month', date)::date AS month, SUM(total_amount) AS monthly_total FROM demo.sales GROUP BY 1 ORDER BY 1`
	sql, err := queryrunner.BuildPeriodComparisonSQL(inner, "month", []string{"monthly_total"})
	if err != nil {
		t.Fatalf("BuildPeriodComparisonSQL: %v", err)
	}
	if !strings.Contains(sql, "LAG(") {
		t.Errorf("expected LAG window function in SQL, got:\n%s", sql)
	}
	if !strings.Contains(sql, "pgqn_period_label") {
		t.Error("expected pgqn_period_label alias")
	}
	if !strings.Contains(sql, "pgqn_m0_previous") {
		t.Error("expected measure previous column alias")
	}
}

func TestParsePeriodComparisonRow(t *testing.T) {
	columns := []queryrunner.ColumnInfo{
		{Name: "pgqn_period_label"},
		{Name: "pgqn_previous_period_label"},
		{Name: "pgqn_m0_current"},
		{Name: "pgqn_m0_previous"},
		{Name: "pgqn_m0_change"},
		{Name: "pgqn_m0_change_pct"},
	}
	rows := [][]interface{}{
		{
			time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC),
			time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
			120.0,
			80.0,
			40.0,
			50.0,
		},
	}

	out, err := queryrunner.ParsePeriodComparisonRow(columns, rows, []string{"monthly_total"}, 0.5)
	if err != nil {
		t.Fatalf("ParsePeriodComparisonRow: %v", err)
	}
	if out == nil {
		t.Fatal("expected output")
	}
	if out.CurrentPeriodLabel != "2025-02-01" || out.PreviousPeriodLabel != "2025-01-01" {
		t.Errorf("labels = %q / %q", out.CurrentPeriodLabel, out.PreviousPeriodLabel)
	}
	if len(out.Comparisons) != 1 {
		t.Fatalf("expected 1 comparison, got %d", len(out.Comparisons))
	}
	pc := out.Comparisons[0]
	if pc.Measure != "monthly_total" || pc.Current != 120 || pc.Trend != "up" {
		t.Errorf("comparison = %+v", pc)
	}
	if pc.Previous == nil || *pc.Previous != 80 {
		t.Errorf("previous = %v, want 80", pc.Previous)
	}
}

func TestPeriodColumnsFromProfiles(t *testing.T) {
	columns := []string{"month", "monthly_total", "units_sold"}
	profiles := []metrics.ColumnProfile{
		{Name: "month", Type: metrics.ColumnTypeDate, IsTimeSeries: true},
		{Name: "monthly_total", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
		{Name: "units_sold", Type: metrics.ColumnTypeNumeric, IsMeasure: true},
	}
	timeCol, measures := queryrunner.PeriodColumnsFromProfiles(columns, profiles)
	if timeCol != "month" {
		t.Errorf("timeCol = %q, want month", timeCol)
	}
	if len(measures) != 2 || measures[0] != "monthly_total" || measures[1] != "units_sold" {
		t.Errorf("measures = %v", measures)
	}
}

func periodComparisonFromGoMetrics(columnNames []string, rows [][]interface{}) (*queryrunner.PeriodComparisonOutput, error) {
	profiles := metrics.ProfileColumns(columnNames, rows)
	m := metrics.CalculateMetrics(columnNames, rows, profiles, nil)
	if len(m.TimeSeries) == 0 {
		return nil, nil
	}
	out := &queryrunner.PeriodComparisonOutput{
		CurrentPeriodLabel:  m.CurrentPeriodLabel,
		PreviousPeriodLabel: m.PreviousPeriodLabel,
	}
	for measure, ts := range m.TimeSeries {
		pc := queryrunner.PeriodComparison{
			Measure: measure,
			Current: ts.CurrentPeriod,
			Trend:   ts.Trend,
		}
		if ts.PreviousPeriod != nil {
			pc.Previous = ts.PreviousPeriod
		}
		if ts.Change != nil {
			pc.Change = ts.Change
		}
		if ts.ChangePercentage != nil {
			pc.ChangePercentage = ts.ChangePercentage
		}
		out.Comparisons = append(out.Comparisons, pc)
	}
	return out, nil
}

func TestSQLParseMatchesGoMetrics_OnExampleShape(t *testing.T) {
	columns := []string{"month", "monthly_total", "units_sold"}
	rows := [][]interface{}{
		{"2025-01-01", 1000.0, 50.0},
		{"2025-02-01", 1200.0, 60.0},
		{"2025-03-01", 900.0, 45.0},
	}

	goOut, err := periodComparisonFromGoMetrics(columns, rows)
	if err != nil || goOut == nil {
		t.Fatalf("go metrics: out=%v err=%v", goOut, err)
	}

	// Simulate SQL output for the latest period (2025-03 vs 2025-02).
	sqlColumns := []queryrunner.ColumnInfo{
		{Name: "pgqn_period_label"},
		{Name: "pgqn_previous_period_label"},
		{Name: "pgqn_m0_current"},
		{Name: "pgqn_m0_previous"},
		{Name: "pgqn_m0_change"},
		{Name: "pgqn_m0_change_pct"},
		{Name: "pgqn_m1_current"},
		{Name: "pgqn_m1_previous"},
		{Name: "pgqn_m1_change"},
		{Name: "pgqn_m1_change_pct"},
	}
	sqlRows := [][]interface{}{
		{
			"2025-03-01", "2025-02-01",
			900.0, 1200.0, -300.0, -25.0,
			45.0, 60.0, -15.0, -25.0,
		},
	}
	sqlOut, err := queryrunner.ParsePeriodComparisonRow(sqlColumns, sqlRows, []string{"monthly_total", "units_sold"}, 0.5)
	if err != nil || sqlOut == nil {
		t.Fatalf("sql parse: out=%v err=%v", sqlOut, err)
	}

	if goOut.CurrentPeriodLabel != sqlOut.CurrentPeriodLabel || goOut.PreviousPeriodLabel != sqlOut.PreviousPeriodLabel {
		t.Errorf("labels go=%q/%q sql=%q/%q",
			goOut.CurrentPeriodLabel, goOut.PreviousPeriodLabel,
			sqlOut.CurrentPeriodLabel, sqlOut.PreviousPeriodLabel,
		)
	}
	if !queryrunner.NearlyEqualPeriodComparison(goOut.Comparisons, sqlOut.Comparisons, 1e-9) {
		t.Errorf("comparisons differ:\ngo  = %+v\nsql = %+v", goOut.Comparisons, sqlOut.Comparisons)
	}
}
