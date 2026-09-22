package queryrunner

import (
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

const maxOrUnionLeaves = 4

// suggestOrToUnion rewrites a simple SELECT whose WHERE is (or contains a
// single) OR of predicates on different columns into UNION ALL of per-branch
// queries. Overlaps are excluded with NOT so row multiplicity matches OR.
func suggestOrToUnion(sql string, findings []PlanFinding) *RewriteCandidate {
	result, sel, ok := parseSingleSelect(sql)
	if !ok || sel.WhereClause == nil || !selectIsSimpleForUnion(sel) {
		return nil
	}
	shared, leaves := factorOr(sel.WhereClause)
	if len(leaves) < 2 || len(leaves) > maxOrUnionLeaves {
		return nil
	}
	// Fail closed: every OR leaf must be a simple col = const predicate.
	// Complex leaves (LIKE, IN-lists, nested OR, function wraps) are not rewritten.
	if !orLeavesAllSimpleEquality(leaves) {
		return nil
	}
	if orLeavesSameColumn(leaves) {
		return nil
	}
	// Multi-table FROM makes NOT/branch correlation risky; require a single base table.
	if len(sel.FromClause) != 1 {
		return nil
	}

	sortClause := sel.SortClause
	var union *pg_query.SelectStmt
	var seen []*pg_query.Node
	for i, leaf := range leaves {
		_, branchSel, ok := parseSingleSelect(sql)
		if !ok || branchSel == nil {
			return nil
		}
		preds := make([]*pg_query.Node, 0, 2+len(seen))
		preds = append(preds, shared, leaf)
		for _, prev := range seen {
			// NULL-safe: `(prev) IS NOT TRUE`, not `NOT (prev)`. A plain NOT is
			// NULL when prev is NULL, which would drop rows the original OR keeps
			// (e.g. a IS NULL, b = 2 for `a = 1 OR b = 2`).
			preds = append(preds, isNotTrueExpr(prev))
		}
		branchSel.WhereClause = combineAnd(preds)
		branchSel.SortClause = nil
		branchSel.LimitCount = nil
		branchSel.LimitOffset = nil
		if i == 0 {
			union = branchSel
		} else {
			union = &pg_query.SelectStmt{
				Op:          pg_query.SetOperation_SETOP_UNION,
				All:         true,
				Larg:        union,
				Rarg:        branchSel,
				LimitOption: pg_query.LimitOption_LIMIT_OPTION_DEFAULT,
			}
		}
		seen = append(seen, leaf)
	}
	if union == nil {
		return nil
	}
	if len(sortClause) > 0 {
		union.SortClause = sortClause
	}
	result.Stmts[0].Stmt = &pg_query.Node{Node: &pg_query.Node_SelectStmt{SelectStmt: union}}
	outSQL, err := pg_query.Deparse(result)
	if err != nil {
		return nil
	}
	outSQL = strings.TrimSpace(outSQL)
	if outSQL == "" || strings.EqualFold(outSQL, sql) {
		return nil
	}
	confidence := "medium"
	if findingsSuggestOrRewrite(findings) {
		confidence = "high"
	}
	return &RewriteCandidate{
		SQL:        outSQL,
		Rationale:  "split OR predicates on different columns into UNION ALL branches so each side can use its own index; `IS NOT TRUE` of prior branches preserves OR multiplicity even when a branch column is NULL",
		Category:   "or_to_union",
		Confidence: confidence,
	}
}

func selectIsSimpleForUnion(sel *pg_query.SelectStmt) bool {
	if sel == nil {
		return false
	}
	if len(sel.DistinctClause) > 0 || sel.IntoClause != nil {
		return false
	}
	if len(sel.GroupClause) > 0 || sel.HavingClause != nil || len(sel.WindowClause) > 0 {
		return false
	}
	if sel.LimitCount != nil || sel.LimitOffset != nil || sel.WithClause != nil {
		return false
	}
	if len(sel.LockingClause) > 0 {
		return false
	}
	if sel.Op != pg_query.SetOperation_SETOP_NONE && sel.Op != pg_query.SetOperation_SET_OPERATION_UNDEFINED {
		return false
	}
	if len(sel.FromClause) == 0 {
		return false
	}
	for _, f := range sel.FromClause {
		if f.GetRangeVar() == nil {
			return false
		}
	}
	// A single-row (or per-group) aggregate produces one row per UNION ALL
	// branch instead of one row combining all matched rows, so splitting the
	// OR into branches changes the result (e.g. SUM(x) WHERE a=1 OR b=2 becomes
	// two partial sums instead of one total). GROUP BY is already rejected
	// above; this additionally rejects an aggregate with no GROUP BY.
	if targetListHasAggregate(sel.TargetList) {
		return false
	}
	// ORDER BY must only reference columns the target list actually produces —
	// deparsing UNION ALL branches with an ORDER BY on a column absent from
	// every branch's SELECT list is not valid SQL. A wildcard target list
	// makes every column available, so it needs no check.
	if !targetListHasWildcard(sel.TargetList) && sortClauseReferencesMissingColumn(sel.SortClause, sel.TargetList) {
		return false
	}
	return true
}

// aggregateFuncNames are the built-in PostgreSQL aggregates worth guarding
// against here. The parser does not know at parse time whether a bare
// function call is an aggregate (that is resolved against the catalog), so
// this is a name allowlist rather than a structural check — AggStar alone
// only covers COUNT(*). False negatives (an aggregate not in this list) fail
// safe: equivalence verification still catches a resulting wrong rewrite
// before it ships (see the compare-plans guardrail), this list just keeps the
// rewriter from proposing the wrong shape in the common cases.
var aggregateFuncNames = map[string]bool{
	"count": true, "sum": true, "avg": true, "min": true, "max": true,
	"array_agg": true, "string_agg": true, "json_agg": true, "jsonb_agg": true,
	"bool_and": true, "bool_or": true, "every": true,
	"variance": true, "var_pop": true, "var_samp": true,
	"stddev": true, "stddev_pop": true, "stddev_samp": true,
}

func targetListHasAggregate(targets []*pg_query.Node) bool {
	for _, t := range targets {
		rt := t.GetResTarget()
		if rt == nil {
			continue
		}
		if nodeContainsAggregateCall(rt.Val) {
			return true
		}
	}
	return false
}

func nodeContainsAggregateCall(n *pg_query.Node) bool {
	if n == nil {
		return false
	}
	if fc := n.GetFuncCall(); fc != nil {
		if fc.AggStar {
			return true
		}
		for _, part := range fc.Funcname {
			if name, ok := stringNodeValue(part); ok && aggregateFuncNames[strings.ToLower(name)] {
				return true
			}
		}
		for _, arg := range fc.Args {
			if nodeContainsAggregateCall(arg) {
				return true
			}
		}
		return false
	}
	if ae := n.GetAExpr(); ae != nil {
		return nodeContainsAggregateCall(ae.Lexpr) || nodeContainsAggregateCall(ae.Rexpr)
	}
	if be := n.GetBoolExpr(); be != nil {
		for _, arg := range be.Args {
			if nodeContainsAggregateCall(arg) {
				return true
			}
		}
	}
	if ce := n.GetCoalesceExpr(); ce != nil {
		for _, arg := range ce.Args {
			if nodeContainsAggregateCall(arg) {
				return true
			}
		}
	}
	if tc := n.GetTypeCast(); tc != nil {
		return nodeContainsAggregateCall(tc.Arg)
	}
	return false
}

func targetListHasWildcard(targets []*pg_query.Node) bool {
	for _, t := range targets {
		rt := t.GetResTarget()
		if rt == nil {
			continue
		}
		if cr := rt.Val.GetColumnRef(); cr != nil {
			for _, f := range cr.Fields {
				if f.GetAStar() != nil {
					return true
				}
			}
		}
	}
	return false
}

// targetListOutputNames returns the set of column names this target list
// produces under ORDER BY resolution rules: an explicit alias, or a bare
// column reference's own (unqualified) name. A target with neither (e.g. an
// unaliased function call) contributes nothing — conservatively correct,
// since Postgres would give it an implicit name we cannot cheaply reproduce.
func targetListOutputNames(targets []*pg_query.Node) map[string]bool {
	names := make(map[string]bool, len(targets))
	for _, t := range targets {
		rt := t.GetResTarget()
		if rt == nil {
			continue
		}
		if rt.Name != "" {
			names[strings.ToLower(rt.Name)] = true
			continue
		}
		if name := columnRefName(rt.Val); name != "" {
			parts := strings.Split(name, ".")
			names[strings.ToLower(parts[len(parts)-1])] = true
		}
	}
	return names
}

func sortClauseReferencesMissingColumn(sortClause []*pg_query.Node, targets []*pg_query.Node) bool {
	if len(sortClause) == 0 {
		return false
	}
	available := targetListOutputNames(targets)
	for _, s := range sortClause {
		sb := s.GetSortBy()
		if sb == nil {
			continue
		}
		name := columnRefName(sb.Node)
		if name == "" {
			// Not a plain column reference (an ordinal position, or an
			// expression) — leave it alone rather than guess.
			continue
		}
		parts := strings.Split(name, ".")
		unqualified := strings.ToLower(parts[len(parts)-1])
		if !available[unqualified] {
			return true
		}
	}
	return false
}

func factorOr(where *pg_query.Node) (shared *pg_query.Node, leaves []*pg_query.Node) {
	if where == nil {
		return nil, nil
	}
	if isOR(where) {
		return nil, flattenOR(where)
	}
	be := where.GetBoolExpr()
	if be == nil || be.Boolop != pg_query.BoolExprType_AND_EXPR {
		return nil, nil
	}
	var orNode *pg_query.Node
	orCount := 0
	rest := make([]*pg_query.Node, 0, len(be.Args))
	for _, a := range be.Args {
		if isOR(a) {
			orCount++
			orNode = a
			continue
		}
		rest = append(rest, a)
	}
	if orCount != 1 {
		return nil, nil
	}
	return combineAnd(rest), flattenOR(orNode)
}

func isOR(n *pg_query.Node) bool {
	if n == nil {
		return false
	}
	be := n.GetBoolExpr()
	return be != nil && be.Boolop == pg_query.BoolExprType_OR_EXPR
}

func flattenOR(n *pg_query.Node) []*pg_query.Node {
	if n == nil {
		return nil
	}
	if !isOR(n) {
		return []*pg_query.Node{n}
	}
	var out []*pg_query.Node
	for _, a := range n.GetBoolExpr().Args {
		out = append(out, flattenOR(a)...)
	}
	return out
}

func orLeavesAllSimpleEquality(leaves []*pg_query.Node) bool {
	if len(leaves) == 0 {
		return false
	}
	for _, leaf := range leaves {
		if _, ok := equalityColumn(leaf); !ok {
			return false
		}
	}
	return true
}

func orLeavesSameColumn(leaves []*pg_query.Node) bool {
	var col string
	for _, leaf := range leaves {
		c, ok := equalityColumn(leaf)
		if !ok {
			return false
		}
		if col == "" {
			col = c
		}
		if c != col {
			return false
		}
	}
	return col != ""
}

func equalityColumn(n *pg_query.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	ae := n.GetAExpr()
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return "", false
	}
	if c := columnRefName(ae.Lexpr); c != "" && isConstNode(ae.Rexpr) {
		return c, true
	}
	if c := columnRefName(ae.Rexpr); c != "" && isConstNode(ae.Lexpr) {
		return c, true
	}
	return "", false
}

func findingsSuggestOrRewrite(findings []PlanFinding) bool {
	for _, f := range findings {
		if f.Category == CategorySeqScan || f.Category == CategoryIndexCandidate {
			return true
		}
	}
	return false
}
