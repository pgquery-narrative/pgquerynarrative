package queryrunner

import (
	"encoding/json"
	"strings"
	"testing"
)

// parseExplainTuple keeps the pre-struct return shape for tests that only need
// total cost / findings / plan JSON.
func parseExplainTuple(b []byte) (float64, []PlanFinding, json.RawMessage, error) {
	p, err := parseExplainJSON(b)
	if err != nil {
		return 0, nil, nil, err
	}
	return p.TotalCost, p.Findings, p.PlanJSON, nil
}

func TestParseExplainJSON_TimingFields(t *testing.T) {
	// ANALYZE output: top-level Planning Time + Execution Time.
	analyzed := `[{"Plan":{"Node Type":"Result","Total Cost":0.01,"Actual Total Time":4.2},"Planning Time":0.87,"Execution Time":5.13}]`
	p, err := parseExplainJSON([]byte(analyzed))
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanningTimeMs != 0.87 || p.ServerExecutionTimeMs != 5.13 {
		t.Fatalf("planning=%v exec=%v, want 0.87 / 5.13", p.PlanningTimeMs, p.ServerExecutionTimeMs)
	}

	// Estimate-only output: Planning Time present, no Execution Time.
	estimated := `[{"Plan":{"Node Type":"Result","Total Cost":0.01},"Planning Time":0.3}]`
	p, err = parseExplainJSON([]byte(estimated))
	if err != nil {
		t.Fatal(err)
	}
	if p.PlanningTimeMs != 0.3 {
		t.Fatalf("planning=%v, want 0.3", p.PlanningTimeMs)
	}
	if p.ServerExecutionTimeMs != 0 {
		t.Fatalf("estimate-only must have ServerExecutionTimeMs 0, got %v", p.ServerExecutionTimeMs)
	}
}

const sampleExplainJSON = `[
  {
    "Plan": {
      "Node Type": "Aggregate",
      "Strategy": "Plain",
      "Partial Mode": "Simple",
      "Parallel Aware": false,
      "Startup Cost": 188.25,
      "Total Cost": 188.27,
      "Plan Rows": 1,
      "Plan Width": 32,
      "Plans": [
        {
          "Node Type": "Seq Scan",
          "Parent Relationship": "Outer",
          "Parallel Aware": false,
          "Relation Name": "sales",
          "Schema": "demo",
          "Alias": "sales",
          "Startup Cost": 0.00,
          "Total Cost": 180.00,
          "Plan Rows": 8000,
          "Plan Width": 32,
          "Filter": "(region = 'North'::text)"
        }
      ]
    },
    "Planning Time": 0.123
  }
]`

func TestParseExplainJSON(t *testing.T) {
	totalCost, findings, planJSON, err := parseExplainTuple([]byte(sampleExplainJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if totalCost != 188.27 {
		t.Fatalf("expected total cost 188.27, got %v", totalCost)
	}
	if len(planJSON) == 0 {
		t.Fatal("expected raw plan json")
	}

	var seqFinding *PlanFinding
	for i := range findings {
		if findings[i].IsSeqScan {
			seqFinding = &findings[i]
			break
		}
	}
	if seqFinding == nil {
		t.Fatal("expected seq scan finding")
	}
	if seqFinding.Relation != "sales" || seqFinding.Schema != "demo" {
		t.Fatalf("unexpected relation: %+v", seqFinding)
	}
	if seqFinding.EstimatedCost != 180.0 {
		t.Fatalf("expected child cost 180, got %v", seqFinding.EstimatedCost)
	}
	if !strings.Contains(seqFinding.Message, "Sequential scan on demo.sales") {
		t.Fatalf("unexpected message: %s", seqFinding.Message)
	}
	if !strings.Contains(seqFinding.Message, "btree index") {
		t.Fatalf("expected index hint in message: %s", seqFinding.Message)
	}
}

func TestParseExplainJSON_invalid(t *testing.T) {
	_, _, _, err := parseExplainTuple([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid json")
	}
}

func TestBuildExplainSQL(t *testing.T) {
	got := buildExplainSQL("SELECT 1", ExplainOptions{})
	if got != "EXPLAIN (FORMAT JSON) SELECT 1" {
		t.Fatalf("got %q", got)
	}
	got = buildExplainSQL("SELECT 1", ExplainOptions{Analyze: true, Buffers: true})
	if got != "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) SELECT 1" {
		t.Fatalf("got %q", got)
	}
	got = buildExplainSQL("SELECT 1", ExplainOptions{Analyze: true})
	if got != "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1" {
		t.Fatalf("got %q", got)
	}
	got = buildExplainSQL("SELECT 1 WHERE x = $1", ExplainOptions{GenericPlan: true})
	if got != "EXPLAIN (GENERIC_PLAN, FORMAT JSON) SELECT 1 WHERE x = $1" {
		t.Fatalf("got %q", got)
	}
	// ANALYZE and GENERIC_PLAN are incompatible — ANALYZE wins if both are set
	// (the caller clears Analyze for parameterized queries).
	got = buildExplainSQL("SELECT 1", ExplainOptions{Analyze: true, GenericPlan: true})
	if got != "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1" {
		t.Fatalf("got %q", got)
	}
}

func TestQueryHasPositionalParams(t *testing.T) {
	yes := []string{
		"SELECT 1 FROM demo.sales WHERE date = $1",
		"SELECT 1 FROM demo.sales WHERE DATE_TRUNC('month', date) BETWEEN $1 AND $2",
	}
	no := []string{
		"SELECT 1 FROM demo.sales WHERE date = DATE '2025-01-01'",
		"SELECT 1 FROM demo.sales WHERE note = 'costs $5'",
		"SELECT 1",
	}
	for _, q := range yes {
		if !queryHasPositionalParams(q) {
			t.Errorf("expected params: %s", q)
		}
	}
	for _, q := range no {
		if queryHasPositionalParams(q) {
			t.Errorf("expected no params: %s", q)
		}
	}
}

func TestPlanFindingMessage_FunctionWraps(t *testing.T) {
	tests := []struct {
		filter     string
		want       string
		confidence string
	}{
		{filter: "(date_trunc('month'::text, date) = '2025-01-01'::date)", want: "function-wrapped", confidence: "high"},
		{filter: "(EXTRACT(year FROM date) = 2025)", want: "function-wrapped", confidence: "high"},
		{filter: "(date::text = '5'::text)", want: "casting the column to text", confidence: "high"},
		{filter: "(region = 'North'::text)", want: "btree index", confidence: "medium"},
	}
	for _, tt := range tests {
		msg, conf := planFindingMessage("Seq Scan", "demo", "sales", tt.filter, 180, true, map[string]interface{}{})
		if conf != tt.confidence {
			t.Errorf("filter %q confidence=%s, want %s", tt.filter, conf, tt.confidence)
		}
		if !strings.Contains(strings.ToLower(msg), strings.ToLower(tt.want)) {
			t.Errorf("filter %q message %q missing %q", tt.filter, msg, tt.want)
		}
	}
}

func TestParseExplainJSON_roundTrip(t *testing.T) {
	_, _, planJSON, err := parseExplainTuple([]byte(sampleExplainJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded []map[string]interface{}
	if err := json.Unmarshal(planJSON, &decoded); err != nil {
		t.Fatalf("plan json not valid: %v", err)
	}
}
