package service

import (
	"encoding/json"
	"testing"

	investigations "github.com/pgquerynarrative/pgquerynarrative/api/gen/investigations"
	queries "github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// explainPlan builds the inner plan object the Explain API returns (not the
// top-level EXPLAIN array), optionally carrying ANALYZE timings.
func explainPlan(actualTotalTime float64) map[string]any {
	p := map[string]any{
		"Node Type":     "Seq Scan",
		"Relation Name": "sales",
		"Total Cost":    1234.5,
		"Plan Rows":     1000.0,
	}
	if actualTotalTime > 0 {
		p["Actual Total Time"] = actualTotalTime
		p["Actual Rows"] = 1000.0
		p["Actual Loops"] = 1.0
	}
	return p
}

func TestMetricsFromExplainAPI(t *testing.T) {
	t.Run("nil explain is an error, not a zero-value plan", func(t *testing.T) {
		if _, err := metricsFromExplainAPI(nil); err == nil {
			t.Fatal("expected an error for a missing plan")
		}
	})

	t.Run("wraps a bare inner plan into the EXPLAIN root array", func(t *testing.T) {
		m, err := metricsFromExplainAPI(&queries.ExplainQueryResult{
			Plan:         explainPlan(0),
			TotalCost:    1234.5,
			EvidenceMode: queryrunner.EvidenceEstimated,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.TotalCost != 1234.5 {
			t.Errorf("total cost = %v, want 1234.5", m.TotalCost)
		}
	})

	t.Run("falls back to the API total cost when the plan carries none", func(t *testing.T) {
		m, err := metricsFromExplainAPI(&queries.ExplainQueryResult{
			Plan:         map[string]any{"Node Type": "Result"},
			TotalCost:    88.25,
			EvidenceMode: queryrunner.EvidenceEstimated,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.TotalCost != 88.25 {
			t.Errorf("total cost = %v, want the API value 88.25", m.TotalCost)
		}
	})

	// The whole point of the timing-honesty work: an estimate-only EXPLAIN must
	// never be dressed up with a timing number, no matter what the envelope says.
	t.Run("estimated evidence never yields a timing", func(t *testing.T) {
		wall := 412.0
		m, err := metricsFromExplainAPI(&queries.ExplainQueryResult{
			Plan:                  explainPlan(0),
			TotalCost:             10,
			EvidenceMode:          queryrunner.EvidenceEstimated,
			RequestWallTimeMs:     412,
			ServerExecutionTimeMs: &wall,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if m.HasActualTiming {
			t.Error("an estimate-only plan must not claim observed timing")
		}
		if m.ExecutionTimeMs != 0 {
			t.Errorf("execution time = %v, want 0 for an estimate-only plan", m.ExecutionTimeMs)
		}
	})

	t.Run("observed evidence folds in the server execution time", func(t *testing.T) {
		server := 37.5
		m, err := metricsFromExplainAPI(&queries.ExplainQueryResult{
			Plan:                  map[string]any{"Node Type": "Result", "Total Cost": 10.0},
			TotalCost:             10,
			EvidenceMode:          queryrunner.EvidenceObserved,
			RequestWallTimeMs:     999,
			ServerExecutionTimeMs: &server,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !m.HasActualTiming {
			t.Fatal("observed evidence should mark the metrics as timed")
		}
		if m.ExecutionTimeMs != server {
			t.Errorf("execution time = %v, want the server time %v (never the 999ms wall clock)", m.ExecutionTimeMs, server)
		}
	})

	t.Run("the plan's own actual timing wins over the envelope", func(t *testing.T) {
		envelope := 500.0
		m, err := metricsFromExplainAPI(&queries.ExplainQueryResult{
			Plan:                  explainPlan(12.25),
			TotalCost:             10,
			EvidenceMode:          queryrunner.EvidenceObserved,
			ServerExecutionTimeMs: &envelope,
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !m.HasActualTiming {
			t.Fatal("a plan with Actual Total Time is observed")
		}
		if m.ExecutionTimeMs == envelope {
			t.Error("the envelope must not overwrite timing already read from the plan")
		}
	})
}

func TestPlanBytesPair(t *testing.T) {
	t.Run("either side missing yields no pair", func(t *testing.T) {
		exp := &queries.ExplainQueryResult{Plan: explainPlan(0)}
		if _, _, ok := planBytesPair(nil, exp); ok {
			t.Error("nil before → not ok")
		}
		if _, _, ok := planBytesPair(exp, nil); ok {
			t.Error("nil after → not ok")
		}
	})

	t.Run("both sides are wrapped in the EXPLAIN root array", func(t *testing.T) {
		before := &queries.ExplainQueryResult{Plan: explainPlan(0)}
		after := &queries.ExplainQueryResult{Plan: explainPlan(1)}
		bb, ab, ok := planBytesPair(before, after)
		if !ok {
			t.Fatal("expected a pair")
		}
		for name, raw := range map[string]json.RawMessage{"before": bb, "after": ab} {
			var root []map[string]any
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatalf("%s is not an EXPLAIN root array: %v", name, err)
			}
			if len(root) != 1 || root[0]["Plan"] == nil {
				t.Errorf("%s = %s, want a single {\"Plan\": ...} element", name, raw)
			}
		}
	})
}

func TestQueryrunnerFindingsFromQueriesAPI(t *testing.T) {
	t.Run("nil explain yields no findings", func(t *testing.T) {
		if got := queryrunnerFindingsFromQueriesAPI(nil); got != nil {
			t.Errorf("expected nil, got %v", got)
		}
	})

	t.Run("nil entries are dropped and optional fields are dereferenced", func(t *testing.T) {
		category, confidence := "seq_scan", "high"
		schema, relation := "demo", "sales"
		cost := 4200.0
		exp := &queries.ExplainQueryResult{Findings: []*queries.PlanFinding{
			nil,
			{
				NodeType:       "Seq Scan",
				IsSeqScan:      true,
				Message:        "sequential scan on a large table",
				Evidence:       []string{"rows=1000000"},
				RelatedColumns: []string{"date", "region"},
				Category:       &category,
				Confidence:     &confidence,
				Schema:         &schema,
				Relation:       &relation,
				EstimatedCost:  &cost,
			},
		}}
		got := queryrunnerFindingsFromQueriesAPI(exp)
		if len(got) != 1 {
			t.Fatalf("expected the nil entry to be dropped, got %d findings", len(got))
		}
		f := got[0]
		if f.Category != category || f.Confidence != confidence || f.Schema != schema ||
			f.Relation != relation || f.EstimatedCost != cost {
			t.Errorf("optional fields not carried across: %+v", f)
		}
		if !f.IsSeqScan || f.NodeType != "Seq Scan" {
			t.Errorf("required fields not carried across: %+v", f)
		}
		// The slice must be copied, not aliased into the API struct.
		f.RelatedColumns[0] = "mutated"
		if exp.Findings[1].RelatedColumns[0] != "date" {
			t.Error("related columns alias the API payload")
		}
	})

	t.Run("absent optional fields stay at their zero value", func(t *testing.T) {
		got := queryrunnerFindingsFromQueriesAPI(&queries.ExplainQueryResult{
			Findings: []*queries.PlanFinding{{NodeType: "Index Scan"}},
		})
		if len(got) != 1 {
			t.Fatalf("expected 1 finding, got %d", len(got))
		}
		if got[0].Category != "" || got[0].EstimatedCost != 0 {
			t.Errorf("expected zero values for absent optionals, got %+v", got[0])
		}
	})
}

func TestIndexAdviceFromAPI(t *testing.T) {
	t.Run("nil in, nil out", func(t *testing.T) {
		if got := indexAdviceFromAPI(nil); got != nil {
			t.Errorf("expected nil, got %+v", got)
		}
	})

	t.Run("dereferences optionals and copies slices", func(t *testing.T) {
		benefit, write, storage := "high", "one extra write per row", "~40 MB"
		ddl := "CREATE INDEX CONCURRENTLY ON demo.sales (date)"
		in := &investigations.IndexAdvice{
			RelatedColumns:   []string{"date"},
			Issues:           []string{"seq scan"},
			PotentialBenefit: &benefit,
			WriteCost:        &write,
			StorageCost:      &storage,
			CandidateDdl:     &ddl,
		}
		got := indexAdviceFromAPI(in)
		if got.PotentialBenefit != benefit || got.WriteCost != write ||
			got.StorageCost != storage || got.CandidateDDL != ddl {
			t.Errorf("optional fields not carried across: %+v", got)
		}
		got.RelatedColumns[0] = "mutated"
		if in.RelatedColumns[0] != "date" {
			t.Error("related columns alias the API payload")
		}
	})

	t.Run("absent optionals stay blank", func(t *testing.T) {
		got := indexAdviceFromAPI(&investigations.IndexAdvice{})
		if got.CandidateDDL != "" || got.PotentialBenefit != "" {
			t.Errorf("expected blanks, got %+v", got)
		}
	})
}

func TestPlanDiagnosisToAPI(t *testing.T) {
	cause := queryrunner.DiagnosisCause{
		Category:    "partition_pruning",
		Title:       "No partitions pruned",
		Severity:    queryrunner.SeverityBlocker,
		NodeTypes:   []string{"Append"},
		Evidence:    []string{"Subplans Removed: 0"},
		Detail:      "the predicate is not sargable",
		Fix:         "compare the partition key directly",
		CostShare:   0.82,
		Occurrences: 3,
	}

	t.Run("carries causes, root cause and incidental rollup", func(t *testing.T) {
		root := cause
		d := &queryrunner.Diagnosis{
			RawCount:  9,
			Headline:  "Every partition is scanned",
			Summary:   "50 of 50 partitions read",
			RootCause: &root,
			Causes:    []queryrunner.DiagnosisCause{cause},
			Incidental: &queryrunner.IncidentalRollup{
				Count: 6, Summary: "6 low-severity findings", Categories: []string{"estimate_drift"},
			},
		}
		got := planDiagnosisToAPI(d)
		if got.RawCount != 9 {
			t.Errorf("raw count = %d, want 9", got.RawCount)
		}
		if got.Headline == nil || *got.Headline != d.Headline {
			t.Errorf("headline = %v", got.Headline)
		}
		if got.Summary == nil || *got.Summary != d.Summary {
			t.Errorf("summary = %v", got.Summary)
		}
		if got.RootCause == nil || got.RootCause.Category != cause.Category {
			t.Fatalf("root cause = %+v", got.RootCause)
		}
		if len(got.Causes) != 1 {
			t.Fatalf("expected 1 cause, got %d", len(got.Causes))
		}
		if got.Incidental == nil || got.Incidental.Count != 6 {
			t.Errorf("incidental = %+v", got.Incidental)
		}
	})

	t.Run("blank optional strings are omitted rather than sent as empty", func(t *testing.T) {
		got := planDiagnosisToAPI(&queryrunner.Diagnosis{RawCount: 0})
		if got.Headline != nil || got.Summary != nil {
			t.Errorf("blank strings should be omitted, got headline=%v summary=%v", got.Headline, got.Summary)
		}
		if got.RootCause != nil || got.Incidental != nil {
			t.Error("absent sections should stay nil")
		}
	})
}

func TestPlanCauseToAPI(t *testing.T) {
	t.Run("zero-valued optionals are omitted", func(t *testing.T) {
		got := planCauseToAPI(queryrunner.DiagnosisCause{
			Category: "seq_scan", Title: "Sequential scan", Severity: queryrunner.SeverityContributing,
		})
		if got.Detail != nil || got.Fix != nil || got.CostShare != nil {
			t.Errorf("expected omitted optionals, got %+v", got)
		}
		if got.Severity != string(queryrunner.SeverityContributing) {
			t.Errorf("severity = %q", got.Severity)
		}
	})

	t.Run("a real cost share is surfaced", func(t *testing.T) {
		got := planCauseToAPI(queryrunner.DiagnosisCause{
			Category: "sort", Title: "External sort", Severity: queryrunner.SeverityContributing, CostShare: 0.31,
		})
		if got.CostShare == nil || *got.CostShare != 0.31 {
			t.Errorf("cost share = %v, want 0.31", got.CostShare)
		}
	})
}
