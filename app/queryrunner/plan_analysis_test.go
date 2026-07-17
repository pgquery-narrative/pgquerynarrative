package queryrunner

import (
	"os"
	"path/filepath"
	"testing"
)

func loadExplainFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "explain", name))
	if err != nil {
		t.Fatalf("failed to read fixture %s: %v", name, err)
	}
	return data
}

// TestPlanSignalFixtures runs the analysis over representative EXPLAIN plans
// and asserts each expected finding category is produced.
func TestPlanSignalFixtures(t *testing.T) {
	tests := []struct {
		fixture        string
		wantCategories []string
	}{
		{fixture: "seq_scan.json", wantCategories: []string{CategorySeqScan}},
		{fixture: "cardinality_misestimate.json", wantCategories: []string{CategoryCardinality}},
		{fixture: "sort_spill.json", wantCategories: []string{CategorySortSpill}},
		{fixture: "hash_batches.json", wantCategories: []string{CategoryHashBatches}},
		{fixture: "parallel_shortage.json", wantCategories: []string{CategoryParallelShortage}},
		{fixture: "partition_no_pruning.json", wantCategories: []string{CategoryPartitionPruning}},
		{fixture: "rows_removed.json", wantCategories: []string{CategorySelectivity}},
		{fixture: "buffer_pressure.json", wantCategories: []string{CategoryBufferPressure}},
		{fixture: "nested_loop_inflation.json", wantCategories: []string{CategoryLoopInflation}},
	}

	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			_, findings, _, err := parseExplainJSON(loadExplainFixture(t, tt.fixture))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := make(map[string]bool)
			for _, f := range findings {
				got[f.Category] = true
				if f.Category == "" {
					t.Errorf("finding without category: %+v", f)
				}
			}
			for _, want := range tt.wantCategories {
				if !got[want] {
					t.Errorf("expected category %q in findings, got %v", want, keysOf(got))
				}
			}
		})
	}
}

// TestPlanSignalEvidence ensures findings carry raw plan metrics as evidence.
func TestPlanSignalEvidence(t *testing.T) {
	_, findings, _, err := parseExplainJSON(loadExplainFixture(t, "cardinality_misestimate.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range findings {
		if f.Category == CategoryCardinality {
			if len(f.Evidence) == 0 {
				t.Fatalf("cardinality finding has no evidence: %+v", f)
			}
			return
		}
	}
	t.Fatal("no cardinality finding produced")
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// FuzzParseExplainJSON asserts the plan walker never panics on arbitrary input.
func FuzzParseExplainJSON(f *testing.F) {
	fixtures, _ := filepath.Glob(filepath.Join("testdata", "explain", "*.json"))
	for _, fixture := range fixtures {
		if data, err := os.ReadFile(fixture); err == nil {
			f.Add(data)
		}
	}
	f.Add([]byte(`[]`))
	f.Add([]byte(`[{"Plan": {"Node Type": 42, "Plans": [{"Plans": "bogus"}]}}]`))
	f.Add([]byte(`not json`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _, _, _ = parseExplainJSON(data)
	})
}
