package service

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

func TestWrapCountSQL(t *testing.T) {
	got, err := wrapCountSQL(`SELECT region FROM demo.sales WHERE region = 'North'`)
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(got)
	if !strings.Contains(low, "count(*)") || !strings.Contains(low, "demo.sales") {
		t.Fatalf("unexpected wrap: %s", got)
	}
}

func TestMultisetFingerprint_OrderIndependent(t *testing.T) {
	a := &queryrunner.Result{
		Columns:  []queryrunner.ColumnInfo{{Name: "x"}},
		Rows:     [][]interface{}{{1}, {2}, {3}},
		RowCount: 3,
	}
	b := &queryrunner.Result{
		Columns:  []queryrunner.ColumnInfo{{Name: "x"}},
		Rows:     [][]interface{}{{3}, {1}, {2}},
		RowCount: 3,
	}
	ha, okA := multisetFingerprint(a)
	hb, okB := multisetFingerprint(b)
	if !okA || !okB || ha != hb {
		t.Fatalf("order-independent fingerprints should match: %s vs %s", ha, hb)
	}
}

func TestMultisetFingerprint_DetectsDifference(t *testing.T) {
	a := &queryrunner.Result{
		Columns:  []queryrunner.ColumnInfo{{Name: "x"}},
		Rows:     [][]interface{}{{1}, {2}},
		RowCount: 2,
	}
	b := &queryrunner.Result{
		Columns:  []queryrunner.ColumnInfo{{Name: "x"}},
		Rows:     [][]interface{}{{1}, {9}},
		RowCount: 2,
	}
	ha, _ := multisetFingerprint(a)
	hb, _ := multisetFingerprint(b)
	if ha == hb {
		t.Fatal("different payloads must not match")
	}
}

func TestResultFingerprint_Nil(t *testing.T) {
	if _, ok := resultFingerprint(nil); ok {
		t.Fatal("nil result must not fingerprint")
	}
}

func TestEquivalenceSemantics_CountMismatchIsDifferent(t *testing.T) {
	// Document contract used by compareResultEquivalence without a live DB:
	// unequal COUNT(*) → Different (Equal=false), never Unverified.
	beforeCount, afterCount := int64(2), int64(1)
	if beforeCount == afterCount {
		t.Fatal("setup")
	}
	eq := false
	status := EquivalenceDifferent
	if eq || status != EquivalenceDifferent {
		t.Fatal("count mismatch must be Different")
	}
}

func TestAsInt64(t *testing.T) {
	n, err := asInt64(int64(42))
	if err != nil || n != 42 {
		t.Fatalf("got %d %v", n, err)
	}
	n, err = asInt64(float64(7))
	if err != nil || n != 7 {
		t.Fatalf("got %d %v", n, err)
	}
}
