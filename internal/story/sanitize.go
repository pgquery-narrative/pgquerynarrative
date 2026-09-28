package story

import (
	"regexp"
	"strings"
)

// RemoveFabricatedPeriodComparison strips or rewrites headline and takeaways
// when the result had no period-over-period comparison, so the LLM cannot
// invent "previous period" or "same period last year".
func RemoveFabricatedPeriodComparison(n *NarrativeContent) {
	if n == nil {
		return
	}
	n.Headline = stripPeriodComparisonPhrases(n.Headline)
	out := make([]string, 0, len(n.Takeaways))
	for _, t := range n.Takeaways {
		cleaned := stripPeriodComparisonPhrases(t)
		// Drop takeaways that became purely comparison fluff
		if isOnlyPeriodComparison(cleaned) {
			continue
		}
		if cleaned != "" {
			out = append(out, cleaned)
		}
	}
	if len(out) > 0 {
		n.Takeaways = out
	}
	// Drivers, limitations, recommendations: remove comparison-only lines
	n.Drivers = filterSlice(n.Drivers, stripPeriodComparisonPhrases, isOnlyPeriodComparison)
	n.Limitations = filterSlice(n.Limitations, stripPeriodComparisonPhrases, isOnlyPeriodComparison)
	n.Recommendations = filterSlice(n.Recommendations, stripPeriodComparisonPhrases, isOnlyPeriodComparison)
}

var periodComparisonPatterns = []*regexp.Regexp{
	// "up from the previous period's $718,534,295.76" or "up from the prior period"
	regexp.MustCompile(`(?i)\s*,?\s*up from the (previous|prior) period('s)?\s*\$?[0-9,.]*\.?`),
	regexp.MustCompile(`(?i)\s*,?\s*compared to the (previous|prior) period[^.]*\.?`),
	regexp.MustCompile(`(?i)\s*,?\s*compared to (the )?same period last year[^.]*\.?`),
	regexp.MustCompile(`(?i)\s*,?\s*from the (previous|prior) period\.?`),
	regexp.MustCompile(`(?i)\s*,?\s*vs\.?\s*the (previous|prior) period\.?`),
	regexp.MustCompile(`(?i)\s*,?\s*versus the (previous|prior) period\.?`),
	regexp.MustCompile(`(?i)\s*declined? by [0-9.,]+% (from|over) (the )?(previous|prior) period\.?`),
	regexp.MustCompile(`(?i)\s*increased? by [0-9.,]+% (from|over) (the )?(previous|prior) period\.?`),
	regexp.MustCompile(`(?i)\s*,?\s*down from the (previous|prior) period('s)?\s*\$?[0-9,.]*\.?`),
	// "There was a 0.3% decrease in total fares and an increase of 0.1% in trips" (invented comparison) - drop clause after "compared to"
	regexp.MustCompile(`(?i)\s*compared to the (previous|prior) period[^.]*\.?`),
}

func stripPeriodComparisonPhrases(s string) string {
	s = strings.TrimSpace(s)
	for _, re := range periodComparisonPatterns {
		s = re.ReplaceAllString(s, "")
	}
	s = strings.TrimSpace(s)
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	s = strings.TrimSuffix(s, ", ")
	return s
}

func isOnlyPeriodComparison(s string) bool {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return true
	}
	// Short fragment that's only about comparison
	only := []string{"compared to the previous period", "compared to the prior period",
		"same period last year", "from the previous period", "from the prior period",
		"vs the previous period", "vs the prior period", "up from the previous",
		"up from the prior", "down from the previous", "down from the prior"}
	for _, p := range only {
		if s == p || strings.HasPrefix(s, p+",") || strings.HasPrefix(s, p+".") {
			return true
		}
	}
	return false
}

func filterSlice(in []string, strip func(string) string, drop func(string) bool) []string {
	if len(in) == 0 {
		return in
	}
	out := make([]string, 0, len(in))
	for _, s := range in {
		cleaned := strip(s)
		if drop(cleaned) {
			continue
		}
		if cleaned != "" {
			out = append(out, cleaned)
		}
	}
	return out
}
