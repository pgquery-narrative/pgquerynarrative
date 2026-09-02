package queryrunner

import (
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// suggestLeftJoinAntiJoinToNotExists rewrites the classic anti-join idiom
//
//	SELECT a.* FROM a LEFT JOIN b ON b.a_id = a.id WHERE b.a_id IS NULL
//
// into an explicit
//
//	SELECT a.* FROM a WHERE NOT EXISTS (SELECT 1 FROM b WHERE b.a_id = a.id)
//
// so the planner can run a hash/merge anti-join or an index anti-join instead
// of building the full outer join and filtering it afterwards.
//
// It only fires when the IS NULL test is on a column that appears in an
// equality term of the ON clause. A row of b whose join column is NULL can
// never satisfy that equality, so it is only ever among the null-extended
// (no-match) rows — which makes the two forms equivalent without having to
// know whether b's column is declared NOT NULL. Every other shape fails
// closed: OR/NOT in the ON clause, a subquery in the ON clause, the right
// relation referenced anywhere that survives the rewrite, or any unqualified
// column (its table is ambiguous once the join is gone).
func suggestLeftJoinAntiJoinToNotExists(sql string, findings []PlanFinding) *RewriteCandidate {
	result, sel, ok := parseSingleSelect(sql)
	if !ok || sel.WhereClause == nil {
		return nil
	}
	if sel.WithClause != nil || sel.IntoClause != nil {
		return nil
	}
	if sel.Op != pg_query.SetOperation_SETOP_NONE && sel.Op != pg_query.SetOperation_SET_OPERATION_UNDEFINED {
		return nil
	}
	if len(sel.FromClause) != 1 {
		return nil
	}
	je := sel.FromClause[0].GetJoinExpr()
	if je == nil || je.Jointype != pg_query.JoinType_JOIN_LEFT || je.IsNatural {
		return nil
	}
	if len(je.UsingClause) > 0 || je.Quals == nil {
		return nil
	}
	leftRV := je.Larg.GetRangeVar()
	rightRV := je.Rarg.GetRangeVar()
	if leftRV == nil || rightRV == nil {
		return nil
	}
	leftAlias := rangeVarAlias(leftRV)
	rightAlias := rangeVarAlias(rightRV)
	if leftAlias == "" || rightAlias == "" || strings.EqualFold(leftAlias, rightAlias) {
		return nil
	}

	rightKeyCols := map[string]struct{}{}
	if !onClauseSafeForAntiJoin(je.Quals, rightAlias, rightKeyCols) || len(rightKeyCols) == 0 {
		return nil
	}

	// Peel `<rightAlias>.<keyCol> IS NULL` off the top-level AND of WHERE.
	conjuncts := splitAndConjuncts(sel.WhereClause)
	kept := make([]*pg_query.Node, 0, len(conjuncts))
	peeled := 0
	for _, c := range conjuncts {
		if col, isNull := nullTestColumn(c); isNull {
			if a, name := qualifiedColParts(col); strings.EqualFold(a, rightAlias) {
				if _, isKey := rightKeyCols[strings.ToLower(name)]; isKey {
					peeled++
					continue
				}
			}
		}
		kept = append(kept, c)
	}
	if peeled == 0 {
		return nil
	}

	// The right relation must not survive anywhere else, and no surviving
	// column may be unqualified (ambiguous ownership once the join is gone).
	check := make([]*pg_query.Node, 0, len(kept)+8)
	check = append(check, kept...)
	check = append(check, sel.TargetList...)
	check = append(check, sel.GroupClause...)
	check = append(check, sel.SortClause...)
	check = append(check, sel.DistinctClause...)
	check = append(check, sel.WindowClause...)
	if sel.HavingClause != nil {
		check = append(check, sel.HavingClause)
	}
	if referencesAliasOrBareCol(check, rightAlias) {
		return nil
	}

	existsSel := &pg_query.SelectStmt{
		TargetList: []*pg_query.Node{
			{Node: &pg_query.Node_ResTarget{ResTarget: &pg_query.ResTarget{Val: aConstInt(1)}}},
		},
		FromClause:  []*pg_query.Node{cloneNode(je.Rarg)},
		WhereClause: cloneNode(je.Quals),
	}
	existsNode := &pg_query.Node{Node: &pg_query.Node_SubLink{SubLink: &pg_query.SubLink{
		SubLinkType: pg_query.SubLinkType_EXISTS_SUBLINK,
		Subselect:   &pg_query.Node{Node: &pg_query.Node_SelectStmt{SelectStmt: existsSel}},
	}}}

	sel.FromClause = []*pg_query.Node{cloneNode(je.Larg)}
	sel.WhereClause = combineAnd(append(kept, notExpr(existsNode)))

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
	return &RewriteCandidate{
		SQL: outSQL,
		Rationale: "rewrite the LEFT JOIN ... WHERE " + rightAlias +
			".<key> IS NULL anti-join idiom to an explicit NOT EXISTS so the planner can run an anti-join / index lookup instead of materializing the outer join and filtering it",
		Category:   "left_join_antijoin_to_not_exists",
		Confidence: confidence,
	}
}

func rangeVarAlias(rv *pg_query.RangeVar) string {
	if rv == nil {
		return ""
	}
	if rv.Alias != nil && rv.Alias.Aliasname != "" {
		return rv.Alias.Aliasname
	}
	return rv.Relname
}

// onClauseSafeForAntiJoin returns true only for an ON clause built from AND of
// simple operator predicates (column / const / param / cast operands). Every
// `=` term with one side qualified by rightAlias contributes that column to
// keyCols. OR, NOT, subqueries, function calls → not safe.
func onClauseSafeForAntiJoin(n *pg_query.Node, rightAlias string, keyCols map[string]struct{}) bool {
	if n == nil {
		return false
	}
	if be := n.GetBoolExpr(); be != nil {
		if be.Boolop != pg_query.BoolExprType_AND_EXPR {
			return false
		}
		for _, a := range be.Args {
			if !onClauseSafeForAntiJoin(a, rightAlias, keyCols) {
				return false
			}
		}
		return true
	}
	ae := n.GetAExpr()
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP {
		return false
	}
	if !operandSimple(ae.Lexpr) || !operandSimple(ae.Rexpr) {
		return false
	}
	if aExprOpIs(ae, "=") {
		for _, side := range []*pg_query.Node{ae.Lexpr, ae.Rexpr} {
			if a, name := qualifiedColParts(side); name != "" && strings.EqualFold(a, rightAlias) {
				keyCols[strings.ToLower(name)] = struct{}{}
			}
		}
	}
	return true
}

func operandSimple(n *pg_query.Node) bool {
	if n == nil {
		return false
	}
	if n.GetColumnRef() != nil || n.GetAConst() != nil || n.GetParamRef() != nil {
		return true
	}
	if tc := n.GetTypeCast(); tc != nil {
		return operandSimple(tc.Arg)
	}
	return false
}

func qualifiedColParts(n *pg_query.Node) (alias, name string) {
	if n == nil {
		return "", ""
	}
	cr := n.GetColumnRef()
	if cr == nil {
		if tc := n.GetTypeCast(); tc != nil {
			return qualifiedColParts(tc.Arg)
		}
		return "", ""
	}
	if len(cr.Fields) != 2 {
		return "", ""
	}
	a, ok1 := stringNodeValue(cr.Fields[0])
	b, ok2 := stringNodeValue(cr.Fields[1])
	if !ok1 || !ok2 {
		return "", ""
	}
	return a, b
}

func nullTestColumn(n *pg_query.Node) (*pg_query.Node, bool) {
	if n == nil {
		return nil, false
	}
	nt := n.GetNullTest()
	if nt == nil || nt.Nulltesttype != pg_query.NullTestType_IS_NULL {
		return nil, false
	}
	return nt.Arg, true
}

func splitAndConjuncts(n *pg_query.Node) []*pg_query.Node {
	if n == nil {
		return nil
	}
	be := n.GetBoolExpr()
	if be == nil || be.Boolop != pg_query.BoolExprType_AND_EXPR {
		return []*pg_query.Node{n}
	}
	var out []*pg_query.Node
	for _, a := range be.Args {
		out = append(out, splitAndConjuncts(a)...)
	}
	return out
}

// referencesAliasOrBareCol reports whether any column reference in nodes is
// qualified by alias, or is unqualified (a single-field ref or a bare `*`)
// whose owning table can no longer be determined once a join is removed.
func referencesAliasOrBareCol(nodes []*pg_query.Node, alias string) bool {
	bad := false
	for _, n := range nodes {
		if n == nil {
			continue
		}
		walkColumnRefs(n, func(cr *pg_query.ColumnRef) {
			if bad {
				return
			}
			if len(cr.Fields) < 2 {
				bad = true
				return
			}
			if first, ok := stringNodeValue(cr.Fields[0]); !ok || strings.EqualFold(first, alias) {
				bad = true
			}
		})
		if bad {
			return true
		}
	}
	return false
}

// walkColumnRefs visits every ColumnRef reachable from m via protobuf
// reflection. ColumnRef fields hold only String / A_Star leaves, so there is
// nothing useful below one — stop there.
func walkColumnRefs(m proto.Message, fn func(*pg_query.ColumnRef)) {
	if m == nil {
		return
	}
	if cr, ok := m.(*pg_query.ColumnRef); ok {
		fn(cr)
		return
	}
	m.ProtoReflect().Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		if fd.Kind() != protoreflect.MessageKind && fd.Kind() != protoreflect.GroupKind {
			return true
		}
		if fd.IsMap() {
			return true
		}
		if fd.IsList() {
			l := v.List()
			for i := 0; i < l.Len(); i++ {
				walkColumnRefs(l.Get(i).Message().Interface(), fn)
			}
			return true
		}
		walkColumnRefs(v.Message().Interface(), fn)
		return true
	})
}

func cloneNode(n *pg_query.Node) *pg_query.Node {
	if n == nil {
		return nil
	}
	c, _ := proto.Clone(n).(*pg_query.Node)
	return c
}
