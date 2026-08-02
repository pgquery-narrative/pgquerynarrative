package queryrunner

import (
	"strings"
	"testing"
)

func TestRankScoredCandidates_OrdersByCostThenPartitions(t *testing.T) {
	cands := []ScoredCandidate{
		{Kind: CandidateKindSQLRewrite, Rankable: true, SQL: "b", CostDelta: -10, PartitionsDelta: -1},
		{Kind: CandidateKindSQLRewrite, Rankable: true, SQL: "a", CostDelta: -50, PartitionsDelta: 0},
		{Kind: CandidateKindIndexDDL, Rankable: false, DDL: "CREATE INDEX x ON t(a)"},
		{Kind: CandidateKindSQLRewrite, Rankable: true, SQL: "c", CostDelta: -10, PartitionsDelta: -5},
	}
	got := RankScoredCandidates(cands)
	if len(got) != 4 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].SQL != "a" || got[0].Rank != 1 {
		t.Fatalf("rank1 = %+v", got[0])
	}
	if got[1].SQL != "c" || got[1].Rank != 2 {
		t.Fatalf("rank2 should prefer larger partition drop: %+v", got[1])
	}
	if got[2].SQL != "b" || got[2].Rank != 3 {
		t.Fatalf("rank3 = %+v", got[2])
	}
	if got[3].Kind != CandidateKindIndexDDL || got[3].Rank != 0 {
		t.Fatalf("DDL should be unranked at end: %+v", got[3])
	}
}

func TestCollectIndexDDLCandidates_Dedupes(t *testing.T) {
	ddl := "CREATE INDEX CONCURRENTLY idx ON demo.sales (region)"
	findings := []PlanFinding{
		{IndexAdvice: &IndexAdvice{CandidateDDL: ddl, Issues: []string{"no_covering_index"}, PotentialBenefit: "avoid seq scan"}},
		{IndexAdvice: &IndexAdvice{CandidateDDL: ddl, Issues: []string{"no_covering_index"}}},
		{Message: "no advice"},
	}
	got := CollectIndexDDLCandidates(findings)
	if len(got) != 1 {
		t.Fatalf("got %d, want 1", len(got))
	}
	if got[0].Kind != CandidateKindIndexDDL || !strings.Contains(got[0].Rationale, "avoid seq scan") {
		t.Fatalf("%+v", got[0])
	}
	if got[0].Rankable {
		t.Fatal("DDL must not be rankable")
	}
}

func TestScoreSQLRewrite_Deltas(t *testing.T) {
	before := PlanMetrics{TotalCost: 100, PartitionsScanned: 12, HasPartitionAppend: true}
	after := PlanMetrics{TotalCost: 20, PartitionsScanned: 1, HasPartitionAppend: true}
	sc := ScoreSQLRewrite(RewriteCandidate{SQL: "SELECT 1", Rationale: "r", Category: "function_wrap"}, before, after, []string{"Partition pruning"})
	if sc.CostDelta != -80 {
		t.Fatalf("CostDelta=%v", sc.CostDelta)
	}
	if sc.PartitionsDelta != -11 {
		t.Fatalf("PartitionsDelta=%v", sc.PartitionsDelta)
	}
	if len(sc.Improved) != 1 {
		t.Fatalf("Improved=%v", sc.Improved)
	}
}

func TestMetricsFromPlan_Basic(t *testing.T) {
	plan := []byte(`[{"Plan":{"Node Type":"Seq Scan","Total Cost":42.5,"Plan Rows":10}}]`)
	m, err := MetricsFromPlan(plan)
	if err != nil {
		t.Fatal(err)
	}
	if m.TotalCost != 42.5 {
		t.Fatalf("TotalCost=%v", m.TotalCost)
	}
}
