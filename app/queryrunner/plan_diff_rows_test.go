package queryrunner

import (
	"strings"
	"testing"
)

// "Rows scanned" sums rows returned by base-table scan nodes, not partitions
// touched — a real 50-of-50 to 3-of-50 partition-pruning win can leave this
// number unchanged if only a few of the pruned partitions held matching rows
// to begin with, which reads as "no improvement" without the caveat.
func TestRowsScannedMetricCarriesCaveat(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after float64
	}{
		{"unchanged despite pruning", 500, 500},
		{"decreased", 5000, 200},
		{"increased", 200, 5000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := formatRowsScannedMetric(tc.before, tc.after)
			if m.Evidence != "Rows scanned" {
				t.Errorf("label = %q, want %q", m.Evidence, "Rows scanned")
			}
			if m.Caveat == "" {
				t.Fatal("the rows-scanned row must carry a caveat")
			}
			if !strings.Contains(strings.ToLower(m.Caveat), "partition") {
				t.Errorf("the caveat should point at Partitions scanned for the pruning effect, got %q", m.Caveat)
			}
		})
	}
}

func TestRowsScannedMetricStillReportsDirection(t *testing.T) {
	if got := formatRowsScannedMetric(500, 500).Change; got != "equal" {
		t.Errorf("unchanged rows scanned = %q, want equal", got)
	}
	if got := formatRowsScannedMetric(5000, 200).Change; !strings.HasPrefix(got, "−") {
		t.Errorf("fewer rows scanned should read as a decrease, got %q", got)
	}
}

// "Max node rows" can legitimately increase in a faster plan (e.g. fewer,
// larger scans replacing many small parallel ones), so a rise here alone must
// not read as evidence the candidate is worse.
func TestMaxNodeRowsMetricCarriesCaveat(t *testing.T) {
	m := formatMaxNodeRowsMetric(200, 5000)
	if m.Evidence != "Max node rows" {
		t.Errorf("label = %q, want %q", m.Evidence, "Max node rows")
	}
	if m.Caveat == "" {
		t.Fatal("the max-node-rows row must carry a caveat")
	}
	if !strings.HasPrefix(m.Change, "+") {
		t.Errorf("an increase should still read as an increase, got %q", m.Change)
	}
}
