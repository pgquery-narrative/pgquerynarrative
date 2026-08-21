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
			preds = append(preds, notExpr(prev))
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
		Rationale:  "split OR predicates on different columns into UNION ALL branches so each side can use its own index; NOT of prior branches preserves OR multiplicity",
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
	return true
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
