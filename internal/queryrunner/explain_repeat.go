package queryrunner

import (
	"context"
	"sort"
)

// TimingSamples holds repeated ANALYZE measurements of one query.
type TimingSamples struct {
	// Runs is every server-reported execution time, in the order measured.
	Runs []float64
	// MedianMs is the middle sample — preferred over the mean because a single
	// cold-cache or contended run would drag a mean toward a number no
	// individual execution actually produced.
	MedianMs float64
	MinMs    float64
	MaxMs    float64
}

// Samples reports how many measurements were taken.
func (t TimingSamples) Samples() int { return len(t.Runs) }

// SpreadMs is the gap between the fastest and slowest run — the number that says
// whether a reported difference is bigger than the measurement noise.
func (t TimingSamples) SpreadMs() float64 { return t.MaxMs - t.MinMs }

func summarizeTimings(runs []float64) TimingSamples {
	out := TimingSamples{Runs: runs}
	if len(runs) == 0 {
		return out
	}
	sorted := append([]float64(nil), runs...)
	sort.Float64s(sorted)
	out.MinMs = sorted[0]
	out.MaxMs = sorted[len(sorted)-1]
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		out.MedianMs = sorted[mid]
	} else {
		out.MedianMs = (sorted[mid-1] + sorted[mid]) / 2
	}
	return out
}

// ExplainRepeated runs EXPLAIN ANALYZE up to runs times and returns the plan
// from the final run alongside every timing observed.
//
// One ANALYZE is one sample under whatever cache state happened to exist, and on
// small results the run-to-run spread routinely exceeds the difference being
// reported — the same query has measured 2ms and 6ms on consecutive executions.
// Repeating is the only way to tell a real improvement from noise.
//
// Each run executes the query, so this is deliberately bounded and opt-in.
// runs <= 1, or a non-ANALYZE explain, collapses to a single call.
func (r *Runner) ExplainRepeated(ctx context.Context, sql string, analyze bool, runs int) (*ExplainResult, TimingSamples, error) {
	if runs < 1 {
		runs = 1
	}
	if !analyze {
		runs = 1
	}

	var last *ExplainResult
	timings := make([]float64, 0, runs)
	for i := 0; i < runs; i++ {
		res, err := r.Explain(ctx, sql, analyze)
		if err != nil {
			// A later run failing after earlier ones succeeded still yields a
			// usable plan; only report the error when nothing succeeded.
			if last == nil {
				return nil, TimingSamples{}, err
			}
			break
		}
		last = res
		if res.EvidenceMode == EvidenceObserved && res.ServerExecutionTimeMs > 0 {
			timings = append(timings, res.ServerExecutionTimeMs)
		}
	}
	return last, summarizeTimings(timings), nil
}
