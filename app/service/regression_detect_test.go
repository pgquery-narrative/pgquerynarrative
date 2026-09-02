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
		cur      queryStats
		base     baselineStats
		wantType string // "" = no alert
	}{
		{
			name: "normal variance under threshold — no alert",
			cur:  queryStats{MeanMs: 110, TotalMs: 1100, Calls: 100, Rows: 100},
			base: baselineStats{MeanMs: 100, TotalMs: 1000, Calls: 100, Rows: 100, Polls: 8},
		},
		{
			name:     "mean latency doubled — latency alert",
			cur:      queryStats{MeanMs: 220, TotalMs: 2200, Calls: 100, Rows: 100},
			base:     baselineStats{MeanMs: 100, TotalMs: 1000, Calls: 100, Rows: 100, Polls: 8},
			wantType: "latency",
		},
		{
			name: "thin baseline — never alerts even on a big jump",
			cur:  queryStats{MeanMs: 500, TotalMs: 5000, Calls: 100, Rows: 100},
			base: baselineStats{MeanMs: 100, TotalMs: 1000, Calls: 100, Rows: 100, Polls: 2},
		},
		{
			name: "stats reset — everything collapsed, no alert",
			cur:  queryStats{MeanMs: 5, TotalMs: 20, Calls: 4},
			base: baselineStats{MeanMs: 100, TotalMs: 1000, Calls: 100, Rows: 100, Polls: 8},
		},
		{
			// Calls 4x but each call got cheaper, so total DB time is flat —
			// "calls" is the only rule that fires.
			name:     "call volume spiked without more total time — calls alert",
			cur:      queryStats{MeanMs: 2.4, TotalMs: 960, Calls: 400, Rows: 400},
			base:     baselineStats{MeanMs: 10, TotalMs: 1000, Calls: 100, Rows: 100, Polls: 8},
			wantType: "calls",
		},
		{
			name:     "rows per call blew up — rows alert",
			cur:      queryStats{MeanMs: 120, TotalMs: 1200, Calls: 100, Rows: 100000},
			base:     baselineStats{MeanMs: 100, TotalMs: 1000, Calls: 100, Rows: 100, Polls: 8},
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
	base := baselineStats{MeanMs: 100, TotalMs: 1000, Calls: 100, Rows: 100, Polls: 8}

	if !hasRecovered(queryStats{MeanMs: 118}, base, cfg) {
		t.Error("mean within threshold/2 of baseline should count as recovered")
	}
	if hasRecovered(queryStats{MeanMs: 180}, base, cfg) {
		t.Error("mean still well above baseline is not recovered")
	}
	if hasRecovered(queryStats{MeanMs: 100}, baselineStats{MeanMs: 100, Polls: 2}, cfg) {
		t.Error("thin baseline should not auto-resolve")
	}
}

func TestLooksLikeStatsReset(t *testing.T) {
	base := baselineStats{MeanMs: 100, TotalMs: 1000, Calls: 100}
	if !looksLikeStatsReset(queryStats{MeanMs: 10, TotalMs: 30, Calls: 3}, base) {
		t.Error("all three metrics collapsed → reset")
	}
	if looksLikeStatsReset(queryStats{MeanMs: 10, TotalMs: 300, Calls: 90}, base) {
		t.Error("only mean dropped — not a reset (could be a genuine fix)")
	}
}
