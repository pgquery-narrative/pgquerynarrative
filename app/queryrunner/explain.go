package queryrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
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
	// RelatedColumns lists the filter/sort/join-condition columns implicated by
	// this plan node (normalized, unqualified names), used to relate the finding
	// to existing index definitions during catalog enrichment. Empty when the
	// node carries no analyzable predicate/sort/join condition.
	RelatedColumns []string
	// IndexAdvice carries structured index-recommendation evidence for this
	// finding, populated during catalog enrichment when the relation's index
	// definitions are available. Nil until enriched.
	IndexAdvice *IndexAdvice
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
	// CategoryIndexCandidate flags a finding where no existing index covers the
	// implicated columns and a new index is a plausible remedy.
	CategoryIndexCandidate = "index_candidate"
	// CategoryIndexHealth flags a problem with an *existing* index: invalid,
	// unused, a redundant prefix of another index, or overlapping coverage.
	CategoryIndexHealth = "index_health"
)

// IndexDefinition describes one index retrieved from the catalog: its column
// composition (key and INCLUDE columns, in position order), partial-index
// predicate, uniqueness/validity, on-disk size, and live usage counters from
// pg_stat_user_indexes. It is read-only evidence — never a mutation target.
type IndexDefinition struct {
	Name           string
	Definition     string
	KeyColumns     []string
	IncludeColumns []string
	Predicate      string
	IsUnique       bool
	IsPrimary      bool
	IsValid        bool
	SizeBytes      int64
	IndexScans     int64
	TuplesRead     int64
	TuplesFetched  int64
}

// IndexAdvice carries structured, evidence-backed index-recommendation detail
// for a finding: which existing indexes were considered, what issue (if any)
// was detected, the estimated potential benefit, and the write/storage cost of
// acting on the advice.
//
// CandidateDDL is a possible DDL statement FOR EXPERT REVIEW ONLY. Nothing in
// this package — or anywhere else in this codebase — executes it. A human must
// evaluate lock contention, replication lag, and disk headroom before running
// it themselves.
type IndexAdvice struct {
	// RelatedColumns echoes the finding's implicated columns for convenience.
	RelatedColumns []string
	// RelatedIndexes are the existing indexes this advice was evaluated against.
	RelatedIndexes []IndexDefinition
	// Issues classifies the advice, e.g. "no_covering_index", "already_covered",
	// "partial_coverage", "invalid", "low_use", "duplicate_prefix", "overlapping".
	Issues []string
	// PotentialBenefit describes, in plain language, what improves if the advice is followed.
	PotentialBenefit string
	// WriteCost describes the write-amplification cost of the recommended change.
	WriteCost string
	// StorageCost describes the on-disk storage cost (added or freed) of the recommended change.
	StorageCost string
	// CandidateDDL is a draft statement for expert review only; never auto-applied.
	CandidateDDL string
}

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
	if err := r.activeValidator(ctx).Validate(sql); err != nil {
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
	pool := r.activePool(queryCtx)
	if pool == nil {
		return nil, fmt.Errorf("%w: read-only pool unavailable", apperrors.ErrQueryExecutionFailed)
	}
	var planText string
	tx, err := pool.BeginTx(queryCtx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, apperrors.ErrQueryExecutionFailed
	}
	defer func() { _ = tx.Rollback(queryCtx) }()
	if err := tx.QueryRow(queryCtx, explainSQL).Scan(&planText); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(queryCtx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("%s: explain exceeded timeout of %v", apperrors.ErrQueryTimeout, r.queryLimit)
		}
		// Do not embed driver/Postgres detail in the returned error.
		return nil, apperrors.ErrQueryExecutionFailed
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, apperrors.ErrQueryExecutionFailed
	}

	elapsed := time.Since(start).Milliseconds()
	totalCost, findings, planJSON, err := parseExplainJSON([]byte(planText))
	if err != nil {
		return nil, apperrors.ErrQueryExecutionFailed
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
				RelatedColumns: relatedColumnsForNode(node),
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
			NodeType:       nodeType,
			Schema:         schema,
			Relation:       relation,
			EstimatedCost:  totalCost,
			IsSeqScan:      isSeqScan,
			Category:       category,
			Confidence:     confidence,
			Message:        msg,
			Evidence:       seqScanEvidence(node, filter),
			RelatedColumns: relatedColumnsForNode(node),
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
		filterLower := strings.ToLower(filter)
		confidence := "medium"
		msg := fmt.Sprintf("Sequential scan on %s (estimated cost %.2f)", target, cost)
		if filter != "" {
			msg += fmt.Sprintf(" — filter: %s", filter)
		}
		switch {
		case strings.Contains(filterLower, "date_trunc"):
			confidence = "high"
			msg += " — function-wrapped partition/index key blocks pruning and index use; rewrite as a sargable range predicate"
		case filter == "":
			confidence = "low"
			msg += " — likely acceptable for small or unfiltered scans"
		default:
			if planRows, ok := asFloat64(node["Plan Rows"]); ok && planRows > 0 && planRows < 1000 {
				confidence = "low"
				msg += " — likely acceptable for small or unfiltered scans"
			} else {
				msg += " — consider a btree index on filtered or joined columns"
			}
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

var (
	// typeCastRe strips PostgreSQL "::type" casts (e.g. "'North'::text") so the
	// column regex below isn't confused by the cast suffix.
	typeCastRe = regexp.MustCompile(`::[a-zA-Z_][a-zA-Z0-9_]*(?:\([0-9,\s]+\))?`)
	// filterColumnRe captures identifiers (optionally alias-qualified) adjacent
	// to a comparison operator in a flattened filter expression (parentheses
	// already replaced with spaces): group 1 is the left operand of a symbol
	// operator, group 2 its optional right operand (present for join
	// conditions like "a.id = b.a_id", absent when the right side is a
	// literal), and group 3 the operand of a word operator (IS/LIKE/IN/...)
	// which conventionally has no meaningful right-hand column.
	filterColumnRe = regexp.MustCompile(
		`(?i)\b([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?)\s*(?:=|<>|!=|<=|>=|<|>|~~\*?|!~~\*?)\s*([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?)?` +
			`|\b([a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?)\s*(?:IS\b|LIKE\b|ILIKE\b|IN\b|BETWEEN\b)`,
	)
)

// sqlFilterStopwords excludes SQL keywords/literals that can be mistaken for
// column names by filterColumnRe (e.g. "true = false" or chained "IS NOT NULL").
var sqlFilterStopwords = map[string]bool{
	"and": true, "or": true, "not": true, "null": true, "true": true, "false": true,
	"any": true, "all": true, "some": true, "is": true, "in": true, "like": true,
	"ilike": true, "between": true, "exists": true,
}

// extractFilterColumns pulls candidate column names out of a PostgreSQL EXPLAIN
// "Filter"/"Index Cond"/"Recheck Cond"/join-condition expression, e.g.
// "(region = 'North'::text)" -> ["region"]. It is a best-effort heuristic over
// the textual plan representation (Postgres does not emit structured filter
// ASTs in EXPLAIN JSON), so it favors precision over completeness: unmatched
// or ambiguous expressions simply yield no columns rather than a bad guess.
func extractFilterColumns(filter string) []string {
	if strings.TrimSpace(filter) == "" {
		return nil
	}
	cleaned := typeCastRe.ReplaceAllString(filter, "")
	cleaned = strings.NewReplacer("(", " ", ")", " ").Replace(cleaned)

	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		name := normalizeColumnName(raw)
		if name == "" || sqlFilterStopwords[name] {
			return
		}
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, m := range filterColumnRe.FindAllStringSubmatch(cleaned, -1) {
		add(m[1])
		add(m[2])
		add(m[3])
	}
	return out
}

// extractSortColumns reads the "Sort Key" array EXPLAIN JSON emits for Sort
// and incremental-sort nodes, normalizing each entry (stripping ASC/DESC/NULLS
// FIRST/LAST and table qualifiers).
func extractSortColumns(node map[string]interface{}) []string {
	raw, _ := node["Sort Key"].([]interface{})
	var out []string
	for _, v := range raw {
		s, _ := v.(string)
		if name := normalizeColumnName(s); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// extractJoinColumns pulls column names out of a join node's condition fields
// ("Hash Cond", "Merge Cond", "Join Filter"), which use the same textual form
// as "Filter" (e.g. "(a.id = b.a_id)").
func extractJoinColumns(node map[string]interface{}) []string {
	var out []string
	for _, key := range []string{"Hash Cond", "Merge Cond", "Join Filter"} {
		if v, ok := node[key].(string); ok && v != "" {
			out = append(out, extractFilterColumns(v)...)
		}
	}
	return out
}

// relatedColumnsForNode combines filter, index/recheck condition, sort, and
// join columns for a single plan node into one de-duplicated, normalized list.
// This is the set of columns a candidate index for this node would need to cover.
func relatedColumnsForNode(node map[string]interface{}) []string {
	var out []string
	seen := map[string]bool{}
	add := func(cols []string) {
		for _, c := range cols {
			if c != "" && !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	if f, ok := node["Filter"].(string); ok {
		add(extractFilterColumns(f))
	}
	if f, ok := node["Index Cond"].(string); ok {
		add(extractFilterColumns(f))
	}
	if f, ok := node["Recheck Cond"].(string); ok {
		add(extractFilterColumns(f))
	}
	add(extractSortColumns(node))
	add(extractJoinColumns(node))
	return out
}

// normalizeColumnName canonicalizes a column reference extracted from plan
// text: trims whitespace/quotes, strips ASC/DESC/NULLS FIRST/LAST sort
// modifiers, drops a leading table/alias qualifier, and lower-cases the result
// so it can be compared against catalog column names.
func normalizeColumnName(c string) string {
	c = strings.TrimSpace(c)
	for _, suffix := range []string{" NULLS FIRST", " NULLS LAST", " nulls first", " nulls last"} {
		c = strings.TrimSuffix(c, suffix)
	}
	c = strings.TrimSpace(c)
	for _, suffix := range []string{" DESC", " ASC", " desc", " asc"} {
		c = strings.TrimSuffix(c, suffix)
	}
	c = strings.TrimSpace(c)
	c = strings.Trim(c, `"`)
	if idx := strings.LastIndex(c, "."); idx >= 0 && idx < len(c)-1 {
		c = c[idx+1:]
	}
	c = strings.Trim(c, `"`)
	c = strings.ToLower(strings.TrimSpace(c))
	if c == "" {
		return ""
	}
	for _, r := range c {
		if !(r == '_' || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return ""
		}
	}
	return c
}

// isColumnPrefix reports whether cols matches (as a set, order-insensitive —
// equality predicates can appear in any order within the same leftmost prefix)
// the leading len(cols) entries of keyColumns. Used both to check whether an
// existing index covers a finding's implicated columns, and to detect one
// index being a redundant leftmost prefix of another.
func isColumnPrefix(cols, keyColumns []string) bool {
	if len(cols) == 0 || len(cols) > len(keyColumns) {
		return false
	}
	want := make(map[string]bool, len(cols))
	for _, c := range cols {
		want[c] = true
	}
	got := make(map[string]bool, len(cols))
	for _, c := range keyColumns[:len(cols)] {
		got[c] = true
	}
	if len(want) != len(got) {
		return false
	}
	for k := range want {
		if !got[k] {
			return false
		}
	}
	return true
}

// sameColumnSet reports whether a and b contain exactly the same columns,
// regardless of order.
func sameColumnSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	set := make(map[string]bool, len(a))
	for _, c := range a {
		set[c] = true
	}
	for _, c := range b {
		if !set[c] {
			return false
		}
	}
	return true
}

// inferSingleTableRelation finds the schema/relation of the sole underlying
// table scan beneath a node that carries no "Relation Name" of its own (e.g.
// a Sort or Gather Merge directly above a scan). Returns ("", "") when the
// subtree touches zero or more than one base table, since a filter/sort
// finding can only be attributed to a table when there is exactly one.
func inferSingleTableRelation(node map[string]interface{}) (schema, relation string) {
	type relKey struct{ schema, relation string }
	seen := map[relKey]bool{}
	var order []relKey
	var walk func(n map[string]interface{})
	walk = func(n map[string]interface{}) {
		if r, _ := n["Relation Name"].(string); r != "" {
			s, _ := n["Schema"].(string)
			k := relKey{s, r}
			if !seen[k] {
				seen[k] = true
				order = append(order, k)
			}
		}
		children, _ := n["Plans"].([]interface{})
		for _, c := range children {
			if cm, ok := c.(map[string]interface{}); ok {
				walk(cm)
			}
		}
	}
	walk(node)
	if len(order) != 1 {
		return "", ""
	}
	return order[0].schema, order[0].relation
}
