package queryrunner

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	findingPartitionRelationRe = regexp.MustCompile(`\b\w+\.\w+_\d{4}_\d{2}\b`)
	findingEstimatedCostRe     = regexp.MustCompile(`\s*\(estimated cost [\d.]+\)\s*`)
	findingPartitionSuffixRe   = regexp.MustCompile(`\w+_\d{4}_\d{2}`)
)

// FindingFingerprint normalizes a finding message for grouping repeats that differ
// only by partition child name or estimated cost.
func FindingFingerprint(msg string) string {
	return normalizeFindingMessage(msg)
}

func normalizeFindingMessage(msg string) string {
	s := findingPartitionRelationRe.ReplaceAllString(msg, "{partition}")
	s = findingEstimatedCostRe.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

// FormatCollapsedFinding summarizes n similar partition-level findings for display/PDF.
func FormatCollapsedFinding(msg string, n int) string {
	if n <= 1 {
		return msg
	}
	if !findingPartitionSuffixRe.MatchString(msg) {
		return fmt.Sprintf("%s (×%d similar)", strings.TrimSpace(msg), n)
	}
	norm := normalizeFindingMessage(msg)
	tail := norm
	if idx := strings.Index(norm, "—"); idx >= 0 {
		tail = strings.TrimSpace(norm[idx+len("—"):])
	}
	return fmt.Sprintf("×%d similar partition scans — %s", n, tail)
}

// CollapseRepeatedFindingMessages groups narrative driver strings that repeat per partition.
func CollapseRepeatedFindingMessages(messages []string) []string {
	if len(messages) == 0 {
		return nil
	}
	type group struct {
		first string
		n     int
	}
	order := make([]string, 0, len(messages))
	grouped := make(map[string]*group)
	for _, msg := range messages {
		key := FindingFingerprint(msg)
		if g, ok := grouped[key]; ok {
			g.n++
			continue
		}
		order = append(order, key)
		grouped[key] = &group{first: msg, n: 1}
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		g := grouped[key]
		if g.n > 1 {
			out = append(out, FormatCollapsedFinding(g.first, g.n))
		} else {
			out = append(out, g.first)
		}
	}
	return out
}

// FindingDisplayRank orders findings for PDF/UI (lower = more important).
func FindingDisplayRank(category, message string) int {
	rank := findingCategoryRank(category)
	if strings.Contains(message, "similar partition scans") {
		rank--
	}
	return rank
}

func findingCategoryRank(category string) int {
	switch category {
	case CategoryPartitionPruning:
		return 10
	case "function_wrap":
		return 15
	case CategorySeqScan:
		return 20
	case CategoryIndexCandidate, CategoryIndexHealth:
		return 25
	case CategoryCardinality:
		return 30
	case CategoryHighCost:
		return 35
	case CategorySelectivity:
		return 40
	case CategorySortSpill, CategoryHashBatches:
		return 45
	default:
		return 50
	}
}
