package charts_test

import (
	"testing"
	"time"

	"github.com/pgquerynarrative/pgquerynarrative/internal/charts"
)

func TestSuggest_EmptyColumns(t *testing.T) {
	got := charts.Suggest(nil, nil, nil)
	if got != nil {
		t.Errorf("Suggest(nil, nil, nil) = %v, want nil", got)
	}
	got = charts.Suggest([]string{}, []string{}, [][]interface{}{})
	if got != nil {
		t.Errorf("Suggest(empty) = %v, want nil", got)
	}
}

func TestSuggest_TimeSeries(t *testing.T) {
	cols := []string{"month", "revenue"}
	types := []string{"date", "int8"}
	rows := [][]interface{}{
		{time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC), 100.0},
		{time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC), 150.0},
	}
	got := charts.Suggest(cols, types, rows)
	chartTypes := make([]string, len(got))
	for i, s := range got {
		chartTypes[i] = s.ChartType
	}
	hasLine := false
	hasArea := false
	hasTable := false
	for _, c := range chartTypes {
		switch c {
		case "line":
			hasLine = true
		case "area":
			hasArea = true
		case "table":
			hasTable = true
		}
	}
	if !hasLine {
		t.Errorf("time series should suggest line, got %v", chartTypes)
	}
	if !hasArea {
		t.Errorf("time series should suggest area, got %v", chartTypes)
	}
	if !hasTable {
		t.Errorf("should always suggest table, got %v", chartTypes)
	}
}

func TestSuggest_CategoryWithFewValues(t *testing.T) {
	cols := []string{"category", "total"}
	types := []string{"text", "float8"}
	rows := [][]interface{}{
		{"A", 10.0},
		{"B", 20.0},
		{"C", 30.0},
	}
	got := charts.Suggest(cols, types, rows)
	chartTypes := make([]string, len(got))
	for i, s := range got {
		chartTypes[i] = s.ChartType
	}
	hasBar := false
	hasPie := false
	for _, c := range chartTypes {
		if c == "bar" {
			hasBar = true
		}
		if c == "pie" {
			hasPie = true
		}
	}
	if !hasBar {
		t.Errorf("category + numeric should suggest bar, got %v", chartTypes)
	}
	if !hasPie {
		t.Errorf("few categories (2–12) should suggest pie, got %v", chartTypes)
	}
}

func TestSuggest_CategoryWithManyValues_NoPie(t *testing.T) {
	cols := []string{"category", "total"}
	types := []string{"text", "float8"}
	rows := make([][]interface{}, 15)
	for i := range rows {
		rows[i] = []interface{}{string(rune('A' + i)), float64(i * 10)}
	}
	got := charts.Suggest(cols, types, rows)
	for _, s := range got {
		if s.ChartType == "pie" {
			t.Errorf("many categories (>12) should not suggest pie, got chart types including pie")
			break
		}
	}
}

func TestSuggest_TableAlwaysLast(t *testing.T) {
	cols := []string{"x", "y"}
	types := []string{"text", "float8"}
	rows := [][]interface{}{{"a", 1.0}}
	got := charts.Suggest(cols, types, rows)
	if len(got) == 0 {
		t.Fatal("expected at least one suggestion")
	}
	if got[len(got)-1].ChartType != "table" {
		t.Errorf("table should be last suggestion, got last %q", got[len(got)-1].ChartType)
	}
}
