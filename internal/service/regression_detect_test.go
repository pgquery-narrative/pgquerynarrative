package service

import "testing"

func TestIsSelfObservedStatement(t *testing.T) {
	selfObserved := []string{
		"EXPLAIN (FORMAT JSON) SELECT 1",
		"explain analyze select * from demo.sales",
		"SET LOCAL statement_timeout = '5s'",
		"SHOW server_version",
		"BEGIN TRANSACTION READ ONLY",
		"COMMIT",
		"SELECT COUNT(*)::bigint AS pgqn_eq_n FROM (SELECT 1) AS pgqn_eq",
		"SELECT * FROM pg_stat_statements ORDER BY total_exec_time DESC",
		"SELECT hypopg_create_index('CREATE INDEX ...')",
		"SELECT column_name FROM information_schema.columns WHERE table_name = $1",
		"DEALLOCATE ALL",
		"discard all",
	}
	for _, q := range selfObserved {
		if !isSelfObservedStatement(q) {
			t.Errorf("expected self-observed: %q", q)
		}
	}

	appQueries := []string{
		"SELECT product_category, SUM(total_amount) FROM demo.sales WHERE region = $1 GROUP BY 1",
		"UPDATE app_orders SET status = $1 WHERE id = $2",
		"WITH m AS (SELECT 1) SELECT * FROM m",
		"select * from customers where email = $1",
	}
	for _, q := range appQueries {
		if isSelfObservedStatement(q) {
			t.Errorf("app query wrongly flagged self-observed: %q", q)
		}
	}
}

func baseCfg() RegressionPollerConfig {
	return RegressionPollerConfig{MeanTimeThresholdPct: 50, MinBaselinePolls: 3, CriticalThresholdPct: 200, HighThresholdPct: 100}
}

func TestEvaluateRegression(t *testing.T) {
	cfg := baseCfg()

	tests := []struct {
		name     string
		cur      intervalStats
		base     intervalBaseline
		wantType string // "" = no alert
	}{
		{
			name: "interval mean within threshold — no alert",
			cur:  intervalStats{MeanMs: 110, DeltaTotalMs: 11000, DeltaCalls: 100, DeltaRows: 100},
			base: intervalBaseline{MeanMs: 100, DeltaTotalMs: 10000, DeltaCalls: 100, DeltaRows: 100, Intervals: 6},
		},
		{
			// The whole point of #9: cumulative counters can be huge and still
			// represent a flat per-interval latency. Interval mean is unchanged
			// here, so no alert — the old cumulative math would have fired.
			name: "cumulative traffic large but this interval is flat — no alert",
			cur:  intervalStats{MeanMs: 100, DeltaTotalMs: 500000, DeltaCalls: 5000, DeltaRows: 5000},
			base: intervalBaseline{MeanMs: 100, DeltaTotalMs: 500000, DeltaCalls: 5000, DeltaRows: 5000, Intervals: 6},
		},
		{
			name:     "interval mean latency doubled — latency alert",
			cur:      intervalStats{MeanMs: 220, DeltaTotalMs: 22000, DeltaCalls: 100, DeltaRows: 100},
			base:     intervalBaseline{MeanMs: 100, DeltaTotalMs: 10000, DeltaCalls: 100, DeltaRows: 100, Intervals: 6},
			wantType: "latency",
		},
		{
			name: "thin baseline — never alerts even on a big jump",
			cur:  intervalStats{MeanMs: 500, DeltaTotalMs: 50000, DeltaCalls: 100, DeltaRows: 100},
			base: intervalBaseline{MeanMs: 100, DeltaTotalMs: 10000, DeltaCalls: 100, DeltaRows: 100, Intervals: 2},
		},
		{
			name: "no traffic in the current interval — no alert",
			cur:  intervalStats{MeanMs: 0, DeltaTotalMs: 0, DeltaCalls: 0},
			base: intervalBaseline{MeanMs: 100, DeltaTotalMs: 10000, DeltaCalls: 100, DeltaRows: 100, Intervals: 6},
		},
		{
			// 4x calls this interval, each cheaper, interval DB time flat →
			// "calls" is the only rule that fires.
			name:     "call volume spiked without more total time — calls alert",
			cur:      intervalStats{MeanMs: 2.5, DeltaTotalMs: 1000, DeltaCalls: 400, DeltaRows: 400},
			base:     intervalBaseline{MeanMs: 10, DeltaTotalMs: 1000, DeltaCalls: 100, DeltaRows: 100, Intervals: 6},
			wantType: "calls",
		},
		{
			name:     "rows per call blew up — rows alert",
			cur:      intervalStats{MeanMs: 120, DeltaTotalMs: 12000, DeltaCalls: 100, DeltaRows: 100000},
			base:     intervalBaseline{MeanMs: 100, DeltaTotalMs: 10000, DeltaCalls: 100, DeltaRows: 100, Intervals: 6},
			wantType: "rows",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v, ok := evaluateRegression(tc.cur, tc.base, cfg)
			if tc.wantType == "" {
				if ok {
					t.Fatalf("expected no alert, got %+v", v)
				}
				return
			}
			if !ok {
				t.Fatalf("expected a %s alert, got none", tc.wantType)
			}
			if v.ChangeType != tc.wantType {
				t.Fatalf("change type = %q, want %q", v.ChangeType, tc.wantType)
			}
		})
	}
}

func TestHasRecovered(t *testing.T) {
	cfg := baseCfg()
	base := intervalBaseline{MeanMs: 100, DeltaTotalMs: 10000, DeltaCalls: 100, DeltaRows: 100, Intervals: 6}

	if !hasRecovered(intervalStats{MeanMs: 118, DeltaCalls: 100}, base, cfg) {
		t.Error("interval mean within threshold/2 of baseline should count as recovered")
	}
	if hasRecovered(intervalStats{MeanMs: 180, DeltaCalls: 100}, base, cfg) {
		t.Error("interval mean still well above baseline is not recovered")
	}
	if hasRecovered(intervalStats{MeanMs: 100, DeltaCalls: 100}, intervalBaseline{MeanMs: 100, Intervals: 2}, cfg) {
		t.Error("thin baseline should not auto-resolve")
	}
	if hasRecovered(intervalStats{MeanMs: 100, DeltaCalls: 0}, base, cfg) {
		t.Error("no traffic this interval — cannot conclude recovery")
	}
}
