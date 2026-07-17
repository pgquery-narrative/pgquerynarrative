package queryrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// PlanFinding highlights a notable node in an EXPLAIN plan.
type PlanFinding struct {
	NodeType      string
	Schema        string
	Relation      string
	EstimatedCost float64
	IsSeqScan     bool
	Category      string
	Confidence    string
	Message       string
	Evidence      []string
}

// Plan finding categories.
const (
	CategorySeqScan          = "seq_scan"
	CategoryHighCost         = "high_cost"
	CategoryCardinality      = "cardinality_misestimate"
	CategorySelectivity      = "low_selectivity"
	CategorySortSpill        = "sort_spill"
	CategoryHashBatches      = "hash_batches"
	CategoryLoopInflation    = "loop_inflation"
	CategoryParallelShortage = "parallel_shortage"
	CategoryPartitionPruning = "partition_pruning"
	CategoryBufferPressure   = "buffer_pressure"
	CategoryStaleStats       = "stale_statistics"
)

// ExplainOptions controls which EXPLAIN options the server emits.
type ExplainOptions struct {
	Analyze bool
	Buffers bool
}

// ExplainResult is the outcome of EXPLAIN (FORMAT JSON) on a read-only query.
type ExplainResult struct {
	SQL             string
	TotalCost       float64
	Plan            json.RawMessage
	Findings        []PlanFinding
	ExecutionTimeMs int64
}

// Explain runs EXPLAIN (FORMAT JSON) on a validated read-only query and analyzes the plan.
// When analyze is true, uses EXPLAIN (ANALYZE, FORMAT JSON) (executes the query; timeout-guarded).
func (r *Runner) Explain(ctx context.Context, sql string, analyze bool) (*ExplainResult, error) {
	if analyze && !r.allowExplainAnalyze {
		return nil, apperrors.ErrExplainAnalyzeDisabled
	}
	if err := r.validator.Validate(sql); err != nil {
		return nil, fmt.Errorf("query validation failed: %w", err)
	}

	innerSQL, _, err := ExtractReadOnlySQL(sql)
	if err != nil {
		return nil, fmt.Errorf("query validation failed: %w", err)
	}

	// BUFFERS requires ANALYZE; enable it whenever ANALYZE runs so plans carry I/O evidence.
	explainSQL := buildExplainSQL(innerSQL, ExplainOptions{Analyze: analyze, Buffers: analyze})

	queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()

	start := time.Now()
	pool := r.activePool()
	if pool == nil {
		return nil, fmt.Errorf("%w: read-only pool unavailable", apperrors.ErrQueryExecutionFailed)
	}
	var planText string
	if err := pool.QueryRow(queryCtx, explainSQL).Scan(&planText); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s: explain exceeded timeout of %v", apperrors.ErrQueryTimeout, r.queryLimit)
		}
		return nil, fmt.Errorf("%w: %v", apperrors.ErrQueryExecutionFailed, err)
	}

	elapsed := time.Since(start).Milliseconds()
	totalCost, findings, planJSON, err := parseExplainJSON([]byte(planText))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", apperrors.ErrQueryExecutionFailed, err)
	}

	return &ExplainResult{
		SQL:             strings.TrimSpace(innerSQL),
		TotalCost:       totalCost,
		Plan:            planJSON,
		Findings:        r.enrichExplainFindings(queryCtx, findings),
		ExecutionTimeMs: elapsed,
	}, nil
}

func buildExplainSQL(innerSQL string, opts ExplainOptions) string {
	parts := make([]string, 0, 3)
	if opts.Analyze {
		parts = append(parts, "ANALYZE")
		if opts.Buffers {
			parts = append(parts, "BUFFERS")
		}
	}
	parts = append(parts, "FORMAT JSON")
	return fmt.Sprintf("EXPLAIN (%s) %s", strings.Join(parts, ", "), innerSQL)
}

type explainRoot []struct {
	Plan map[string]interface{} `json:"Plan"`
}

// parseExplainJSON extracts total cost, raw plan JSON, and findings from PostgreSQL EXPLAIN output.
func parseExplainJSON(planBytes []byte) (float64, []PlanFinding, json.RawMessage, error) {
	planBytes = json.RawMessage(bytesTrimSpace(planBytes))
	if len(planBytes) == 0 {
		return 0, nil, nil, fmt.Errorf("empty explain output")
	}

	var roots explainRoot
	if err := json.Unmarshal(planBytes, &roots); err != nil {
		return 0, nil, nil, fmt.Errorf("invalid explain json: %w", err)
	}
	if len(roots) == 0 || roots[0].Plan == nil {
		return 0, nil, nil, fmt.Errorf("explain json has no plan")
	}

	root := roots[0].Plan
	totalCost, _ := asFloat64(root["Total Cost"])
	findings := collectPlanFindings(root)

	return totalCost, findings, json.RawMessage(planBytes), nil
}

func collectPlanFindings(node map[string]interface{}) []PlanFinding {
	var findings []PlanFinding
	rootCost, _ := asFloat64(node["Total Cost"])
	walkPlanNode(node, rootCost, true, &findings)
	return findings
}

func walkPlanNode(node map[string]interface{}, rootCost float64, isRoot bool, findings *[]PlanFinding) {
	nodeType, _ := node["Node Type"].(string)
	totalCost, _ := asFloat64(node["Total Cost"])
	schema, _ := node["Schema"].(string)
	relation, _ := node["Relation Name"].(string)
	filter, _ := node["Filter"].(string)

	isSeqScan := nodeType == "Seq Scan"
	isHighCost := !isRoot && rootCost > 0 && totalCost >= rootCost*0.5

	actualRows, hasActual := asFloat64(node["Actual Rows"])
	planRows, hasPlan := asFloat64(node["Plan Rows"])
	if hasActual && hasPlan && planRows > 0 {
		ratio := actualRows / planRows
		if ratio >= 10 || ratio <= 0.1 {
			msg := fmt.Sprintf("Cardinality misestimate on %s: planned ~%.0f rows, actual ~%.0f rows (ratio %.1fx)",
				relationOrNode(nodeType, schema, relation), planRows, actualRows, ratio)
			confidence := "medium"
			if ratio >= 100 || ratio <= 0.01 {
				confidence = "high"
			}
			*findings = append(*findings, PlanFinding{
				NodeType:      nodeType,
				Schema:        schema,
				Relation:      relation,
				EstimatedCost: totalCost,
				Category:      CategoryCardinality,
				Confidence:    confidence,
				Message:       msg + " — consider ANALYZE or reviewing predicate selectivity",
				Evidence: []string{
					fmt.Sprintf("Plan Rows=%.0f", planRows),
					fmt.Sprintf("Actual Rows=%.0f", actualRows),
					fmt.Sprintf("ratio=%.1fx", ratio),
				},
			})
		}
	}

	if isSeqScan || isHighCost {
		msg, confidence := planFindingMessage(nodeType, schema, relation, filter, totalCost, isSeqScan, node)
		category := CategoryHighCost
		if isSeqScan {
			category = CategorySeqScan
		}
		*findings = append(*findings, PlanFinding{
			NodeType:      nodeType,
			Schema:        schema,
			Relation:      relation,
			EstimatedCost: totalCost,
			IsSeqScan:     isSeqScan,
			Category:      category,
			Confidence:    confidence,
			Message:       msg,
			Evidence:      seqScanEvidence(node, filter),
		})
	}

	*findings = append(*findings, detectPlanSignals(node, nodeType, schema, relation, totalCost)...)

	children, _ := node["Plans"].([]interface{})
	for _, child := range children {
		childMap, ok := child.(map[string]interface{})
		if !ok {
			continue
		}
		walkPlanNode(childMap, rootCost, false, findings)
	}
}

func planFindingMessage(nodeType, schema, relation, filter string, cost float64, isSeqScan bool, node map[string]interface{}) (string, string) {
	target := relation
	if schema != "" && relation != "" {
		target = schema + "." + relation
	} else if target == "" {
		target = "unknown relation"
	}

	if isSeqScan {
		confidence := "medium"
		if planRows, ok := asFloat64(node["Plan Rows"]); ok && planRows > 0 && planRows < 1000 {
			confidence = "low"
		}
		if filter == "" {
			confidence = "low"
		}
		msg := fmt.Sprintf("Sequential scan on %s (estimated cost %.2f)", target, cost)
		if filter != "" {
			msg += fmt.Sprintf(" — filter: %s", filter)
		}
		if confidence == "medium" {
			msg += " — consider a btree index on filtered or joined columns"
		} else {
			msg += " — likely acceptable for small or unfiltered scans"
		}
		return msg, confidence
	}

	return fmt.Sprintf("High-cost %s on %s (estimated cost %.2f, ≥50%% of plan total)", nodeType, target, cost), "high"
}

func relationOrNode(nodeType, schema, relation string) string {
	if relation != "" {
		if schema != "" {
			return schema + "." + relation
		}
		return relation
	}
	return nodeType
}

func asFloat64(v interface{}) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}
