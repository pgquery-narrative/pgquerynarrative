package web

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/reports"
)

func investigationReportFixture() *reports.Report {
	return &reports.Report{
		ID:          "abcdef12-3456-7890-abcd-ef1234567890",
		SQL:         "SELECT product_category, SUM(total_amount) FROM demo.sales WHERE DATE_TRUNC('month', date) = $1 GROUP BY 1",
		CreatedAt:   "2026-09-01T12:00:00Z",
		LlmProvider: "anthropic",
		LlmModel:    "claude-sonnet-5",
		Narrative:   &reports.NarrativeContent{Headline: "Monthly sales rollup scans every partition"},
		Metrics: &reports.MetricsData{
			Investigation: map[string]any{
				"executive_summary": "The predicate wraps the partition key in DATE_TRUNC, defeating pruning.",
				"source_query":      "SELECT product_category, SUM(total_amount) FROM demo.sales WHERE DATE_TRUNC('month', date) = $1 GROUP BY 1",
				"impact": map[string]any{
					"severity": "high",
					"summary":  "Every request reads all 36 monthly partitions.",
				},
				"postgresql_evidence": []any{
					"Seq Scan on 36 partitions",
					"Rows Removed by Filter: 1.2M",
				},
				"plan_findings": []any{
					map[string]any{"category": "partitioning", "message": "No partition pruning on demo.sales"},
				},
				"candidate_improvements": []any{
					map[string]any{
						"proposed_change":   "SELECT product_category, SUM(total_amount) FROM demo.sales WHERE date >= $1 AND date < $1 + INTERVAL '1 month' GROUP BY 1",
						"why_it_might_help": "Range predicate on the raw column lets the planner prune to one partition.",
					},
					map[string]any{
						"proposed_change": "CREATE INDEX CONCURRENTLY ON demo.sales (date, product_category)",
					},
				},
				"equivalence_validation": map[string]any{
					"status": "Equal",
					"notes":  "COUNT(*) and multiset fingerprint match on the sampled binds.",
				},
				"recommended_next_action": "Ship the range rewrite; the index is optional.",
				"risks_and_tradeoffs": []any{
					"INTERVAL arithmetic assumes a month-aligned bind.",
				},
			},
		},
	}
}

func TestBuildReportMarkdown_Investigation(t *testing.T) {
	md := buildReportMarkdown(investigationReportFixture())

	for _, want := range []string{
		"# Monthly sales rollup scans every partition",
		"## Executive summary",
		"## Impact",
		"- **Severity:** high",
		"## PostgreSQL evidence",
		"- Seq Scan on 36 partitions",
		"## Execution-plan findings",
		"**partitioning:** No partition pruning on demo.sales",
		"## Candidate improvements",
		"```sql",
		"WHERE date >= $1 AND date < $1 + INTERVAL '1 month'",
		"## Result equivalence: Equal",
		"## Recommended next action",
		"## Risks and tradeoffs",
		"## Query",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown missing %q\n---\n%s", want, md)
		}
	}
}

func TestBuildReportSQL_Investigation(t *testing.T) {
	sql := buildReportSQL(investigationReportFixture())

	for _, want := range []string{
		"-- PgQueryNarrative report abcdef12",
		"-- The predicate wraps the partition key",
		"-- BEFORE (original)",
		"DATE_TRUNC('month', date) = $1",
		"-- AFTER (candidate rewrite 1)",
		"WHERE date >= $1 AND date < $1 + INTERVAL '1 month'",
		"-- Candidate index — for review only",
		"CREATE INDEX CONCURRENTLY ON demo.sales (date, product_category)",
		"-- Result equivalence: Equal",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("sql missing %q\n---\n%s", want, sql)
		}
	}
	// The CREATE INDEX must never be emitted as a plain rewrite.
	if strings.Contains(sql, "-- AFTER (candidate rewrite 2)") {
		t.Errorf("index DDL leaked into the rewrite list:\n%s", sql)
	}
}

func TestBuildReportMarkdown_Analytics(t *testing.T) {
	report := &reports.Report{
		ID:        "11111111-2222-3333-4444-555555555555",
		SQL:       "SELECT count(*) FROM demo.sales",
		CreatedAt: "2026-09-01T12:00:00Z",
		Narrative: &reports.NarrativeContent{
			Headline:        "Sales volume is flat quarter over quarter",
			Takeaways:       []string{"Total rows unchanged", "No seasonal spike"},
			Recommendations: []string{"Revisit after the Q4 campaign"},
		},
	}

	md := buildReportMarkdown(report)
	for _, want := range []string{
		"# Sales volume is flat quarter over quarter",
		"## Key takeaways",
		"- Total rows unchanged",
		"## Recommendations",
		"## Query",
		"SELECT count(*) FROM demo.sales",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("analytics markdown missing %q\n---\n%s", want, md)
		}
	}
}
