package queryrunner

import pg_query "github.com/pganalyze/pg_query_go/v6"

// applySargableRewritesToSelectTree walks WITH bodies, FROM subqueries, and the
// outer WHERE clause, applying sargable unwrap rewrites at each level. rc is
// passed down unchanged — a cast rewrite inside a nested CTE/subquery resolves
// its column against that nested SELECT's own FROM clause, never the outer
// query's.
func applySargableRewritesToSelectTree(rc rewriteCtx, sel *pg_query.SelectStmt, out *[]dateTruncRewrite) int {
	if sel == nil {
		return 0
	}
	total := 0
	if sel.WithClause != nil {
		for _, cte := range sel.WithClause.Ctes {
			if cteNode := cte.GetCommonTableExpr(); cteNode != nil {
				if sub := cteNode.Ctequery.GetSelectStmt(); sub != nil {
					total += applySargableRewritesToSelectTree(rc, sub, out)
				}
			}
		}
	}
	for _, from := range sel.FromClause {
		if rs := from.GetRangeSubselect(); rs != nil {
			if sub := rs.Subquery.GetSelectStmt(); sub != nil {
				total += applySargableRewritesToSelectTree(rc, sub, out)
			}
		}
	}
	if sel.WhereClause != nil {
		where := sel.WhereClause
		newWhere, nYM := rewriteExtractYearMonthAnd(where, out)
		if nYM > 0 && newWhere != nil {
			where = newWhere
			total += nYM
		}
		newWhere, nWrap := rewriteFunctionWrapInExpr(rc, sel, where, out)
		if nWrap > 0 && newWhere != nil {
			where = newWhere
			total += nWrap
		}
		sel.WhereClause = where
	}
	return total
}
