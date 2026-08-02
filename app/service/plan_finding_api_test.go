package service

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

func TestPlanFindingToAPI_IncludesIndexAdvice(t *testing.T) {
	ddl := "CREATE INDEX CONCURRENTLY IF NOT EXISTS demo_sales_region_idx ON demo.sales (region)"
	f := queryrunner.PlanFinding{
		NodeType:       "Seq Scan",
		Schema:         "demo",
		Relation:       "sales",
		EstimatedCost:  1200,
		IsSeqScan:      true,
		Category:       queryrunner.CategorySeqScan,
		Confidence:     "high",
		Message:        "Sequential scan on demo.sales",
		Evidence:       []string{"Plan Rows=8000"},
		RelatedColumns: []string{"region"},
		IndexAdvice: &queryrunner.IndexAdvice{
			RelatedColumns:   []string{"region"},
			Issues:           []string{"no_covering_index"},
			PotentialBenefit: "Index scan may avoid full table read",
			WriteCost:        "Extra index maintenance on writes",
			StorageCost:      "Additional index size on disk",
			CandidateDDL:     ddl,
			RelatedIndexes: []queryrunner.IndexDefinition{{
				Name:       "sales_pkey",
				Definition: "CREATE UNIQUE INDEX sales_pkey ON demo.sales USING btree (id)",
				KeyColumns: []string{"id"},
				IsUnique:   true,
				IsPrimary:  true,
				IsValid:    true,
				SizeBytes:  8192,
				IndexScans: 10,
			}},
		},
	}

	pf := planFindingToAPI(f)
	if pf.IndexAdvice == nil {
		t.Fatal("expected IndexAdvice to be mapped")
	}
	if pf.IndexAdvice.CandidateDdl == nil || *pf.IndexAdvice.CandidateDdl != ddl {
		t.Fatalf("CandidateDdl = %v, want %q", pf.IndexAdvice.CandidateDdl, ddl)
	}
	if len(pf.RelatedColumns) != 1 || pf.RelatedColumns[0] != "region" {
		t.Fatalf("RelatedColumns = %v", pf.RelatedColumns)
	}
	if len(pf.IndexAdvice.Issues) != 1 || pf.IndexAdvice.Issues[0] != "no_covering_index" {
		t.Fatalf("Issues = %v", pf.IndexAdvice.Issues)
	}
	if pf.IndexAdvice.PotentialBenefit == nil || !strings.Contains(*pf.IndexAdvice.PotentialBenefit, "Index scan") {
		t.Fatalf("PotentialBenefit = %v", pf.IndexAdvice.PotentialBenefit)
	}
	if len(pf.IndexAdvice.RelatedIndexes) != 1 || pf.IndexAdvice.RelatedIndexes[0].Name != "sales_pkey" {
		t.Fatalf("RelatedIndexes = %#v", pf.IndexAdvice.RelatedIndexes)
	}
}

func TestPlanFindingToAPI_NilAdvice(t *testing.T) {
	pf := planFindingToAPI(queryrunner.PlanFinding{
		NodeType:  "Sort",
		IsSeqScan: false,
		Message:   "Sort",
	})
	if pf.IndexAdvice != nil {
		t.Fatalf("expected nil IndexAdvice, got %#v", pf.IndexAdvice)
	}
}

func TestExplainResultToAPI_PreservesAdvice(t *testing.T) {
	result := &queryrunner.ExplainResult{
		SQL:       "SELECT 1",
		TotalCost: 1,
		Findings: []queryrunner.PlanFinding{{
			NodeType:  "Seq Scan",
			IsSeqScan: true,
			Message:   "seq",
			IndexAdvice: &queryrunner.IndexAdvice{
				Issues:       []string{"no_covering_index"},
				CandidateDDL: "CREATE INDEX x ON demo.t (a)",
			},
		}},
	}
	api := explainResultToAPI(result)
	if len(api.Findings) != 1 || api.Findings[0].IndexAdvice == nil {
		t.Fatalf("IndexAdvice dropped: %#v", api.Findings)
	}
	if api.Findings[0].IndexAdvice.CandidateDdl == nil {
		t.Fatal("CandidateDdl nil")
	}
}
