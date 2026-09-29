package queryrunner

import (
	"strings"
	"testing"
)

func TestSummarizeTimings(t *testing.T) {
	if got := summarizeTimings(nil); got.Samples() != 0 || got.MedianMs != 0 {
		t.Errorf("no samples should summarise to zero, got %+v", got)
	}

	odd := summarizeTimings([]float64{6, 2, 4})
	if odd.MedianMs != 4 || odd.MinMs != 2 || odd.MaxMs != 6 || odd.SpreadMs() != 4 {
		t.Errorf("odd-count summary wrong: %+v", odd)
	}

	even := summarizeTimings([]float64{10, 2, 4, 6})
	if even.MedianMs != 5 {
		t.Errorf("even-count median = %v, want 5", even.MedianMs)
	}

	// The median must resist a single outlier; a mean would not. One cold run at
	// 100ms among fast ones should not become the reported number.
	skewed := summarizeTimings([]float64{2, 2, 2, 2, 100})
	if skewed.MedianMs != 2 {
		t.Errorf("median = %v, want 2 — one slow run must not drag the reported figure", skewed.MedianMs)
	}
}

// A difference smaller than the run-to-run spread is noise, and saying so is the
// entire reason for measuring more than once.
func TestRepeatedTimingRefusesToClaimNoiseIsASpeedup(t *testing.T) {
	bm := PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 5}
	am := PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 4.5}

	noisy := formatRepeatedTimingMetric(bm, am,
		summarizeTimings([]float64{5, 2, 9}), // spread 7ms
		summarizeTimings([]float64{4.5, 3, 8}),
	)
	if !strings.Contains(strings.ToLower(noisy.Caveat), "noise") {
		t.Errorf("a difference inside the spread must be called noise, got %q", noisy.Caveat)
	}

	clean := formatRepeatedTimingMetric(
		PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 100},
		PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 5},
		summarizeTimings([]float64{100, 101, 99}),
		summarizeTimings([]float64{5, 5.5, 4.8}),
	)
	if strings.Contains(strings.ToLower(clean.Caveat), "noise") {
		t.Errorf("a difference far outside the spread is real, got %q", clean.Caveat)
	}
	if !strings.Contains(clean.Before, "runs") {
		t.Errorf("the value should show how many runs it came from: %q", clean.Before)
	}
}

func TestRepeatedTimingFallsBackToSingleSample(t *testing.T) {
	bm := PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 5}
	am := PlanMetrics{HasActualTiming: true, ExecutionTimeMs: 1}
	got := formatRepeatedTimingMetric(bm, am, summarizeTimings([]float64{5}), summarizeTimings([]float64{1}))
	if !strings.Contains(got.Evidence, "Execution time") || strings.Contains(got.Evidence, "median") {
		t.Errorf("one sample per side must not be labelled a median: %q", got.Evidence)
	}
	if got.Caveat == "" {
		t.Error("the single-sample caveat should still apply")
	}
}
