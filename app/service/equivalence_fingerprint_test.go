package service

import (
	"strings"
	"testing"
)

func TestWrapFingerprintSQL(t *testing.T) {
	got, err := wrapFingerprintSQL("SELECT id, region FROM demo.sales WHERE region = 'North'")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The whole point is that this replaced a sort. If one reappears here, the
	// cost of verification goes back to being proportional to a full sort of a
	// result the user already thinks is too slow.
	if strings.Contains(strings.ToUpper(got), "ORDER BY") {
		t.Errorf("fingerprint SQL must not sort: %s", got)
	}
	if strings.Contains(strings.ToUpper(got), "LIMIT") {
		t.Errorf("fingerprint SQL must not sample: %s", got)
	}
	for _, want := range []string{"count(*)", "sum(", "bit_xor(", "hashtextextended"} {
		if !strings.Contains(got, want) {
			t.Errorf("fingerprint SQL missing %q: %s", want, got)
		}
	}
	if !strings.Contains(got, "FROM demo.sales") {
		t.Errorf("inner query lost: %s", got)
	}
}

func TestWrapFingerprintSQLRejectsWrites(t *testing.T) {
	for _, sql := range []string{
		"DELETE FROM demo.sales",
		"UPDATE demo.sales SET region = 'x'",
		"DROP TABLE demo.sales",
	} {
		if _, err := wrapFingerprintSQL(sql); err == nil {
			t.Errorf("expected %q to be rejected", sql)
		}
	}
	if _, err := wrapFingerprintSQL("   "); err == nil {
		t.Error("expected blank SQL to be rejected")
	}
}

func TestAggregateFingerprintEquality(t *testing.T) {
	base := aggregateFingerprint{Count: 5, Sum: "-9059239638504963042", Xor: -9112442938751334206, Sum2: "77", Xor2: 88}

	if !base.equal(base) {
		t.Error("a fingerprint must equal itself")
	}

	// Each component has to matter on its own: count catches cardinality, sum
	// catches changed values, and xor catches a transposition that happens to
	// leave the sum intact.
	for name, other := range map[string]aggregateFingerprint{
		"count differs": {Count: 6, Sum: base.Sum, Xor: base.Xor, Sum2: base.Sum2, Xor2: base.Xor2},
		"sum differs":   {Count: 5, Sum: "1", Xor: base.Xor, Sum2: base.Sum2, Xor2: base.Xor2},
		"xor differs":   {Count: 5, Sum: base.Sum, Xor: 1, Sum2: base.Sum2, Xor2: base.Xor2},
		"second sum":    {Count: 5, Sum: base.Sum, Xor: base.Xor, Sum2: "1", Xor2: base.Xor2},
		"second xor":    {Count: 5, Sum: base.Sum, Xor: base.Xor, Sum2: base.Sum2, Xor2: 1},
	} {
		if base.equal(other) {
			t.Errorf("%s: fingerprints must not compare equal", name)
		}
	}
}
