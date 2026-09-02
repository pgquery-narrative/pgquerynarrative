package queryrunner

import (
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// Parameterized sargable rewrites.
//
// The literal rewrites (rewriter.go / rewriter_sargable.go) parse the constant on
// the value side into a time.Time and compute an explicit range. Queries pulled
// from pg_stat_statements are normalized with positional parameters ($1, $2, ...)
// instead of constants, so those paths bail (selectTreeContainsParamRef).
//
// When the value side is a plain ParamRef and the shape is an equality or BETWEEN
// against a wrapped partition/index key, the rewrite is a pure structural
// transform — the bound value is whatever the caller binds, and
// `col >= $1 AND col < $1 + INTERVAL '1 month'` is sargable for any $1. We only
// handle `=` and `BETWEEN`, where the transform is unambiguous; parameterized
// inequalities are left alone (safe only if the bind is already unit-aligned,
// which we cannot verify).

// paramIntervalSpec maps a DATE_TRUNC unit to a PostgreSQL-valid interval literal
// ("quarter" is not a valid interval unit). Empty + false = fail closed.
func paramIntervalSpec(unit string) (string, bool) {
	switch normalizeTruncUnit(unit) {
	case "year":
		return "1 year", true
	case "quarter":
		return "3 months", true
	case "month":
		return "1 month", true
	case "week":
		return "1 week", true
	case "day":
		return "1 day", true
	case "hour":
		return "1 hour", true
	case "minute":
		return "1 minute", true
	case "second":
		return "1 second", true
	default:
		return "", false
	}
}

func cloneParamRef(n *pg_query.Node) *pg_query.Node {
	pr := n.GetParamRef()
	if pr == nil {
		return nil
	}
	return &pg_query.Node{Node: &pg_query.Node_ParamRef{ParamRef: &pg_query.ParamRef{
		Number:   pr.Number,
		Location: pr.Location,
	}}}
}

func isParamRef(n *pg_query.Node) bool {
	return n != nil && n.GetParamRef() != nil
}

// intervalConst builds `<spec>::interval` (deparses as e.g. `'1 month'::interval`).
func intervalConst(spec string) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_TypeCast{TypeCast: &pg_query.TypeCast{
		Arg: aConstString(spec),
		TypeName: &pg_query.TypeName{
			Names:   []*pg_query.Node{stringNode("pg_catalog"), stringNode("interval")},
			Typemod: -1,
		},
	}}}
}

// paramRangePred builds `col >= <lowParam> AND col < (<highParam> + INTERVAL '<spec>')`.
func paramRangePred(col, lowParam, highParam *pg_query.Node, spec string) *pg_query.Node {
	colLow := cloneColumnRef(col)
	colHigh := cloneColumnRef(col)
	low := cloneParamRef(lowParam)
	high := cloneParamRef(highParam)
	if colLow == nil || colHigh == nil || low == nil || high == nil {
		return nil
	}
	return andExpr(
		cmpExpr(">=", colLow, low),
		cmpExpr("<", colHigh, cmpExpr("+", high, intervalConst(spec))),
	)
}

// tryRewriteDateTruncEqualityParam: DATE_TRUNC(unit, col) = $n
//
//	→ col >= $n AND col < $n + INTERVAL '1 <unit>'
func tryRewriteDateTruncEqualityParam(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	unit, col, param, ok := splitDateTruncFuncParam(ae.Lexpr, ae.Rexpr)
	if !ok {
		unit, col, param, ok = splitDateTruncFuncParam(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	spec, ok := paramIntervalSpec(unit)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	pred := paramRangePred(col, param, param, spec)
	if pred == nil {
		return nil, dateTruncRewrite{}, false
	}
	return pred, dateTruncRewrite{Unit: normalizeTruncUnit(unit), Column: columnRefName(col), Kind: "date_trunc_param"}, true
}

// tryRewriteDateTruncBetweenParam: DATE_TRUNC(unit, col) BETWEEN $a AND $b
//
//	→ col >= $a AND col < $b + INTERVAL '1 <unit>'
func tryRewriteDateTruncBetweenParam(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_BETWEEN {
		return nil, dateTruncRewrite{}, false
	}
	unit, col, ok := splitDateTruncFunc(ae.Lexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	low, high, ok := splitBetweenParams(ae.Rexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	spec, ok := paramIntervalSpec(unit)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	pred := paramRangePred(col, low, high, spec)
	if pred == nil {
		return nil, dateTruncRewrite{}, false
	}
	return pred, dateTruncRewrite{Unit: normalizeTruncUnit(unit), Column: columnRefName(col), Kind: "date_trunc_param"}, true
}

// tryRewriteCastDateEqualityParam: col::date = $n / CAST(col AS date) = $n
//
//	→ col >= $n AND col < $n + INTERVAL '1 day'
func tryRewriteCastDateEqualityParam(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	col, param, ok := splitCastDateColumnParam(ae.Lexpr, ae.Rexpr)
	if !ok {
		col, param, ok = splitCastDateColumnParam(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	pred := paramRangePred(col, param, param, "1 day")
	if pred == nil {
		return nil, dateTruncRewrite{}, false
	}
	return pred, dateTruncRewrite{Unit: "day", Column: columnRefName(col), Kind: "cast_date_param"}, true
}

// tryRewriteCastDateBetweenParam: col::date BETWEEN $a AND $b
//
//	→ col >= $a AND col < $b + INTERVAL '1 day'
func tryRewriteCastDateBetweenParam(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_BETWEEN {
		return nil, dateTruncRewrite{}, false
	}
	col, ok := splitCastDateColumn(ae.Lexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	low, high, ok := splitBetweenParams(ae.Rexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	pred := paramRangePred(col, low, high, "1 day")
	if pred == nil {
		return nil, dateTruncRewrite{}, false
	}
	return pred, dateTruncRewrite{Unit: "day", Column: columnRefName(col), Kind: "cast_date_param"}, true
}

// splitDateTruncFuncParam matches DATE_TRUNC(unit, col) on a and a bare ParamRef on b.
func splitDateTruncFuncParam(a, b *pg_query.Node) (unit string, col, param *pg_query.Node, ok bool) {
	unit, col, ok = splitDateTruncFunc(a)
	if !ok || !isParamRef(b) {
		return "", nil, nil, false
	}
	return strings.ToLower(unit), col, b, true
}

// splitCastDateColumnParam matches col::date on a and a bare ParamRef on b.
func splitCastDateColumnParam(a, b *pg_query.Node) (col, param *pg_query.Node, ok bool) {
	col, ok = splitCastDateColumn(a)
	if !ok || !isParamRef(b) {
		return nil, nil, false
	}
	return col, b, true
}

// splitBetweenParams extracts the two ParamRef bounds of a BETWEEN Rexpr. Both
// bounds must be positional parameters.
func splitBetweenParams(n *pg_query.Node) (low, high *pg_query.Node, ok bool) {
	if n == nil {
		return nil, nil, false
	}
	if lst := n.GetList(); lst != nil && len(lst.Items) == 2 {
		if isParamRef(lst.Items[0]) && isParamRef(lst.Items[1]) {
			return lst.Items[0], lst.Items[1], true
		}
		return nil, nil, false
	}
	if be := n.GetBoolExpr(); be != nil && be.Boolop == pg_query.BoolExprType_AND_EXPR && len(be.Args) == 2 {
		if isParamRef(be.Args[0]) && isParamRef(be.Args[1]) {
			return be.Args[0], be.Args[1], true
		}
	}
	return nil, nil, false
}
