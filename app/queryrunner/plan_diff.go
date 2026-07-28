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
	ExecutionTimeMs  float64
	TotalCost        float64
	RowsScanned      float64
	TempWrittenBytes float64
	RootNodeType     string
	HasSeqScan       bool
	NodeTypes        []string
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
		formatMetric("Execution time", bm.ExecutionTimeMs, am.ExecutionTimeMs, "ms", true),
		formatMetric("Total cost", bm.TotalCost, am.TotalCost, "", true),
		formatMetric("Rows scanned", bm.RowsScanned, am.RowsScanned, "", true),
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
	}
	m.RootNodeType, _ = root["Node Type"].(string)
	m.NodeTypes = collectNodeTypes(root)
	m.RowsScanned = sumPlanRows(root)
	m.TempWrittenBytes = sumTempBytes(root)
	m.HasSeqScan = containsNodeType(m.NodeTypes, "Seq Scan")
	return m
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

func sumPlanRows(node map[string]interface{}) float64 {
	rows, ok := asFloat64(node["Actual Rows"])
	if !ok {
		rows, _ = asFloat64(node["Plan Rows"])
	}
	maxRows := rows
	children, _ := node["Plans"].([]interface{})
	for _, child := range children {
		if cm, ok := child.(map[string]interface{}); ok {
			childRows := sumPlanRows(cm)
			if childRows > maxRows {
				maxRows = childRows
			}
		}
	}
	return maxRows
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

func formatValue(v float64, unit string) string {
	switch unit {
	case "ms":
		if v >= 1000 {
			return fmt.Sprintf("%.2fs", v/1000)
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
	default:
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
		return "Equal"
	}
	if before == 0 {
		return "New"
	}
	pct := ((after - before) / before) * 100
	if math.Abs(pct) < 0.5 {
		return "Equal"
	}
	improved := (lowerIsBetter && after < before) || (!lowerIsBetter && after > before)
	sign := "+"
	if pct < 0 {
		sign = "−"
	}
	label := fmt.Sprintf("%s%.1f%%", sign, math.Abs(pct))
	if improved {
		return label
	}
	return label
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
	if after.ExecutionTimeMs < before.ExecutionTimeMs && before.ExecutionTimeMs > 0 {
		improved = append(improved, "Execution time")
	}
	if before.HasSeqScan && !after.HasSeqScan {
		improved = append(improved, "Scan strategy")
	}
	if after.TotalCost < before.TotalCost && before.TotalCost > 0 {
		improved = append(improved, "Planner cost")
	}
	return improved
}
