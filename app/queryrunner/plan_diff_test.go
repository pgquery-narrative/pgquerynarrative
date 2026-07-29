package queryrunner

import (
	"encoding/json"
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
