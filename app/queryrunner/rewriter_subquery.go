package queryrunner

import (
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// suggestInToExists rewrites col IN (SELECT ...) to EXISTS and col NOT IN
// (SELECT ...) to a NULL-safe NOT EXISTS form. Only simple single-table
// outer queries with a single-column subquery are rewritten.
func suggestInToExists(sql string, findings []PlanFinding) *RewriteCandidate {
	result, sel, ok := parseSingleSelect(sql)
	if !ok || sel.WhereClause == nil {
		return nil
	}
	// Fail closed on shapes where EXISTS correlation can change semantics.
	if !outerSelectSafeForInExists(sel) {
		return nil
	}
	outerAlias, ok := singleFromAlias(sel)
	if !ok {
		return nil
	}
	var kinds []string
	newWhere, n := rewriteInToExistsInExpr(sel.WhereClause, outerAlias, false, &kinds)
	if n == 0 || newWhere == nil {
		return nil
	}
	sel.WhereClause = newWhere
	outSQL, err := pg_query.Deparse(result)
	if err != nil {
		return nil
	}
	outSQL = strings.TrimSpace(outSQL)
	if outSQL == "" || strings.EqualFold(outSQL, sql) {
		return nil
	}
	confidence := "medium"
	if findingsSuggestInRewrite(findings) {
		confidence = "high"
	}
	category := "in_to_exists"
	rationale := "rewrite IN (SELECT ...) to EXISTS so the planner can use a correlated semi-join / index lookup"
	if hasKind(kinds, "not_in") {
		category = "not_in_to_exists"
		rationale = "rewrite NOT IN (SELECT ...) to NULL-safe NOT EXISTS (rejects NULL subquery rows the same way NOT IN does)"
		if hasKind(kinds, "in") {
			category = "in_to_exists"
			rationale = "rewrite IN/NOT IN (SELECT ...) to EXISTS / NULL-safe NOT EXISTS"
		}
	}
	return &RewriteCandidate{
		SQL:        outSQL,
		Rationale:  rationale,
		Category:   category,
		Confidence: confidence,
	}
}

// rewriteInToExistsInExpr rewrites the IN / NOT IN sub-links a WHERE clause reaches through AND and OR.
// IN and NOT IN can be NULL; EXISTS and NOT EXISTS cannot. WHERE treats NULL and FALSE alike, so that
// difference is invisible at the top of a WHERE, through AND and through OR, but not under an enclosing
// NOT, which turns NULL into NULL and FALSE into TRUE. Nothing under another NOT is rewritten.
func rewriteInToExistsInExpr(node *pg_query.Node, outerAlias string, negated bool, kinds *[]string) (*pg_query.Node, int) {
	if node == nil || negated {
		return node, 0
	}
	if sl := node.GetSubLink(); sl != nil {
		if replacement, ok := tryRewriteInSubLink(sl, outerAlias, false); ok {
			*kinds = append(*kinds, "in")
			return replacement, 1
		}
		return node, 0
	}
	if be := node.GetBoolExpr(); be != nil {
		if be.Boolop == pg_query.BoolExprType_NOT_EXPR && len(be.Args) == 1 {
			if sl := be.Args[0].GetSubLink(); sl != nil {
				if replacement, ok := tryRewriteInSubLink(sl, outerAlias, true); ok {
					*kinds = append(*kinds, "not_in")
					return replacement, 1
				}
			}
		}
		if be.Boolop == pg_query.BoolExprType_NOT_EXPR {
			return node, 0
		}
		total := 0
		args := make([]*pg_query.Node, len(be.Args))
		changed := false
		for i, arg := range be.Args {
			rewritten, n := rewriteInToExistsInExpr(arg, outerAlias, false, kinds)
			total += n
			if n > 0 && rewritten != nil {
				args[i] = rewritten
				changed = true
			} else {
				args[i] = arg
			}
		}
		if !changed {
			return node, 0
		}
		newBe := &pg_query.BoolExpr{Boolop: be.Boolop, Args: args, Location: be.Location}
		return &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: newBe}}, total
	}
	return node, 0
}

func tryRewriteInSubLink(sl *pg_query.SubLink, outerAlias string, notIn bool) (*pg_query.Node, bool) {
	if sl == nil || sl.SubLinkType != pg_query.SubLinkType_ANY_SUBLINK {
		return nil, false
	}
	// `x IN (...)` has no operator name; `x = ANY (...)` names "=". `x > ANY (...)` and the rest are not
	// equality and must not become one.
	if len(sl.OperName) > 0 && !(len(sl.OperName) == 1 && sl.OperName[0].GetString_().GetSval() == "=") {
		return nil, false
	}
	if sl.Testexpr == nil || sl.Testexpr.GetColumnRef() == nil {
		return nil, false
	}
	subSel := sl.Subselect.GetSelectStmt()
	if subSel == nil || !subqueryEligibleForExists(subSel) {
		return nil, false
	}
	innerAlias, ok := singleFromAlias(subSel)
	if !ok || strings.EqualFold(innerAlias, outerAlias) {
		// The same name in both scopes (a table compared with itself) would make `inner.col = outer.col`
		// compare the inner table with itself.
		return nil, false
	}
	target := resTargetVal(subSel.TargetList[0])
	if target == nil || target.GetAStar() != nil {
		return nil, false
	}
	// subqueryEligibleForExists already required a bare-column target, so
	// unwrapColumnRefNode(target) is never nil here; qualifyColumnRef can still
	// fail to produce a ColumnRef, in which case the original target node is
	// used unqualified.
	innerCol := qualifyColumnRef(unwrapColumnRefNode(target), innerAlias)
	if innerCol == nil || innerCol.GetColumnRef() == nil {
		innerCol = target
	}
	outerCol := qualifyColumnRef(sl.Testexpr, outerAlias)
	eq := cmpExpr("=", innerCol, outerCol)

	existsSel := cloneSelectStmt(subSel)
	if existsSel == nil {
		return nil, false
	}
	existsSel.TargetList = []*pg_query.Node{
		{Node: &pg_query.Node_ResTarget{ResTarget: &pg_query.ResTarget{Val: aConstInt(1)}}},
	}
	if notIn {
		// x NOT IN (subquery) is TRUE for an empty subquery whatever x is, and never TRUE when x or any
		// subquery value is NULL. "A row equal to x, a NULL row, or x itself NULL" as the thing to be
		// absent gives exactly that; `x IS NOT NULL AND ...` would drop a NULL x from an empty subquery.
		existsSel.WhereClause = combineAnd([]*pg_query.Node{
			existsSel.WhereClause,
			orExpr(eq, nullTest(cloneColumnRef(innerCol), true), nullTest(cloneColumnRef(outerCol), true)),
		})
	} else {
		existsSel.WhereClause = combineAnd([]*pg_query.Node{existsSel.WhereClause, eq})
	}
	existsNode := &pg_query.Node{Node: &pg_query.Node_SubLink{SubLink: &pg_query.SubLink{
		SubLinkType: pg_query.SubLinkType_EXISTS_SUBLINK,
		Subselect:   &pg_query.Node{Node: &pg_query.Node_SelectStmt{SelectStmt: existsSel}},
	}}}
	if !notIn {
		return existsNode, true
	}
	return notExpr(existsNode), true
}

func outerSelectSafeForInExists(sel *pg_query.SelectStmt) bool {
	if sel == nil {
		return false
	}
	if sel.IntoClause != nil || sel.WithClause != nil {
		return false
	}
	if len(sel.WindowClause) > 0 || len(sel.LockingClause) > 0 {
		return false
	}
	if sel.Op != pg_query.SetOperation_SETOP_NONE && sel.Op != pg_query.SetOperation_SET_OPERATION_UNDEFINED {
		return false
	}
	// Single base table only — joins make correlation/alias rewriting unsafe.
	return len(sel.FromClause) == 1 && sel.FromClause[0].GetRangeVar() != nil
}

func subqueryEligibleForExists(sel *pg_query.SelectStmt) bool {
	if sel == nil || len(sel.TargetList) != 1 {
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
	if sel.Op != pg_query.SetOperation_SETOP_NONE && sel.Op != pg_query.SetOperation_SET_OPERATION_UNDEFINED {
		return false
	}
	if len(sel.FromClause) != 1 || sel.FromClause[0].GetRangeVar() == nil {
		return false
	}
	// Target must be a bare column (not expression/agg) so equality correlation is sound.
	target := resTargetVal(sel.TargetList[0])
	return unwrapColumnRefNode(target) != nil
}

func hasKind(kinds []string, want string) bool {
	for _, k := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

func findingsSuggestInRewrite(findings []PlanFinding) bool {
	for _, f := range findings {
		blob := strings.ToLower(f.Category + " " + f.Message)
		if strings.Contains(blob, "nested loop") || f.Category == CategoryLoopInflation || f.Category == CategorySeqScan {
			return true
		}
	}
	return false
}
