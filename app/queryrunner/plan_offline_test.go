package queryrunner

import (
	"context"
	"testing"
)

const offlineSeqScanPlan = `[{"Plan": {"Node Type": "Seq Scan", "Relation Name": "orders", "Schema": "shop", "Alias": "orders",
 "Startup Cost": 0.0, "Total Cost": 24373.0, "Plan Rows": 7500, "Plan Width": 60,
 "Filter": "(date_trunc('day'::text, orders.created_at) = '2025-03-01 00:00:00+00'::timestamp with time zone)"}}]`

func TestAnalyzePlanJSONWithoutAPool(t *testing.T) {
	a, err := AnalyzePlanJSON(context.Background(), nil, []byte(offlineSeqScanPlan))
	if err != nil {
		t.Fatalf("AnalyzePlanJSON: %v", err)
	}
	if a.TotalCost != 24373 {
		t.Errorf("TotalCost = %v, want 24373", a.TotalCost)
	}
	if !a.Metrics.HasSeqScan {
		t.Errorf("metrics should record the sequential scan")
	}
	var seq bool
	for _, f := range a.Findings {
		if f.IsSeqScan && f.Relation == "orders" {
			seq = true
		}
	}
	if !seq {
		t.Fatalf("expected a sequential scan finding on orders, got %+v", a.Findings)
	}
}

func TestAnalyzePlanJSONRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "not json", "[]", `[{"Nope": 1}]`} {
		if _, err := AnalyzePlanJSON(context.Background(), nil, []byte(in)); err == nil {
			t.Errorf("AnalyzePlanJSON(%q) should fail", in)
		}
	}
}

func TestOfflineRewriteAndCompare(t *testing.T) {
	a, err := AnalyzePlanJSON(context.Background(), nil, []byte(offlineSeqScanPlan))
	if err != nil {
		t.Fatal(err)
	}
	sql := "SELECT * FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-01'"
	cands := SuggestRewrites(sql, a.Findings)
	if len(cands) == 0 {
		t.Fatal("expected a sargable rewrite for date_trunc")
	}
}
