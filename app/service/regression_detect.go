package service

import (
	"fmt"
	"math"
	"strings"
)

// queryStats is one query's numbers from a single pg_stat_statements poll.
type queryStats struct {
	MeanMs  float64
	TotalMs float64
	Calls   int64
	Rows    int64
}

// baselineStats summarizes a query across the recent baseline window: median
// mean-time, p90 total-time, average calls/rows, and how many polls contributed.
type baselineStats struct {
	MeanMs  float64
	TotalMs float64
	Calls   float64
	Rows    float64
	Polls   int
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

// minBaselinePolls is the floor of baseline polls before a query can alert.
// Below this the "baseline" is too thin to distinguish a regression from noise.
func minBaselinePolls(cfg RegressionPollerConfig) int {
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

// looksLikeStatsReset reports whether the current numbers fell so far below the
// baseline that pg_stat_statements was almost certainly reset — in which case
// the deltas are meaningless and this cycle should be skipped for the query.
func looksLikeStatsReset(cur queryStats, base baselineStats) bool {
	if base.TotalMs <= 0 || base.MeanMs <= 0 {
		return false
	}
	return cur.TotalMs < base.TotalMs*0.5 && cur.MeanMs < base.MeanMs*0.5 && cur.Calls < int64(base.Calls*0.5)
}

// evaluateRegression classifies one query against its rolling baseline. The
// second return is false when there is nothing to alert on.
func evaluateRegression(cur queryStats, base baselineStats, cfg RegressionPollerConfig) (regressionVerdict, bool) {
	if base.Polls < minBaselinePolls(cfg) {
		return regressionVerdict{}, false
	}
	if base.MeanMs <= 0 && base.TotalMs <= 0 {
		return regressionVerdict{}, false
	}
	if looksLikeStatsReset(cur, base) {
		return regressionVerdict{}, false
	}

	threshold := meanThreshold(cfg)
	meanPct := pctChange(base.MeanMs, cur.MeanMs)
	totalPct := pctChange(base.TotalMs, cur.TotalMs)
	callsPct := pctChange(base.Calls, float64(cur.Calls))

	switch {
	case meanPct >= threshold:
		return regressionVerdict{ChangeType: "latency", ChangePct: meanPct, Summary: fmt.Sprintf("+%.0f%% mean latency vs baseline", meanPct)}, true
	case totalPct >= threshold*1.5:
		return regressionVerdict{ChangeType: "total_time", ChangePct: totalPct, Summary: fmt.Sprintf("+%.0f%% total database time vs baseline", totalPct)}, true
	case callsPct >= 200:
		return regressionVerdict{ChangeType: "calls", ChangePct: callsPct, Summary: fmt.Sprintf("+%.0f%% calls vs baseline", callsPct)}, true
	default:
		baseRPC := 0.0
		if base.Calls > 0 {
			baseRPC = base.Rows / base.Calls
		}
		curRPC := rowsPerCall(cur.Rows, cur.Calls)
		if baseRPC > 0 && math.Abs(pctChange(baseRPC, curRPC)) >= 80 {
			d := pctChange(baseRPC, curRPC)
			return regressionVerdict{ChangeType: "rows", ChangePct: d, Summary: fmt.Sprintf("%+.0f%% rows per execution vs baseline", d)}, true
		}
	}
	return regressionVerdict{}, false
}

// hasRecovered reports whether a query's current latency has returned close
// enough to its baseline that an open alert should auto-resolve.
func hasRecovered(cur queryStats, base baselineStats, cfg RegressionPollerConfig) bool {
	if base.Polls < minBaselinePolls(cfg) || base.MeanMs <= 0 {
		return false
	}
	return pctChange(base.MeanMs, cur.MeanMs) <= meanThreshold(cfg)/2
}
