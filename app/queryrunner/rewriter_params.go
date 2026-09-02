package queryrunner

import pg_query "github.com/pganalyze/pg_query_go/v6"

// selectTreeContainsParamRef reports whether any predicate in the select tree
// uses positional parameters ($1, $2, ...). Rewrites are literals-only today.
func selectTreeContainsParamRef(sel *pg_query.SelectStmt) bool {
	if sel == nil {
		return false
	}
	if sel.WithClause != nil {
		for _, cte := range sel.WithClause.Ctes {
			if cteNode := cte.GetCommonTableExpr(); cteNode != nil {
				if sub := cteNode.Ctequery.GetSelectStmt(); sub != nil && selectTreeContainsParamRef(sub) {
					return true
				}
			}
		}
	}
	for _, from := range sel.FromClause {
		if rs := from.GetRangeSubselect(); rs != nil {
			if sub := rs.Subquery.GetSelectStmt(); sub != nil && selectTreeContainsParamRef(sub) {
				return true
			}
		}
	}
	return exprContainsParamRef(sel.WhereClause)
}

func exprContainsParamRef(node *pg_query.Node) bool {
	if node == nil {
		return false
	}
	if node.GetParamRef() != nil {
		return true
	}
	if ae := node.GetAExpr(); ae != nil {
		if exprContainsParamRef(ae.Lexpr) || exprContainsParamRef(ae.Rexpr) {
			return true
		}
	}
	if lst := node.GetList(); lst != nil {
		for _, item := range lst.Items {
			if exprContainsParamRef(item) {
				return true
			}
		}
	}
	if be := node.GetBoolExpr(); be != nil {
		for _, arg := range be.Args {
			if exprContainsParamRef(arg) {
				return true
			}
		}
	}
	if sl := node.GetSubLink(); sl != nil {
		if exprContainsParamRef(sl.Testexpr) {
			return true
		}
		if sub := sl.Subselect.GetSelectStmt(); sub != nil && selectTreeContainsParamRef(sub) {
			return true
		}
	}
	if ce := node.GetCoalesceExpr(); ce != nil {
		for _, arg := range ce.Args {
			if exprContainsParamRef(arg) {
				return true
			}
		}
	}
	if tc := node.GetTypeCast(); tc != nil {
		return exprContainsParamRef(tc.Arg)
	}
	if fc := node.GetFuncCall(); fc != nil {
		for _, arg := range fc.Args {
			if exprContainsParamRef(arg) {
				return true
			}
		}
	}
	if nt := node.GetNullTest(); nt != nil {
		return exprContainsParamRef(nt.Arg)
	}
	return false
}
