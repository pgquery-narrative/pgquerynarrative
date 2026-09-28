package queryrunner

import (
	"strings"
	"testing"
)

// A plan cannot be read without the settings that produced it: a local
// random_page_cost or work_mem changes both the plan chosen and every cost
// shown, so two plans are only comparable when they were produced alike.
func TestExplainRequestsSettings(t *testing.T) {
	got := buildExplainSQL("SELECT 1", ExplainOptions{Settings: true})
	if !strings.Contains(got, "SETTINGS") {
		t.Errorf("expected SETTINGS in %q", got)
	}
	// SETTINGS is free: it must not drag ANALYZE (which executes the query) in.
	if strings.Contains(got, "ANALYZE") {
		t.Errorf("SETTINGS must not imply ANALYZE: %q", got)
	}

	withAnalyze := buildExplainSQL("SELECT 1", ExplainOptions{Analyze: true, Buffers: true, Settings: true})
	for _, want := range []string{"ANALYZE", "BUFFERS", "SETTINGS", "FORMAT JSON"} {
		if !strings.Contains(withAnalyze, want) {
			t.Errorf("expected %s in %q", want, withAnalyze)
		}
	}

	if got := buildExplainSQL("SELECT 1", ExplainOptions{}); strings.Contains(got, "SETTINGS") {
		t.Errorf("SETTINGS should be opt-in: %q", got)
	}
}

func TestParseExplainCapturesNonDefaultSettings(t *testing.T) {
	plan := []byte(`[{"Plan":{"Node Type":"Result","Total Cost":0.01},
	                  "Settings":{"random_page_cost":"1.1","work_mem":"64MB"}}]`)
	parsed, err := parseExplainJSON(plan)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(parsed.NonDefaultSettings) != 2 {
		t.Fatalf("expected 2 settings, got %v", parsed.NonDefaultSettings)
	}
	if parsed.NonDefaultSettings["random_page_cost"] != "1.1" {
		t.Errorf("got %v", parsed.NonDefaultSettings)
	}

	// An all-default server emits no Settings key at all; that must stay empty
	// rather than becoming an empty-but-present map the UI would render.
	bare, err := parseExplainJSON([]byte(`[{"Plan":{"Node Type":"Result"}}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(bare.NonDefaultSettings) != 0 {
		t.Errorf("expected no settings, got %v", bare.NonDefaultSettings)
	}
}
