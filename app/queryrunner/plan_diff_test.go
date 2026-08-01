package queryrunner

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestComparePlans(t *testing.T) {
	before := `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"sales","Schema":"demo","Total Cost":482100,"Plan Rows":48000000,"Plans":[]}}]`
	after := `[{"Plan":{"Node Type":"Bitmap Heap Scan","Relation Name":"sales","Schema":"demo","Total Cost":18400,"Plan Rows":3100000,"Plans":[{"Node Type":"Bitmap Index Scan","Relation Name":"sales","Schema":"demo","Total Cost":100,"Plan Rows":3100000}]}}]`

	cmp, err := ComparePlans(json.RawMessage(before), json.RawMessage(after))
	if err != nil {
		t.Fatalf("ComparePlans: %v", err)
	}
	if len(cmp.Metrics) < 4 {
		t.Fatalf("expected metrics, got %d", len(cmp.Metrics))
	}
	if len(cmp.Diff.Added) == 0 {
		t.Fatal("expected added nodes")
	}
	if cmp.BeforeMetrics.HasSeqScan != true {
		t.Fatal("before should have seq scan")
	}
	if cmp.AfterMetrics.HasSeqScan {
		t.Fatal("after should not have seq scan at root")
	}
}

func TestFormatChangeAvoidsFakeHundredPercent(t *testing.T) {
	got := formatChange(206565.94, 0.03, true)
	if strings.Contains(got, "100.0%") {
		t.Fatalf("got %q, want fold-change not −100.0%%", got)
	}
	if !strings.Contains(got, "×") {
		t.Fatalf("got %q, want fold-change with ×", got)
	}
	gotZero := formatChange(100, 0, true)
	if gotZero == "−100.0%" || strings.Contains(gotZero, "100.0%") {
		t.Fatalf("got %q for after=0", gotZero)
	}
}

func TestFormatCostKeepsFractionalPrecision(t *testing.T) {
	got := formatValue(0.03, "cost")
	if got == "0" || got == "0.0" {
		t.Fatalf("got %q, want fractional cost", got)
	}
}

func TestFormatTimingEstimateOnly(t *testing.T) {
	m := formatTimingMetric(PlanMetrics{}, PlanMetrics{})
	if m.Change != "estimate-only" {
		t.Fatalf("got change %q", m.Change)
	}
}

func TestComparePlansPartitionsMetric(t *testing.T) {
	before := `[{"Plan":{"Node Type":"Append","Total Cost":1000,"Subplans Removed":0,"Plans":[
		{"Node Type":"Seq Scan","Relation Name":"sales_2024_01","Total Cost":100,"Plan Rows":10},
		{"Node Type":"Seq Scan","Relation Name":"sales_2024_02","Total Cost":100,"Plan Rows":10},
		{"Node Type":"Seq Scan","Relation Name":"sales_2024_03","Total Cost":100,"Plan Rows":10}
	]}}]`
	after := `[{"Plan":{"Node Type":"Append","Total Cost":10,"Subplans Removed":2,"Plans":[
		{"Node Type":"Seq Scan","Relation Name":"sales_2024_01","Total Cost":10,"Plan Rows":10}
	]}}]`
	cmp, err := ComparePlans(json.RawMessage(before), json.RawMessage(after))
	if err != nil {
		t.Fatalf("ComparePlans: %v", err)
	}
	var found bool
	for _, m := range cmp.Metrics {
		if m.Evidence == "Partitions scanned" {
			found = true
			if m.Before == "n/a" || m.After == "n/a" {
				t.Fatalf("unexpected partitions metric: %+v", m)
			}
			if !strings.Contains(m.Change, "→") {
				t.Fatalf("expected partition change arrow, got %q", m.Change)
			}
		}
	}
	if !found {
		t.Fatal("missing Partitions scanned metric")
	}
	improved := false
	for _, s := range cmp.Diff.Improved {
		if s == "Partition pruning" {
			improved = true
		}
	}
	if !improved {
		t.Fatalf("expected Partition pruning improvement, got %v", cmp.Diff.Improved)
	}
}
