package service

import (
	"errors"
	"testing"

	investigations "github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	queries "github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

func TestSQLFingerprint(t *testing.T) {
	a := sqlFingerprint("SELECT * FROM t WHERE x = 1")
	b := sqlFingerprint("select   *\nfrom T\twhere x = 1")
	if a != b {
		t.Errorf("fingerprint should ignore case/whitespace: %s vs %s", a, b)
	}
	if a == sqlFingerprint("SELECT * FROM t WHERE x = 2") {
		t.Error("different literal → different fingerprint")
	}
	if len(a) != 16 { // 8 bytes hex-encoded
		t.Errorf("expected 16 hex chars, got %d (%s)", len(a), a)
	}
}

func TestEquivalenceStatusFromComparison(t *testing.T) {
	if got := equivalenceStatusFromComparison(nil); got != EquivalenceUnverified {
		t.Errorf("nil → Unverified, got %q", got)
	}
	eq := EquivalenceSampleMatch
	if got := equivalenceStatusFromComparison(&investigations.ComparePlansResult{ResultEquivalenceStatus: &eq}); got != EquivalenceSampleMatch {
		t.Errorf("explicit status wins, got %q", got)
	}
	if got := equivalenceStatusFromComparison(&investigations.ComparePlansResult{}); got != EquivalenceUnverified {
		t.Errorf("no status, no checksum → Unverified, got %q", got)
	}
	// A comparison stored before this PR carries the literal "Equal".
	legacy := "Equal"
	if got := equivalenceStatusFromComparison(&investigations.ComparePlansResult{ResultEquivalenceStatus: &legacy}); got != EquivalenceVerifiedEqual {
		t.Errorf("legacy \"Equal\" → VerifiedEqual, got %q", got)
	}
	yes, no := true, false
	// Legacy fallback: a stored checksum-equal comparison predates the status
	// string and was only ever a full compare.
	if got := equivalenceStatusFromComparison(&investigations.ComparePlansResult{ResultChecksumEqual: &yes}); got != EquivalenceVerifiedEqual {
		t.Errorf("checksum equal → VerifiedEqual, got %q", got)
	}
	if got := equivalenceStatusFromComparison(&investigations.ComparePlansResult{ResultChecksumEqual: &no}); got != EquivalenceDifferent {
		t.Errorf("checksum unequal → Different, got %q", got)
	}
}

func TestEquivalenceIsShippable(t *testing.T) {
	shippable := map[string]bool{
		EquivalenceVerifiedEqual: true,
		EquivalenceSampleMatch:   true,
		EquivalenceDifferent:     false,
		EquivalenceUnverified:    false,
		EquivalenceNotRequested:  false,
		"":                       false,
	}
	for status, want := range shippable {
		if got := equivalenceIsShippable(status); got != want {
			t.Errorf("equivalenceIsShippable(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestNormalizeInvestigationError(t *testing.T) {
	if normalizeInvestigationError(nil) != nil {
		t.Error("nil stays nil")
	}
	plain := errors.New("boom")
	if normalizeInvestigationError(plain) != plain {
		t.Error("plain error passes through unchanged")
	}
	code := "VALIDATION_ERROR"
	qv := &queries.ValidationError{Name: "validation_error", Message: "bad sql", Code: &code}
	got := normalizeInvestigationError(qv)
	var iv *investigations.ValidationError
	if !errors.As(got, &iv) {
		t.Fatalf("queries.ValidationError should map to investigations.ValidationError, got %T", got)
	}
	if iv.Message != "bad sql" {
		t.Errorf("message not carried over: %q", iv.Message)
	}
}

func TestIsExplainRootArray(t *testing.T) {
	if !isExplainRootArray([]byte("  \n [ {\"Plan\": {}} ]")) {
		t.Error("leading whitespace then [ → true")
	}
	if isExplainRootArray([]byte("{\"Plan\": {}}")) {
		t.Error("object root → false")
	}
	if isExplainRootArray([]byte("   ")) {
		t.Error("all whitespace → false")
	}
}

func TestQueryrunnerPartitionCount(t *testing.T) {
	if got := queryrunnerPartitionCount(queryrunner.PlanMetrics{}); got != 1 {
		t.Errorf("no partition info → 1, got %v", got)
	}
	if got := queryrunnerPartitionCount(queryrunner.PlanMetrics{HasPartitionAppend: true, PartitionsScanned: 7}); got != 7 {
		t.Errorf("partition append → scanned count, got %v", got)
	}
	if got := queryrunnerPartitionCount(queryrunner.PlanMetrics{PartitionsScanned: 3}); got != 3 {
		t.Errorf("scanned without append flag → scanned count, got %v", got)
	}
}

func TestFixTransitionAllowed(t *testing.T) {
	allowed := [][2]string{
		{"proposed", "verified"}, {"proposed", "applied"}, {"proposed", "abandoned"},
		{"verified", "applied"}, {"verified", "proposed"},
		{"applied", "confirmed"}, {"applied", "regressed"}, {"applied", "verified"},
		{"regressed", "applied"}, {"confirmed", "applied"}, {"abandoned", "proposed"},
	}
	for _, tr := range allowed {
		if !fixTransitionAllowed(tr[0], tr[1]) {
			t.Errorf("expected %s → %s to be allowed", tr[0], tr[1])
		}
	}
	denied := [][2]string{
		{"proposed", "confirmed"}, {"proposed", "regressed"},
		{"confirmed", "verified"}, {"abandoned", "applied"},
		{"regressed", "confirmed"}, {"applied", "proposed"},
		{"bogus", "applied"},
	}
	for _, tr := range denied {
		if fixTransitionAllowed(tr[0], tr[1]) {
			t.Errorf("expected %s → %s to be denied", tr[0], tr[1])
		}
	}
}
