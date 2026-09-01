package queryrunner

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Diagnosis is a verdict-first rollup of raw plan findings. A single EXPLAIN of
// a partitioned query can emit hundreds of true-but-repetitive findings (one per
// partition, one per plan node, plus schema-hygiene noise). Diagnose collapses
// those into the small set of distinct, actionable causes a human would name,
// ranked by how much of the plan cost each one explains.
type Diagnosis struct {
	// Headline is a short root-cause label, e.g. "Partition pruning defeated".
	Headline string
	// Summary is the two-to-three sentence verdict with concrete numbers.
	Summary string
	// RootCause is the single highest-leverage cause. Nil when nothing dominates.
	RootCause *DiagnosisCause
	// Causes is the ranked list of distinct causes (root first), excluding
	// incidental schema-hygiene items. Usually one to three entries.
	Causes []DiagnosisCause
	// Incidental rolls up findings that are true but unrelated to this query's
	// latency (unused/redundant indexes, small-table seq scans). Nil when none.
	Incidental *IncidentalRollup
	// RawCount is the number of findings before rollup.
	RawCount int
}

// CauseSeverity ranks a cause by its share of the query's cost.
type CauseSeverity string

const (
	// SeverityBlocker explains the bulk of the plan cost.
	SeverityBlocker CauseSeverity = "blocker"
	// SeverityContributing makes the query slower but is not the headline.
	SeverityContributing CauseSeverity = "contributing"
)

// DiagnosisCause is one distinct, deduplicated reason the query is slow.
type DiagnosisCause struct {
	Category    string
	Title       string
	Detail      string
	Fix         string
	Severity    CauseSeverity
	CostShare   float64  // 0..1 share of plan total cost attributable to this cause
	Occurrences int      // raw findings that rolled into this cause
	NodeTypes   []string // distinct plan node types involved
	Evidence    []string // up to five sample raw messages
}

// IncidentalRollup is a single collapsed line for schema-hygiene noise.
type IncidentalRollup struct {
	Count      int
	Summary    string
	Categories []string
}

// symptomCategories are downstream effects: the planner's row estimates being
// off, buffers spilling, a node being "expensive". They are re-emitted at every
// ancestor plan node, so they collapse to one story per category.
var symptomCategories = map[string]bool{
	CategoryBufferPressure:   true,
	CategoryHighCost:         true,
	CategoryCardinality:      true,
	CategorySelectivity:      true,
	CategoryParallelShortage: true,
}

// incidentalCategories are true findings that do not explain this query's
// latency: existing-index hygiene.
var incidentalCategories = map[string]bool{
	CategoryIndexHealth: true,
}

// causalRank orders causes by position in the causal chain (lower = more
// upstream). A predicate that defeats pruning causes the seq scans, which cause
// the row-estimate misses, which cause the buffer pressure and high cost.
func causalRank(category string) int {
	switch category {
	case CategoryPartitionPruning, "function_wrap":
		return 0
	case CategorySeqScan, CategoryIndexCandidate:
		return 1
	case CategorySortSpill, CategoryHashBatches, CategoryStaleStats, CategoryLoopInflation:
		return 2
	default:
		return 5
	}
}

var (
	nodePrefixRe = regexp.MustCompile(`^[A-Za-z][A-Za-z ]*? on [A-Za-z][A-Za-z ]*? `)
	digitRunRe   = regexp.MustCompile(`\d[\d.]*`)
	estCostRe    = regexp.MustCompile(`(?i)estimated cost ([\d.]+)`)
)

// smallTableSeqScan reports whether a seq-scan finding is one the planner would
// pick anyway — the finding text itself says an index will not help. These are
// noise on a partitioned scan (one per empty child partition).
func smallTableSeqScan(f PlanFinding) bool {
	if f.Category != CategorySeqScan {
		return false
	}
	m := strings.ToLower(f.Message)
	return strings.Contains(m, "table is small") ||
		strings.Contains(m, "index is unlikely to help") ||
		strings.Contains(m, "sequential scan is usually optimal") ||
		strings.Contains(m, "skip new index ddl")
}

// diagnosisGroupKey collapses findings that tell the same story. Symptom
// categories collapse to one entry each; seq scans under a partition Append are
// one story regardless of which child partition emitted them; other structural
// findings keep distinct relations apart.
func diagnosisGroupKey(f PlanFinding, partitionScan bool) string {
	if symptomCategories[f.Category] {
		return f.Category
	}
	if f.Category == CategorySeqScan && partitionScan {
		return CategorySeqScan
	}
	msg := nodePrefixRe.ReplaceAllString(f.Message, "")
	msg = digitRunRe.ReplaceAllString(FindingFingerprint(msg), "N")
	return f.Category + "\x00" + msg
}

// findingCost returns the planner cost this finding stands for: the struct field
// when set, otherwise a cost parsed out of the message text.
func findingCost(f PlanFinding) float64 {
	if f.EstimatedCost > 0 {
		return f.EstimatedCost
	}
	if m := estCostRe.FindStringSubmatch(f.Message); m != nil {
		return parseFloat(m[1])
	}
	return 0
}

type diagGroup struct {
	category    string
	firstMsg    string
	occurrences int
	maxCost     float64
	nodeTypes   map[string]bool
	samples     []string
}

// Diagnose rolls raw plan findings into ranked causes. Returns nil when there is
// nothing to diagnose.
func Diagnose(findings []PlanFinding, metrics PlanMetrics) *Diagnosis {
	if len(findings) == 0 {
		return nil
	}

	partitionScan := metrics.HasPartitionAppend && metrics.PartitionsScanned > 1

	order := make([]string, 0, 16)
	groups := make(map[string]*diagGroup)
	incOrder := make([]string, 0, 8)
	incGroups := make(map[string]*diagGroup)

	for _, f := range findings {
		keys, dst := &order, groups
		if incidentalCategories[f.Category] || smallTableSeqScan(f) {
			keys, dst = &incOrder, incGroups
		}
		key := diagnosisGroupKey(f, partitionScan)
		g := dst[key]
		if g == nil {
			g = &diagGroup{category: f.Category, firstMsg: f.Message, nodeTypes: map[string]bool{}}
			dst[key] = g
			*keys = append(*keys, key)
		}
		g.occurrences++
		if c := findingCost(f); c > g.maxCost {
			g.maxCost = c
		}
		if f.NodeType != "" {
			g.nodeTypes[f.NodeType] = true
		}
		if len(g.samples) < 3 {
			g.samples = append(g.samples, f.Message)
		}
	}

	total := metrics.TotalCost

	causes := make([]DiagnosisCause, 0, len(order))
	for _, key := range order {
		g := groups[key]
		share := 0.0
		if total > 0 {
			share = g.maxCost / total
			if share > 1 {
				share = 1
			}
		}
		if (g.category == CategoryPartitionPruning || g.category == CategorySeqScan) && partitionScan {
			share = maxFloat(share, 0.9)
		}
		causes = append(causes, DiagnosisCause{
			Category:    g.category,
			Title:       causeTitle(g.category, g.firstMsg, g.occurrences, metrics),
			Detail:      causeDetail(g.category, g.firstMsg),
			Fix:         causeFix(g.category),
			CostShare:   share,
			Occurrences: g.occurrences,
			NodeTypes:   sortedBoolKeys(g.nodeTypes),
			Evidence:    g.samples,
		})
	}

	sort.SliceStable(causes, func(i, j int) bool {
		ri, rj := causalRank(causes[i].Category), causalRank(causes[j].Category)
		if ri != rj {
			return ri < rj
		}
		return causes[i].CostShare > causes[j].CostShare
	})

	d := &Diagnosis{RawCount: len(findings), Causes: rankCauses(causes)}
	if len(d.Causes) > 0 {
		d.RootCause = &d.Causes[0]
		d.Headline, d.Summary = verdict(d.Causes[0], metrics)
	}
	d.Incidental = rollupIncidental(incOrder, incGroups)
	return d
}

// rankCauses assigns severity and drops pure downstream symptoms once an
// upstream cause dominates. Distinct-fix contributors (sort spill, stale stats)
// are always kept.
func rankCauses(sorted []DiagnosisCause) []DiagnosisCause {
	if len(sorted) == 0 {
		return nil
	}
	topUpstream := causalRank(sorted[0].Category) <= 1 &&
		(sorted[0].CostShare >= 0.4 || sorted[0].Category == CategoryPartitionPruning)

	rootIsPruning := causalRank(sorted[0].Category) == 0

	out := make([]DiagnosisCause, 0, len(sorted))
	fold := func(c DiagnosisCause) {
		if len(out) > 0 && len(out[0].Evidence) < 5 && len(c.Evidence) > 0 {
			out[0].Evidence = append(out[0].Evidence, c.Evidence[0])
		}
	}
	for i, c := range sorted {
		switch {
		case i == 0:
			if c.CostShare >= 0.4 || causalRank(c.Category) == 0 {
				c.Severity = SeverityBlocker
			} else {
				c.Severity = SeverityContributing
			}
			out = append(out, c)
		case rootIsPruning && (c.Category == CategorySeqScan || c.Category == CategoryIndexCandidate):
			// The seq scans ARE the pruning failure — the mechanism, not a
			// separate cause. Roll into the root's evidence.
			fold(c)
		case causalRank(c.Category) <= 2 && c.Fix != "" && c.CostShare >= 0.02:
			c.Severity = SeverityContributing
			out = append(out, c)
		case !topUpstream && c.CostShare >= 0.15:
			c.Severity = SeverityContributing
			out = append(out, c)
		default:
			fold(c)
		}
	}
	return out
}

func rollupIncidental(order []string, groups map[string]*diagGroup) *IncidentalRollup {
	if len(order) == 0 {
		return nil
	}
	total := 0
	cats := map[string]bool{}
	for _, k := range order {
		g := groups[k]
		total += g.occurrences
		cats[g.category] = true
	}
	distinct := len(order)
	summary := fmt.Sprintf(
		"%d schema-hygiene finding(s) across %d pattern(s) — unused or redundant indexes on the scanned tables. Not part of this query's latency; review under schema health.",
		total, distinct)
	return &IncidentalRollup{Count: total, Summary: summary, Categories: sortedBoolKeys(cats)}
}

// verdict builds the headline and 2-3 sentence summary from the root cause.
func verdict(root DiagnosisCause, m PlanMetrics) (headline, summary string) {
	switch root.Category {
	case CategoryPartitionPruning, "function_wrap":
		headline = "Partition pruning defeated"
		if m.HasPartitionAppend && m.PartitionsScanned > 1 {
			summary = fmt.Sprintf(
				"The predicate on the partition key is not sargable, so PostgreSQL cannot prune: it scans %d partitions (%s rows) for this query. Rewriting the predicate as a plain range restores pruning.",
				int(m.PartitionsScanned), humanCount(m.RowsScanned))
		} else {
			summary = "The predicate on the partition key is wrapped in a function, so PostgreSQL cannot prune partitions. Rewrite it as a plain range predicate."
		}
	case CategorySeqScan, CategoryIndexCandidate:
		headline = "Full scan on a large table"
		summary = fmt.Sprintf(
			"The query reads the whole table (%s rows) because no index covers the filter. Add a covering index or make the predicate sargable.",
			humanCount(m.RowsScanned))
	case CategorySortSpill:
		headline = "Sort spills to disk"
		summary = "The sort does not fit in work_mem and spills to an external merge on disk. Raise work_mem for this query or add an index matching the sort order."
	case CategoryStaleStats:
		headline = "Planner statistics are stale"
		summary = "Row estimates are far off because table statistics were never collected or are out of date. Run ANALYZE on the affected tables."
	default:
		headline = firstSentence(root.Title)
		summary = root.Detail
		if summary == "" {
			summary = root.Title
		}
	}
	return headline, summary
}

func causeTitle(category, msg string, occ int, m PlanMetrics) string {
	switch category {
	case CategoryPartitionPruning:
		if m.HasPartitionAppend && m.PartitionsScanned > 1 {
			return fmt.Sprintf("Partition pruning defeated — %d partitions scanned", int(m.PartitionsScanned))
		}
		return "Partition pruning defeated"
	case CategorySeqScan:
		if occ > 1 {
			return fmt.Sprintf("Sequential scan on %d sibling partitions", occ)
		}
		return "Sequential scan (no usable index)"
	case CategoryCardinality:
		return "Planner row estimates are off"
	case CategoryBufferPressure:
		return "Working set exceeds shared_buffers"
	case CategoryHighCost:
		return "One plan node dominates the cost"
	case CategorySortSpill:
		return "Sort spills to disk"
	case CategoryStaleStats:
		return "Table statistics are stale"
	case CategoryHashBatches:
		return "Hash join spills to multiple batches"
	case CategorySelectivity:
		return "Filter is not selective"
	case CategoryIndexCandidate:
		return "No index covers the filter"
	default:
		return firstSentence(msg)
	}
}

func causeDetail(category, msg string) string {
	if i := strings.Index(msg, " — "); i >= 0 {
		return strings.TrimSpace(msg[:i])
	}
	return firstSentence(msg)
}

func causeFix(category string) string {
	switch category {
	case CategoryPartitionPruning, "function_wrap":
		return "Rewrite the predicate as a sargable range so PostgreSQL can prune partitions."
	case CategorySeqScan, CategoryIndexCandidate:
		return "Add an index covering the filter columns, or make the predicate sargable."
	case CategorySortSpill:
		return "Raise work_mem for this query, or add an index matching the sort/group key."
	case CategoryStaleStats:
		return "Run ANALYZE on the affected tables."
	case CategoryHashBatches:
		return "Raise work_mem; the hash join is spilling to multiple batches."
	default:
		return ""
	}
}

// --- small helpers -------------------------------------------------------------

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

func sortedBoolKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parseFloat(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%g", &f)
	return f
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for _, sep := range []string{" — ", ". ", "; "} {
		if i := strings.Index(s, sep); i > 0 {
			return strings.TrimSpace(s[:i])
		}
	}
	return s
}

func humanCount(n float64) string {
	switch {
	case n <= 0:
		return "0"
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", n/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.0fk", n/1_000)
	default:
		return fmt.Sprintf("%.0f", n)
	}
}
