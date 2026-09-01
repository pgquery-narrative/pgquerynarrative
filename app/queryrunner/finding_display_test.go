package queryrunner

import (
	"strings"
	"testing"
)

func TestFindingFingerprint_GroupsPartitionScans(t *testing.T) {
	a := "Sequential scan on demo.sales_2023_01 (estimated cost 12.50) — function-wrapped partition/index key blocks pruning"
	b := "Sequential scan on demo.sales_2024_06 (estimated cost 880000.10) — function-wrapped partition/index key blocks pruning"
	if FindingFingerprint(a) != FindingFingerprint(b) {
		t.Fatalf("expected same fingerprint, got %q vs %q", FindingFingerprint(a), FindingFingerprint(b))
	}
}

func TestCollapseRepeatedFindingMessages(t *testing.T) {
	messages := []string{
		"Sequential scan on demo.sales_2023_01 (estimated cost 12.50) — function-wrapped partition/index key blocks pruning",
		"Sequential scan on demo.sales_2024_06 (estimated cost 880000.10) — function-wrapped partition/index key blocks pruning",
		"Sequential scan on demo.regions (estimated cost 1.00) — likely acceptable for small or unfiltered scans",
	}
	got := CollapseRepeatedFindingMessages(messages)
	if len(got) != 2 {
		t.Fatalf("expected 2 collapsed items, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "×2 similar partition scans") {
		t.Fatalf("expected collapsed partition message, got %q", got[0])
	}
	if strings.Contains(got[0], "estimated cost") {
		t.Fatalf("collapsed message should not include per-partition cost: %q", got[0])
	}
}

func TestFormatCollapsedFinding(t *testing.T) {
	msg := "Sequential scan on demo.sales_2023_01 (estimated cost 12.50) — function-wrapped partition/index key blocks pruning"
	got := FormatCollapsedFinding(msg, 3)
	if !strings.Contains(got, "×3 similar partition scans") {
		t.Fatalf("got %q", got)
	}
}

func TestFindingDisplayRank_PartitionPruningFirst(t *testing.T) {
	if FindingDisplayRank(CategoryPartitionPruning, "append") >= FindingDisplayRank(CategorySeqScan, "scan") {
		t.Fatal("partition pruning should rank before seq scan")
	}
}
