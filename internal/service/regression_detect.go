package service

import (
	"fmt"
	"math"
	"strings"
)

// intervalStats is one query's traffic between two consecutive polls: the deltas
// of the cumulative pg_stat_statements counters, plus the mean latency over just
// that interval's calls. Cumulative counters only ever grow, so comparing them
// directly drifts with elapsed time; an interval is what actually changed
// between two observations.
type intervalStats struct {
	DeltaCalls   int64
	DeltaTotalMs float64
	DeltaRows    int64
	MeanMs       float64 // DeltaTotalMs / DeltaCalls; 0 when DeltaCalls == 0
}

// intervalBaseline summarizes a query's recent intervals (excluding the current
// one): median interval mean-latency, p90 interval DB-time, average interval
// call/row counts, and how many intervals contributed.
type intervalBaseline struct {
	MeanMs       float64 // median of interval MeanMs
	DeltaTotalMs float64 // p90 of interval DeltaTotalMs
	DeltaCalls   float64 // avg interval DeltaCalls
	DeltaRows    float64 // avg interval DeltaRows
	Intervals    int
}

// regressionVerdict is the classification of a single regressing query.
type regressionVerdict struct {
	ChangeType string
	ChangePct  float64
	Summary    string
}

// selfObservedPrefixes are statement kinds PgQueryNarrative itself issues against
// an analyzed connection (EXPLAIN, session setup, catalog introspection). They
// must never be treated as application regressions.
var selfObservedPrefixes = []string{
	"explain", "set ", "set\t", "show ", "show\t", "begin", "commit", "rollback",
	"deallocate", "discard", "reset", "prepare ", "execute ", "fetch ", "close ",
	"declare ", "lock ", "listen ", "unlisten", "notify ",
}

// selfObservedContains are fragments unique to PgQueryNarrative's own analysis
// traffic — pg_stat_statements reads, hypopg calls, the equivalence wrappers,
// and the schema-browser catalog probe.
var selfObservedContains = []string{
	"pg_stat_statements", "hypopg", "pgqn_eq", "pgqn_sub",
	"information_schema.columns", "pg_catalog.pg_get_indexdef",
}

// isSelfObservedStatement reports whether queryText is PgQueryNarrative's own
// analysis traffic rather than an application query.
func isSelfObservedStatement(queryText string) bool {
	q := strings.ToLower(strings.TrimSpace(queryText))
	if q == "" {
		return false
	}
	for _, p := range selfObservedPrefixes {
		if strings.HasPrefix(q, p) {
			return true
		}
	}
	for _, c := range selfObservedContains {
		if strings.Contains(q, c) {
			return true
		}
	}
	return false
}

// minBaselineIntervals is the floor of prior intervals a query needs before it
// can alert. N polls give N-1 intervals and the latest one is "current", so the
// baseline holds N-2. Below this the baseline is too thin to tell a regression
// from noise.
func minBaselineIntervals(cfg RegressionPollerConfig) int {
	if cfg.MinBaselinePolls > 0 {
		return cfg.MinBaselinePolls
	}
	return 3
}

func meanThreshold(cfg RegressionPollerConfig) float64 {
	if cfg.MeanTimeThresholdPct > 0 {
		return cfg.MeanTimeThresholdPct
	}
	return 50
}

// evaluateRegression classifies one query's most recent interval against its
// rolling interval baseline. The second return is false when there is nothing
// to alert on.
func evaluateRegression(cur intervalStats, base intervalBaseline, cfg RegressionPollerConfig) (regressionVerdict, bool) {
	if base.Intervals < minBaselineIntervals(cfg) {
		return regressionVerdict{}, false
	}
	// No traffic in the current interval (or a counter reset, which the SQL
	// filters to a non-positive delta) — nothing comparable.
	if cur.DeltaCalls <= 0 {
		return regressionVerdict{}, false
	}
	if base.MeanMs <= 0 && base.DeltaTotalMs <= 0 {
		return regressionVerdict{}, false
	}

	threshold := meanThreshold(cfg)
	meanPct := pctChange(base.MeanMs, cur.MeanMs)
	totalPct := pctChange(base.DeltaTotalMs, cur.DeltaTotalMs)
	callsPct := pctChange(base.DeltaCalls, float64(cur.DeltaCalls))

	switch {
	case base.MeanMs > 0 && meanPct >= threshold:
		return regressionVerdict{ChangeType: "latency", ChangePct: meanPct,
			Summary: fmt.Sprintf("+%.0f%% mean latency this interval vs baseline", meanPct)}, true
	case base.DeltaTotalMs > 0 && totalPct >= threshold*1.5:
		return regressionVerdict{ChangeType: "total_time", ChangePct: totalPct,
			Summary: fmt.Sprintf("+%.0f%% database time this interval vs baseline", totalPct)}, true
	case base.DeltaCalls > 0 && callsPct >= 200:
		return regressionVerdict{ChangeType: "calls", ChangePct: callsPct,
			Summary: fmt.Sprintf("+%.0f%% calls this interval vs baseline", callsPct)}, true
	default:
		baseRPC := 0.0
		if base.DeltaCalls > 0 {
			baseRPC = base.DeltaRows / base.DeltaCalls
		}
		curRPC := rowsPerCall(cur.DeltaRows, cur.DeltaCalls)
		if baseRPC > 0 && math.Abs(pctChange(baseRPC, curRPC)) >= 80 {
			d := pctChange(baseRPC, curRPC)
			return regressionVerdict{ChangeType: "rows", ChangePct: d,
				Summary: fmt.Sprintf("%+.0f%% rows per execution this interval vs baseline", d)}, true
		}
	}
	return regressionVerdict{}, false
}

// hasRecovered reports whether a query's most recent interval latency has
// returned close enough to its baseline that an open alert should auto-resolve.
func hasRecovered(cur intervalStats, base intervalBaseline, cfg RegressionPollerConfig) bool {
	if base.Intervals < minBaselineIntervals(cfg) || base.MeanMs <= 0 || cur.DeltaCalls <= 0 {
		return false
	}
	return pctChange(base.MeanMs, cur.MeanMs) <= meanThreshold(cfg)/2
}
