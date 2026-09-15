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

// A candidate whose last-run timing happens to look faster must not show as
// "Improved: Execution time" when repeated runs prove the gap is inside the
// measurement noise — the same standard the metrics-table row already applies.
func TestComparePlansWithTimings_NoiseSuppressesExecutionTimeImprovement(t *testing.T) {
	before := `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"sales","Schema":"demo","Total Cost":482100,"Plan Rows":48000000,"Actual Total Time":10.2,"Plans":[]}}]`
	after := `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"sales","Schema":"demo","Total Cost":482100,"Plan Rows":48000000,"Actual Total Time":4.5,"Plans":[]}}]`

	// Sanity check: without repeated samples, the last-run numbers alone
	// (10.2ms -> 4.5ms) do read as a speedup.
	single, err := ComparePlans(json.RawMessage(before), json.RawMessage(after))
	if err != nil {
		t.Fatalf("ComparePlans: %v", err)
	}
	if !containsStr(single.Diff.Improved, "Execution time") {
		t.Fatalf("expected the single-sample comparison to claim Execution time improved, got %v", single.Diff.Improved)
	}

	// The repeated samples show the medians are close and the spread is wide
	// (noise ~7ms >= median gap). The claim must not survive.
	noisy, err := ComparePlansWithTimings(json.RawMessage(before), json.RawMessage(after),
		summarizeTimings([]float64{10.2, 2, 9}),
		summarizeTimings([]float64{4.5, 3, 8}),
	)
	if err != nil {
		t.Fatalf("ComparePlansWithTimings: %v", err)
	}
	if containsStr(noisy.Diff.Improved, "Execution time") {
		t.Fatalf("Execution time should not be claimed as improved when spread masks the delta, got %v", noisy.Diff.Improved)
	}

	// A genuine, well-separated improvement (small spread, large gap) must
	// still be reported.
	clear, err := ComparePlansWithTimings(json.RawMessage(before), json.RawMessage(after),
		summarizeTimings([]float64{10.2, 10.0, 10.4}),
		summarizeTimings([]float64{4.5, 4.4, 4.6}),
	)
	if err != nil {
		t.Fatalf("ComparePlansWithTimings: %v", err)
	}
	if !containsStr(clear.Diff.Improved, "Execution time") {
		t.Fatalf("expected Execution time improvement to survive when the gap is well outside the spread, got %v", clear.Diff.Improved)
	}
}

func containsStr(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}

func TestScanRowsProcessed_LoopsAndScanNodeFilter(t *testing.T) {
	// Nested loop: the inner Index Scan reports 3 Actual Rows per loop over 100
	// loops => 300 rows of real work. The Bitmap Index Scan is not a base-relation
	// scan and must not be counted; the outer Seq Scan (100 rows, 1 loop) is.
	plan := `[{"Plan":{"Node Type":"Nested Loop","Actual Rows":300,"Actual Loops":1,"Plans":[
		{"Node Type":"Seq Scan","Relation Name":"a","Actual Rows":100,"Actual Loops":1},
		{"Node Type":"Index Scan","Relation Name":"b","Actual Rows":3,"Actual Loops":100,"Plans":[
			{"Node Type":"Bitmap Index Scan","Relation Name":"b_idx","Actual Rows":3,"Actual Loops":100}]}]}}]`
	root, err := extractPlanRoot(json.RawMessage(plan))
	if err != nil {
		t.Fatal(err)
	}
	m := collectPlanMetrics(root)
	if m.RowsScanned != 400 { // 100*1 (Seq) + 3*100 (Index) ; Bitmap Index excluded
		t.Fatalf("RowsScanned = %v, want 400", m.RowsScanned)
	}
	if m.MaxNodeRows != 300 { // the Nested Loop node
		t.Fatalf("MaxNodeRows = %v, want 300", m.MaxNodeRows)
	}
}

func TestScanRowsProcessed_EstimateOnlyUsesPlanRows(t *testing.T) {
	plan := `[{"Plan":{"Node Type":"Seq Scan","Relation Name":"sales","Plan Rows":48000000,"Plans":[]}}]`
	root, _ := extractPlanRoot(json.RawMessage(plan))
	m := collectPlanMetrics(root)
	if m.RowsScanned != 48000000 {
		t.Fatalf("RowsScanned = %v, want 48000000 (Plan Rows fallback)", m.RowsScanned)
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
