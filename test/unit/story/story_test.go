package story_test

import (
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/internal/story"
)

func TestRemoveFabricatedPeriodComparison_Headline(t *testing.T) {
	n := &story.NarrativeContent{
		Headline:  "Total fares declined by 0.3% from previous period",
		Takeaways: []string{"Some insight."},
	}
	story.RemoveFabricatedPeriodComparison(n)
	if strings.Contains(strings.ToLower(n.Headline), "previous period") {
		t.Errorf("headline should not contain 'previous period' after sanitize, got %q", n.Headline)
	}
	if n.Headline == "" {
		t.Error("headline should not be empty")
	}
}

func TestRemoveFabricatedPeriodComparison_Takeaways(t *testing.T) {
	n := &story.NarrativeContent{
		Headline: "Sales update",
		Takeaways: []string{
			"Revenue was $100, up from the previous period's $90.",
			"Trips increased, compared to the prior period.",
			"Valid takeaway with no comparison.",
		},
	}
	story.RemoveFabricatedPeriodComparison(n)
	for i, takeaway := range n.Takeaways {
		lower := strings.ToLower(takeaway)
		if strings.Contains(lower, "previous period") || strings.Contains(lower, "prior period") {
			t.Errorf("takeaway %d should not contain period comparison, got %q", i, takeaway)
		}
	}
	if len(n.Takeaways) != 3 {
		t.Errorf("expected 3 takeaways (comparison phrases stripped), got %d: %v", len(n.Takeaways), n.Takeaways)
	}
	if !strings.Contains(n.Takeaways[0], "Revenue was $100") {
		t.Errorf("first takeaway should start with 'Revenue was $100', got %q", n.Takeaways[0])
	}
	if !strings.Contains(n.Takeaways[2], "Valid takeaway") {
		t.Errorf("third takeaway should be unchanged, got %q", n.Takeaways[2])
	}
}

func TestRemoveFabricatedPeriodComparison_DropsComparisonOnlyTakeaways(t *testing.T) {
	n := &story.NarrativeContent{
		Headline: "Report",
		Takeaways: []string{
			"Compared to the previous period",
			"Up from the prior period.",
			"Real insight with numbers.",
		},
	}
	story.RemoveFabricatedPeriodComparison(n)
	if len(n.Takeaways) != 1 {
		t.Errorf("expected 1 takeaway, got %d: %v", len(n.Takeaways), n.Takeaways)
	}
	if n.Takeaways[0] != "Real insight with numbers." {
		t.Errorf("expected 'Real insight with numbers.', got %q", n.Takeaways[0])
	}
}

func TestRemoveFabricatedPeriodComparison_SamePeriodLastYear(t *testing.T) {
	n := &story.NarrativeContent{
		Headline:  "Growth",
		Takeaways: []string{"Trips increased 15% compared to the same period last year."},
	}
	story.RemoveFabricatedPeriodComparison(n)
	for _, takeaway := range n.Takeaways {
		if strings.Contains(strings.ToLower(takeaway), "same period last year") {
			t.Errorf("takeaway should not contain 'same period last year', got %q", takeaway)
		}
	}
}

func TestRemoveFabricatedPeriodComparison_NilSafe(t *testing.T) {
	story.RemoveFabricatedPeriodComparison(nil)
	n := &story.NarrativeContent{}
	story.RemoveFabricatedPeriodComparison(n)
}

func TestRemoveFabricatedPeriodComparison_LeavesValidContent(t *testing.T) {
	n := &story.NarrativeContent{
		Headline: "Revenue reached $1.2 million",
		Takeaways: []string{
			"Total sales were 100 units.",
			"Average price was $12.",
		},
	}
	story.RemoveFabricatedPeriodComparison(n)
	if n.Headline != "Revenue reached $1.2 million" {
		t.Errorf("headline should be unchanged, got %q", n.Headline)
	}
	if len(n.Takeaways) != 2 {
		t.Errorf("expected 2 takeaways unchanged, got %d", len(n.Takeaways))
	}
}
