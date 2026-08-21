package queryrunner

import (
	"errors"
	"strings"
	"testing"
)

func TestExtractCreateIndexSQL(t *testing.T) {
	ddl := "-- advice\nCREATE INDEX CONCURRENTLY IF NOT EXISTS idx_sales_date ON demo.sales (date);"
	got := ExtractCreateIndexSQL(ddl)
	if !strings.Contains(strings.ToUpper(got), "CREATE INDEX") {
		t.Fatalf("expected CREATE INDEX, got %q", got)
	}
	if strings.Contains(strings.ToUpper(got), "CONCURRENTLY") {
		t.Fatalf("CONCURRENTLY should be stripped for hypopg: %q", got)
	}
}

func TestScoreIndexProjection_HypopgRankableHeuristicReviewOnly(t *testing.T) {
	base := ScoredCandidate{
		Kind:      CandidateKindIndexDDL,
		Rankable:  false,
		DDL:       "CREATE INDEX ON demo.sales (date)",
		Rationale: "add index",
	}
	heuristic := ScoreIndexProjection(base, IndexProjection{
		Method:        IndexProjectionHeuristic,
		BaselineCost:  100,
		ProjectedCost: 30,
		CostDelta:     -70,
		Available:     true,
		Rationale:     "labeled heuristic",
		FailureReason: "extension_missing",
	})
	if heuristic.Rankable {
		t.Fatal("heuristic must stay review-only so it cannot look like hypopg ranking")
	}
	if heuristic.ProjectionMethod != IndexProjectionHeuristic {
		t.Fatalf("method=%s", heuristic.ProjectionMethod)
	}
	if !strings.Contains(heuristic.Rationale, "hypopg_failure=extension_missing") {
		t.Fatalf("rationale should surface failure: %s", heuristic.Rationale)
	}
	if heuristic.CostDelta != -70 || heuristic.TotalCost != 30 {
		t.Fatalf("costs should still be attached for display: %+v", heuristic)
	}

	hypopg := ScoreIndexProjection(base, IndexProjection{
		Method:        IndexProjectionHypopg,
		BaselineCost:  100,
		ProjectedCost: 25,
		CostDelta:     -75,
		Available:     true,
		Rationale:     "hypopg projected",
	})
	if !hypopg.Rankable {
		t.Fatal("hypopg projection must be rankable")
	}
	if hypopg.ProjectionMethod != IndexProjectionHypopg {
		t.Fatalf("method=%s", hypopg.ProjectionMethod)
	}
}

func TestProjectIndexCost_HeuristicWithoutPool(t *testing.T) {
	r := &Runner{} // no pool → hypopg unavailable → heuristic
	proj := r.ProjectIndexCost(t.Context(), "SELECT 1 FROM demo.sales", "CREATE INDEX ON demo.sales (date)", 200)
	if !proj.Available || proj.Method != IndexProjectionHeuristic {
		t.Fatalf("expected heuristic projection, got %+v", proj)
	}
	if proj.FailureReason != "no_pool" {
		t.Fatalf("expected FailureReason=no_pool, got %+v", proj)
	}
	if proj.ProjectedCost >= 200 || proj.CostDelta >= 0 {
		t.Fatalf("expected lower projected cost, got %+v", proj)
	}
}

func TestClassifyHypopgFailure(t *testing.T) {
	if got := classifyHypopgFailure(errors.New("permission denied for function hypopg_create_index")); got != "privilege_denied" {
		t.Fatalf("got %s", got)
	}
	if got := classifyHypopgFailure(errors.New("cannot execute write in a read-only transaction")); got != "read_only_transaction" {
		t.Fatalf("got %s", got)
	}
}
