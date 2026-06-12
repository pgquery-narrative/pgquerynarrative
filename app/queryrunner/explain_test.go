package queryrunner

import (
	"encoding/json"
	"strings"
	"testing"
)

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
	totalCost, findings, planJSON, err := parseExplainJSON([]byte(sampleExplainJSON))
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
	_, _, _, err := parseExplainJSON([]byte("not json"))
	if err == nil {
		t.Fatal("expected error for invalid json")
	}
}

func TestInnerQuerySQL(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want string
	}{
		{
			name: "bare select",
			sql:  "SELECT 1 FROM demo.sales",
			want: "SELECT 1 FROM demo.sales",
		},
		{
			name: "explain wrapper",
			sql:  "EXPLAIN (FORMAT JSON) SELECT 1 FROM demo.sales",
			want: "SELECT 1 FROM demo.sales",
		},
		{
			name: "with cte",
			sql:  "WITH cte AS (SELECT 1) SELECT * FROM cte",
			want: "WITH cte AS (SELECT 1) SELECT * FROM cte",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := innerQuerySQL(tt.sql)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
		})
	}
}

func TestBuildExplainSQL(t *testing.T) {
	got := buildExplainSQL("SELECT 1", false)
	if got != "EXPLAIN (FORMAT JSON) SELECT 1" {
		t.Fatalf("got %q", got)
	}
	got = buildExplainSQL("SELECT 1", true)
	if got != "EXPLAIN (ANALYZE, FORMAT JSON) SELECT 1" {
		t.Fatalf("got %q", got)
	}
}

func TestParseExplainJSON_roundTrip(t *testing.T) {
	_, _, planJSON, err := parseExplainJSON([]byte(sampleExplainJSON))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var decoded []map[string]interface{}
	if err := json.Unmarshal(planJSON, &decoded); err != nil {
		t.Fatalf("plan json not valid: %v", err)
	}
}
