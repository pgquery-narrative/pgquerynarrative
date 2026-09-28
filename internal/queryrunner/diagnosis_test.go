package queryrunner

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// fixtureFindings is the reduced shape captured from a real investigation.
type fixtureFindings struct {
	TotalCost float64 `json:"total_cost"`
	Findings  []struct {
		NodeType      string  `json:"node_type"`
		Category      string  `json:"category"`
		Message       string  `json:"message"`
		EstimatedCost float64 `json:"estimated_cost"`
		IsSeqScan     bool    `json:"is_seq_scan"`
	} `json:"findings"`
}

func loadDiagnosisFixture(t *testing.T) ([]PlanFinding, PlanMetrics) {
	t.Helper()
	raw, err := os.ReadFile("testdata/diagnosis_real_findings.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var fx fixtureFindings
	if err := json.Unmarshal(raw, &fx); err != nil {
		t.Fatalf("unmarshal fixture: %v", err)
	}
	out := make([]PlanFinding, 0, len(fx.Findings))
	for _, f := range fx.Findings {
		out = append(out, PlanFinding{
			NodeType:      f.NodeType,
			Category:      f.Category,
			Message:       f.Message,
			EstimatedCost: f.EstimatedCost,
			IsSeqScan:     f.IsSeqScan,
		})
	}
	m := PlanMetrics{
		TotalCost:          fx.TotalCost,
		RowsScanned:        15_119_439,
		PartitionsScanned:  50,
		HasPartitionAppend: true,
		RootNodeType:       "Aggregate",
	}
	return out, m
}

func TestDiagnose_CollapsesPartitionedPruningNoise(t *testing.T) {
	findings, metrics := loadDiagnosisFixture(t)
	if len(findings) < 100 {
		t.Fatalf("fixture should carry the noisy real finding set, got %d", len(findings))
	}

	d := Diagnose(findings, metrics)
	if d == nil {
		t.Fatal("expected a diagnosis")
	}
	if d.RawCount != len(findings) {
		t.Errorf("RawCount = %d, want %d", d.RawCount, len(findings))
	}

	// 331 findings collapse to a handful of distinct causes.
	if len(d.Causes) == 0 || len(d.Causes) > 4 {
		t.Fatalf("expected 1-4 ranked causes, got %d: %+v", len(d.Causes), causeTitles(d.Causes))
	}

	// The root cause is the pruning failure, marked as the blocker.
	root := d.RootCause
	if root == nil {
		t.Fatal("expected a root cause")
	}
	if root.Category != CategoryPartitionPruning {
		t.Errorf("root cause category = %q, want %q (titles: %v)", root.Category, CategoryPartitionPruning, causeTitles(d.Causes))
	}
	if root.Severity != SeverityBlocker {
		t.Errorf("root cause severity = %q, want %q", root.Severity, SeverityBlocker)
	}
	if root.CostShare < 0.5 {
		t.Errorf("root cause CostShare = %.2f, want the pruning failure to dominate", root.CostShare)
	}
	if !strings.Contains(strings.ToLower(root.Fix), "sargable") {
		t.Errorf("root cause Fix should point at a sargable rewrite, got %q", root.Fix)
	}

	// The verdict names the failure and the partition count.
	if !strings.Contains(strings.ToLower(d.Headline), "pruning") {
		t.Errorf("headline = %q, want it to mention pruning", d.Headline)
	}
	if !strings.Contains(d.Summary, "50") {
		t.Errorf("summary = %q, want it to state 50 partitions", d.Summary)
	}

	// index_health noise is quarantined out of the ranked causes.
	for _, c := range d.Causes {
		if c.Category == CategoryIndexHealth {
			t.Errorf("index_health must not appear as a ranked cause")
		}
	}
	if d.Incidental == nil || d.Incidental.Count < 50 {
		t.Fatalf("expected a large incidental rollup for the index_health noise, got %+v", d.Incidental)
	}
}

func TestDiagnose_SortSpillSurvivesAsContributingCause(t *testing.T) {
	findings, metrics := loadDiagnosisFixture(t)
	d := Diagnose(findings, metrics)
	if d == nil {
		t.Fatal("expected a diagnosis")
	}
	var sawSpill bool
	for _, c := range d.Causes {
		if c.Category == CategorySortSpill {
			sawSpill = true
			if c.Severity != SeverityContributing {
				t.Errorf("sort spill severity = %q, want contributing", c.Severity)
			}
			if !strings.Contains(strings.ToLower(c.Fix), "work_mem") {
				t.Errorf("sort spill fix should mention work_mem, got %q", c.Fix)
			}
		}
	}
	if !sawSpill {
		t.Errorf("sort spill has a distinct fix and must survive as its own cause: %v", causeTitles(d.Causes))
	}
}

func TestDiagnose_Empty(t *testing.T) {
	if Diagnose(nil, PlanMetrics{}) != nil {
		t.Error("Diagnose(nil) should be nil")
	}
}

func TestDiagnose_SingleCleanFinding(t *testing.T) {
	d := Diagnose([]PlanFinding{{
		NodeType: "Sort", Category: CategorySortSpill,
		Message: "Sort spilled to disk (~96 MB, method external merge) — increase work_mem",
	}}, PlanMetrics{TotalCost: 1000})
	if d == nil || d.RootCause == nil {
		t.Fatal("expected a diagnosis with a root cause")
	}
	if d.RootCause.Category != CategorySortSpill {
		t.Errorf("root category = %q", d.RootCause.Category)
	}
	if len(d.Causes) != 1 {
		t.Errorf("want exactly one cause, got %d", len(d.Causes))
	}
}

func causeTitles(cs []DiagnosisCause) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.Category + ":" + c.Title
	}
	return out
}
