package queryrunner

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// PlanMetrics summarizes measurable plan evidence.
type PlanMetrics struct {
	ExecutionTimeMs float64
	TotalCost       float64
	// RowsScanned is the total tuple work of the base-relation scan nodes:
	// sum of (Actual Rows * Actual Loops) — or Plan Rows when no ANALYZE data.
	RowsScanned float64
	// MaxNodeRows is the largest row count at any single plan node.
	MaxNodeRows        float64
	TempWrittenBytes   float64
	PartitionsScanned  float64
	PartitionsRemoved  float64
	HasPartitionAppend bool
	RootNodeType       string
	HasSeqScan         bool
	NodeTypes          []string
	HasActualTiming    bool
}

// ComparisonMetric is one row in a before/after comparison table.
type ComparisonMetric struct {
	Evidence string
	Before   string
	After    string
	Change   string
}

// PlanDiff summarizes structural plan changes.
type PlanDiff struct {
	Removed  []string
	Added    []string
	Improved []string
}

// PlanComparison is the full comparison between two plans.
type PlanComparison struct {
	BeforeMetrics PlanMetrics
	AfterMetrics  PlanMetrics
	Metrics       []ComparisonMetric
	Diff          PlanDiff
}

// MetricsFromPlan extracts measurable evidence from an EXPLAIN (FORMAT JSON) blob.
// Used by candidate ranking without building a full before/after comparison table.
func MetricsFromPlan(plan json.RawMessage) (PlanMetrics, error) {
	root, err := extractPlanRoot(plan)
	if err != nil {
		return PlanMetrics{}, err
	}
	return collectPlanMetrics(root), nil
}

// ComparePlans compares two EXPLAIN JSON plan outputs.
func ComparePlans(beforePlan, afterPlan json.RawMessage) (*PlanComparison, error) {
	beforeRoot, err := extractPlanRoot(beforePlan)
	if err != nil {
		return nil, fmt.Errorf("before plan: %w", err)
	}
	afterRoot, err := extractPlanRoot(afterPlan)
	if err != nil {
		return nil, fmt.Errorf("after plan: %w", err)
	}
	bm := collectPlanMetrics(beforeRoot)
	am := collectPlanMetrics(afterRoot)

	metrics := []ComparisonMetric{
		formatTimingMetric(bm, am),
		formatMetric("Total cost", bm.TotalCost, am.TotalCost, "cost", true),
		formatMetric("Rows scanned", bm.RowsScanned, am.RowsScanned, "rows", true),
		formatMetric("Max node rows", bm.MaxNodeRows, am.MaxNodeRows, "rows", true),
		formatPartitionsMetric(bm, am),
		formatMetric("Temp written", bm.TempWrittenBytes, am.TempWrittenBytes, "bytes", true),
		{
			Evidence: "Plan type",
			Before:   planTypeLabel(bm),
			After:    planTypeLabel(am),
			Change:   planTypeChange(bm, am),
		},
	}

	diff := diffPlanNodes(bm.NodeTypes, am.NodeTypes)
	diff.Improved = detectImprovements(bm, am)

	return &PlanComparison{
		BeforeMetrics: bm,
		AfterMetrics:  am,
		Metrics:       metrics,
		Diff:          diff,
	}, nil
}

func extractPlanRoot(planBytes json.RawMessage) (map[string]interface{}, error) {
	planBytes = json.RawMessage(bytesTrimSpace(planBytes))
	var roots explainRoot
	if err := json.Unmarshal(planBytes, &roots); err != nil {
		return nil, err
	}
	if len(roots) == 0 || roots[0].Plan == nil {
		return nil, fmt.Errorf("no plan root")
	}
	return roots[0].Plan, nil
}

func collectPlanMetrics(root map[string]interface{}) PlanMetrics {
	m := PlanMetrics{}
	m.TotalCost, _ = asFloat64(root["Total Cost"])
	if exec, ok := asFloat64(root["Actual Total Time"]); ok {
		m.ExecutionTimeMs = exec
		m.HasActualTiming = true
	}
	m.RootNodeType, _ = root["Node Type"].(string)
	m.NodeTypes = collectNodeTypes(root)
	m.RowsScanned = scanRowsProcessed(root)
	m.MaxNodeRows = maxNodeRows(root)
	m.TempWrittenBytes = sumTempBytes(root)
	m.HasSeqScan = containsNodeType(m.NodeTypes, "Seq Scan")
	m.PartitionsScanned, m.PartitionsRemoved, m.HasPartitionAppend = collectPartitionStats(root)
	return m
}

// collectPartitionStats walks Append/Merge Append nodes and returns scanned child
// subplans plus Subplans Removed (partition pruning). Values come from the
// largest Append-like node in the tree (typical parent partition Append).
func collectPartitionStats(root map[string]interface{}) (scanned, removed float64, ok bool) {
	var walk func(map[string]interface{})
	walk = func(n map[string]interface{}) {
		nodeType, _ := n["Node Type"].(string)
		if nodeType == "Append" || nodeType == "Merge Append" {
			children, _ := n["Plans"].([]interface{})
			childCount := float64(len(children))
			rem, _ := asFloat64(n["Subplans Removed"])
			if !ok || childCount+rem > scanned+removed {
				scanned, removed, ok = childCount, rem, true
			}
		}
		children, _ := n["Plans"].([]interface{})
		for _, child := range children {
			if cm, okChild := child.(map[string]interface{}); okChild {
				walk(cm)
			}
		}
	}
	walk(root)
	return scanned, removed, ok
}

func collectNodeTypes(node map[string]interface{}) []string {
	var types []string
	var walk func(map[string]interface{})
	walk = func(n map[string]interface{}) {
		if t, ok := n["Node Type"].(string); ok && t != "" {
			types = append(types, formatNodeLabel(n))
		}
		children, _ := n["Plans"].([]interface{})
		for _, child := range children {
			if cm, ok := child.(map[string]interface{}); ok {
				walk(cm)
			}
		}
	}
	walk(node)
	return types
}

func formatNodeLabel(node map[string]interface{}) string {
	nodeType, _ := node["Node Type"].(string)
	schema, _ := node["Schema"].(string)
	relation, _ := node["Relation Name"].(string)
	if relation != "" {
		if schema != "" {
			return fmt.Sprintf("%s: %s.%s", nodeType, schema, relation)
		}
		return fmt.Sprintf("%s: %s", nodeType, relation)
	}
	return nodeType
}

// maxNodeRows returns the largest per-node row count anywhere in the tree
// (Actual Rows when present, else Plan Rows).
func maxNodeRows(node map[string]interface{}) float64 {
	rows, ok := asFloat64(node["Actual Rows"])
	if !ok {
		rows, _ = asFloat64(node["Plan Rows"])
	}
	maxRows := rows
	children, _ := node["Plans"].([]interface{})
	for _, child := range children {
		if cm, ok := child.(map[string]interface{}); ok {
			if childRows := maxNodeRows(cm); childRows > maxRows {
				maxRows = childRows
			}
		}
	}
	return maxRows
}

// baseScanNodeTypes are the node types that read tuples from a relation. Bitmap
// Index Scan is excluded (it feeds a Bitmap Heap Scan — counting both double
// counts), as are Subquery/CTE/WorkTable/Function/Values scans (not table reads).
var baseScanNodeTypes = map[string]struct{}{
	"Seq Scan":         {},
	"Index Scan":       {},
	"Index Only Scan":  {},
	"Bitmap Heap Scan": {},
	"Tid Scan":         {},
	"Tid Range Scan":   {},
	"Sample Scan":      {},
	"Foreign Scan":     {},
}

// scanRowsProcessed sums the real tuple work of the base-relation scan nodes:
// Actual Rows * Actual Loops (Postgres reports Actual Rows per loop), falling
// back to Plan Rows when there is no ANALYZE data.
func scanRowsProcessed(node map[string]interface{}) float64 {
	var total float64
	var walk func(map[string]interface{})
	walk = func(n map[string]interface{}) {
		nodeType, _ := n["Node Type"].(string)
		if _, isScan := baseScanNodeTypes[nodeType]; isScan {
			if actual, ok := asFloat64(n["Actual Rows"]); ok {
				loops, hasLoops := asFloat64(n["Actual Loops"])
				if !hasLoops || loops < 1 {
					loops = 1
				}
				total += actual * loops
			} else if plan, ok := asFloat64(n["Plan Rows"]); ok {
				total += plan
			}
		}
		children, _ := n["Plans"].([]interface{})
		for _, child := range children {
			if cm, ok := child.(map[string]interface{}); ok {
				walk(cm)
			}
		}
	}
	walk(node)
	return total
}

func sumTempBytes(node map[string]interface{}) float64 {
	var total float64
	if v, ok := asFloat64(node["Temp Written Blocks"]); ok {
		total += v * 8192 // PostgreSQL block size
	}
	children, _ := node["Plans"].([]interface{})
	for _, child := range children {
		if cm, ok := child.(map[string]interface{}); ok {
			total += sumTempBytes(cm)
		}
	}
	return total
}

func containsNodeType(types []string, target string) bool {
	for _, t := range types {
		if strings.HasPrefix(t, target) {
			return true
		}
	}
	return false
}

func formatMetric(name string, before, after float64, unit string, lowerIsBetter bool) ComparisonMetric {
	bStr := formatValue(before, unit)
	aStr := formatValue(after, unit)
	change := formatChange(before, after, lowerIsBetter)
	return ComparisonMetric{Evidence: name, Before: bStr, After: aStr, Change: change}
}

func formatTimingMetric(before, after PlanMetrics) ComparisonMetric {
	if !before.HasActualTiming && !after.HasActualTiming {
		return ComparisonMetric{
			Evidence: "Execution time",
			Before:   "n/a",
			After:    "n/a",
			Change:   "estimate-only",
		}
	}
	return formatMetric("Execution time", before.ExecutionTimeMs, after.ExecutionTimeMs, "ms", true)
}

func formatPartitionsMetric(before, after PlanMetrics) ComparisonMetric {
	bLabel := formatPartitionCount(before)
	aLabel := formatPartitionCount(after)
	// When the after plan fully prunes to a single-partition scan, PostgreSQL may
	// omit the Append node — treat that as 1 partition scanned.
	bCount, bOK := partitionCountForDisplay(before)
	aCount, aOK := partitionCountForDisplay(after)
	if !bOK && !aOK {
		return ComparisonMetric{
			Evidence: "Partitions scanned",
			Before:   "n/a",
			After:    "n/a",
			Change:   "n/a",
		}
	}
	if bOK {
		bLabel = formatValue(bCount, "rows")
		if before.HasPartitionAppend && before.PartitionsRemoved > 0 {
			bLabel = fmt.Sprintf("%s (%s pruned)", bLabel, formatValue(before.PartitionsRemoved, "rows"))
		}
	}
	if aOK {
		aLabel = formatValue(aCount, "rows")
		if after.HasPartitionAppend && after.PartitionsRemoved > 0 {
			aLabel = fmt.Sprintf("%s (%s pruned)", aLabel, formatValue(after.PartitionsRemoved, "rows"))
		}
	}
	change := "Same"
	if bOK && aOK && bCount != aCount {
		change = fmt.Sprintf("%s → %s", formatValue(bCount, "rows"), formatValue(aCount, "rows"))
	}
	return ComparisonMetric{Evidence: "Partitions scanned", Before: bLabel, After: aLabel, Change: change}
}

func partitionCountForDisplay(m PlanMetrics) (float64, bool) {
	if m.HasPartitionAppend {
		return m.PartitionsScanned, true
	}
	// No Append: if the plan only touches one base relation partition-style name,
	// still report 1 so before/after comparisons stay meaningful.
	if m.HasSeqScan || containsNodeType(m.NodeTypes, "Index Scan") || containsNodeType(m.NodeTypes, "Index Only Scan") || containsNodeType(m.NodeTypes, "Bitmap") {
		return 1, true
	}
	return 0, false
}

func formatPartitionCount(m PlanMetrics) string {
	if !m.HasPartitionAppend {
		return "n/a"
	}
	label := formatValue(m.PartitionsScanned, "rows")
	if m.PartitionsRemoved > 0 {
		return fmt.Sprintf("%s (%s pruned)", label, formatValue(m.PartitionsRemoved, "rows"))
	}
	return label
}

func formatValue(v float64, unit string) string {
	switch unit {
	case "ms":
		if v >= 1000 {
			return fmt.Sprintf("%.2fs", v/1000)
		}
		if v > 0 && v < 1 {
			return fmt.Sprintf("%.2fms", v)
		}
		return fmt.Sprintf("%.0fms", v)
	case "bytes":
		if v >= 1<<30 {
			return fmt.Sprintf("%.1f GB", v/(1<<30))
		}
		if v >= 1<<20 {
			return fmt.Sprintf("%.0f MB", v/(1<<20))
		}
		if v > 0 {
			return fmt.Sprintf("%.0f KB", v/1024)
		}
		return "0"
	case "cost":
		if v <= 0 {
			return "0.00"
		}
		if v < 10 {
			return fmt.Sprintf("%.2f", v)
		}
		if v >= 1_000_000 {
			return fmt.Sprintf("%.2fM", v/1_000_000)
		}
		if v >= 1000 {
			return fmt.Sprintf("%.1fk", v/1000)
		}
		return fmt.Sprintf("%.1f", v)
	default: // rows / generic counts
		if v >= 1_000_000 {
			return fmt.Sprintf("%.1fM", v/1_000_000)
		}
		if v >= 1000 {
			return fmt.Sprintf("%.1fk", v/1000)
		}
		return fmt.Sprintf("%.0f", v)
	}
}

func formatChange(before, after float64, lowerIsBetter bool) string {
	if before == 0 && after == 0 {
		return "equal"
	}
	if before == 0 {
		return "New"
	}
	// Avoid claiming −100% when the after value rounded to zero or is missing.
	if after <= 0 {
		if lowerIsBetter {
			return "≈ eliminated"
		}
		return "New"
	}
	ratio := before / after
	pct := ((after - before) / before) * 100
	if math.Abs(pct) < 0.5 {
		return "equal"
	}
	// Prefer fold-change for large improvements (credible vs −100.0%).
	if lowerIsBetter && ratio >= 10 {
		if ratio >= 100 {
			return fmt.Sprintf("−%.0f×", ratio)
		}
		return fmt.Sprintf("−%.1f×", ratio)
	}
	if !lowerIsBetter && after/before >= 10 {
		mult := after / before
		if mult >= 100 {
			return fmt.Sprintf("+%.0f×", mult)
		}
		return fmt.Sprintf("+%.1f×", mult)
	}
	sign := "+"
	if pct < 0 {
		sign = "−"
	}
	absPct := math.Abs(pct)
	if absPct > 99.9 {
		absPct = 99.9
	}
	return fmt.Sprintf("%s%.1f%%", sign, absPct)
}

func planTypeLabel(m PlanMetrics) string {
	if m.HasSeqScan {
		return "Seq scan"
	}
	if containsNodeType(m.NodeTypes, "Index Scan") || containsNodeType(m.NodeTypes, "Index Only Scan") {
		return "Index scan"
	}
	if containsNodeType(m.NodeTypes, "Bitmap") {
		return "Bitmap scan"
	}
	return m.RootNodeType
}

func planTypeChange(before, after PlanMetrics) string {
	b := planTypeLabel(before)
	a := planTypeLabel(after)
	if b == a {
		return "Same"
	}
	return "Changed"
}

func diffPlanNodes(before, after []string) PlanDiff {
	bSet := make(map[string]struct{}, len(before))
	aSet := make(map[string]struct{}, len(after))
	for _, n := range before {
		bSet[n] = struct{}{}
	}
	for _, n := range after {
		aSet[n] = struct{}{}
	}
	var removed, added []string
	for n := range bSet {
		if _, ok := aSet[n]; !ok {
			removed = append(removed, n)
		}
	}
	for n := range aSet {
		if _, ok := bSet[n]; !ok {
			added = append(added, n)
		}
	}
	sort.Strings(removed)
	sort.Strings(added)
	return PlanDiff{Removed: removed, Added: added}
}

func detectImprovements(before, after PlanMetrics) []string {
	var improved []string
	if after.TempWrittenBytes < before.TempWrittenBytes && before.TempWrittenBytes > 0 {
		improved = append(improved, "Temporary disk usage")
	}
	if after.RowsScanned < before.RowsScanned && before.RowsScanned > 0 {
		improved = append(improved, "Rows scanned")
	}
	if after.HasActualTiming && before.HasActualTiming && after.ExecutionTimeMs < before.ExecutionTimeMs && before.ExecutionTimeMs > 0 {
		improved = append(improved, "Execution time")
	}
	if before.HasSeqScan && !after.HasSeqScan {
		improved = append(improved, "Scan strategy")
	}
	if after.TotalCost < before.TotalCost && before.TotalCost > 0 {
		improved = append(improved, "Planner cost")
	}
	if before.HasPartitionAppend {
		afterCount := after.PartitionsScanned
		if !after.HasPartitionAppend {
			afterCount = 1
		}
		if afterCount < before.PartitionsScanned {
			improved = append(improved, "Partition pruning")
		}
	}
	return improved
}
