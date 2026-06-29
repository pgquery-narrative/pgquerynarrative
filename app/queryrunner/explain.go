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
	Message       string
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

	innerSQL, err := innerQuerySQL(sql)
	if err != nil {
		return nil, fmt.Errorf("query validation failed: %w", err)
	}

	explainSQL := buildExplainSQL(innerSQL, analyze)

	queryCtx, cancel := context.WithTimeout(ctx, r.queryLimit)
	defer cancel()

	start := time.Now()
	var planText string
	if err := r.pool.QueryRow(queryCtx, explainSQL).Scan(&planText); err != nil {
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
		Findings:        findings,
		ExecutionTimeMs: elapsed,
	}, nil
}

// innerQuerySQL returns the SELECT/WITH statement to explain, stripping a leading EXPLAIN wrapper if present.
func innerQuerySQL(sql string) (string, error) {
	cleaned := strings.TrimSpace(sql)
	cleaned = strings.TrimSuffix(cleaned, ";")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return "", apperrors.ErrOnlySelectAllowed
	}

	lower := strings.ToLower(cleaned)
	if !strings.HasPrefix(lower, "explain") {
		return cleaned, nil
	}

	// EXPLAIN [(options)] query — find the first top-level SELECT or WITH after the options clause.
	idx := strings.Index(lower, "select")
	withIdx := strings.Index(lower, "with")
	switch {
	case idx == -1 && withIdx == -1:
		return "", apperrors.ErrOnlySelectAllowed
	case withIdx != -1 && (idx == -1 || withIdx < idx):
		idx = withIdx
	}
	return strings.TrimSpace(cleaned[idx:]), nil
}

func buildExplainSQL(innerSQL string, analyze bool) string {
	opts := "FORMAT JSON"
	if analyze {
		opts = "ANALYZE, FORMAT JSON"
	}
	return fmt.Sprintf("EXPLAIN (%s) %s", opts, innerSQL)
}

type explainRoot []struct {
	Plan struct {
		NodeType  string                   `json:"Node Type"`
		TotalCost float64                  `json:"Total Cost"`
		Plans     []map[string]interface{} `json:"Plans"`
	} `json:"Plan"`
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
	if len(roots) == 0 {
		return 0, nil, nil, fmt.Errorf("explain json has no plan")
	}

	root := roots[0].Plan
	findings := collectPlanFindings(map[string]interface{}{
		"Node Type":  root.NodeType,
		"Total Cost": root.TotalCost,
		"Plans":      plansToIface(root.Plans),
	})

	return root.TotalCost, findings, json.RawMessage(planBytes), nil
}

func plansToIface(plans []map[string]interface{}) []interface{} {
	out := make([]interface{}, len(plans))
	for i, p := range plans {
		out[i] = p
	}
	return out
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

	if isSeqScan || isHighCost {
		msg := planFindingMessage(nodeType, schema, relation, filter, totalCost, isSeqScan)
		*findings = append(*findings, PlanFinding{
			NodeType:      nodeType,
			Schema:        schema,
			Relation:      relation,
			EstimatedCost: totalCost,
			IsSeqScan:     isSeqScan,
			Message:       msg,
		})
	}

	children, _ := node["Plans"].([]interface{})
	for _, child := range children {
		childMap, ok := child.(map[string]interface{})
		if !ok {
			continue
		}
		walkPlanNode(childMap, rootCost, false, findings)
	}
}

func planFindingMessage(nodeType, schema, relation, filter string, cost float64, isSeqScan bool) string {
	target := relation
	if schema != "" && relation != "" {
		target = schema + "." + relation
	} else if target == "" {
		target = "unknown relation"
	}

	if isSeqScan {
		msg := fmt.Sprintf("Sequential scan on %s (estimated cost %.2f)", target, cost)
		if filter != "" {
			msg += fmt.Sprintf(" — filter: %s", filter)
		}
		msg += " — consider a btree index on filtered or joined columns"
		return msg
	}

	return fmt.Sprintf("High-cost %s on %s (estimated cost %.2f, ≥50%% of plan total)", nodeType, target, cost)
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
