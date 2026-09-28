package queryrunner

import (
	"strings"
	"testing"
)

// The planner's cost is an abstract number used to choose between plans. Shown
// as "−26.9×" next to an execution-time row it reads as "26.9 times faster",
// which is a claim the number cannot support.
func TestCostMetricIsNeverAFoldChange(t *testing.T) {
	for _, tc := range []struct {
		name          string
		before, after float64
	}{
		{"large improvement", 873.9, 32.5},
		{"huge improvement", 1_000_000, 10},
		{"regression", 32.5, 873.9},
		{"tiny change", 100, 99.9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := formatCostMetric(tc.before, tc.after)
			if strings.Contains(m.Change, "×") {
				t.Errorf("cost change %q uses a fold-change, which reads as a speed multiple", m.Change)
			}
			if m.Caveat == "" {
				t.Error("the cost row must carry a caveat about its units")
			}
			if !strings.Contains(strings.ToLower(m.Evidence), "estimate") {
				t.Errorf("the label should mark it an estimate, got %q", m.Evidence)
			}
		})
	}
}

func TestCostMetricStillReportsDirection(t *testing.T) {
	better := formatCostMetric(873.9, 32.5)
	if !strings.HasPrefix(better.Change, "−") {
		t.Errorf("an improvement should read as a decrease, got %q", better.Change)
	}
	worse := formatCostMetric(32.5, 873.9)
	if !strings.HasPrefix(worse.Change, "+") {
		t.Errorf("a regression should read as an increase, got %q", worse.Change)
	}
	if got := formatCostMetric(100, 100).Change; got != "equal" {
		t.Errorf("unchanged cost = %q, want equal", got)
	}
}

// Execution time is the row a reader is entitled to treat as a measurement, so
// it must say when it is not one — and when it is only a single sample.
func TestTimingMetricCaveats(t *testing.T) {
	estimated := formatTimingMetric(PlanMetrics{}, PlanMetrics{})
	if estimated.Change != "estimate-only" || estimated.Before != "n/a" {
		t.Fatalf("estimate-only plans must not report a time: %+v", estimated)
	}
	if !strings.Contains(strings.ToLower(estimated.Caveat), "analyze") {
		t.Errorf("the estimate-only caveat should say how to get a real number, got %q", estimated.Caveat)
	}

	observed := formatTimingMetric(
		PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 6},
		PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 0.2},
	)
	if observed.Caveat == "" {
		t.Error("a measured time is still one sample and must say so")
	}
}
