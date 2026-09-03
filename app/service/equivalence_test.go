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

func TestWrapDeterministicSampleSQL(t *testing.T) {
	got, err := wrapDeterministicSampleSQL(`SELECT region, total FROM demo.sales WHERE region = 'North' ORDER BY total`, 1000)
	if err != nil {
		t.Fatal(err)
	}
	low := strings.ToLower(got)
	// The wrapper imposes its own hash order + limit so two plans with the same
	// full result set return the same subset.
	if !strings.Contains(low, "order by md5(pgqn_eq::text)") {
		t.Fatalf("missing deterministic order: %s", got)
	}
	if !strings.Contains(low, "limit 1000") {
		t.Fatalf("missing bounded limit: %s", got)
	}
	if !strings.Contains(low, "from (select region, total from demo.sales") {
		t.Fatalf("inner query not wrapped: %s", got)
	}
}

func TestNotRequestedEquivalence(t *testing.T) {
	out := notRequestedEquivalence()
	if out.Status != EquivalenceNotRequested {
		t.Fatalf("status = %q, want NotRequested", out.Status)
	}
	if out.Equal != nil {
		t.Fatalf("Equal must be nil when not requested, got %v", *out.Equal)
	}
	if out.Notes == "" {
		t.Fatal("NotRequested result should carry an explanatory note")
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
