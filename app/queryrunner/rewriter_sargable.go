package queryrunner

import (
	"strconv"
	"strings"
	"time"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// tryRewriteExtractYearEquality rewrites EXTRACT(YEAR FROM col) = N and
// date_part('year', col) = N into a closed year range on col. Month-only
// EXTRACT is not a contiguous range and is left to rewriteExtractYearMonthAnd.
func tryRewriteExtractYearEquality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	field, colNode, year, ok := splitExtractEquality(ae.Lexpr, ae.Rexpr)
	if !ok {
		field, colNode, year, ok = splitExtractEquality(ae.Rexpr, ae.Lexpr)
	}
	if !ok || !strings.EqualFold(field, "year") {
		return nil, dateTruncRewrite{}, false
	}
	if year < 1 || year > 9999 {
		return nil, dateTruncRewrite{}, false
	}
	start := time.Date(int(year), 1, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(1, 0, 0)
	return rangeOnColumn(colNode, start, end, "year", "extract")
}

func tryRewriteToCharEquality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	colNode, format, value, ok := splitToCharEquality(ae.Lexpr, ae.Rexpr)
	if !ok {
		colNode, format, value, ok = splitToCharEquality(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	start, end, unit, ok := toCharRange(format, value)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	return rangeOnColumn(colNode, start, end, unit, "to_char")
}

func tryRewriteCoalesceEquality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	colNode, defNode, constNode, ok := splitCoalesceEquality(ae.Lexpr, ae.Rexpr)
	if !ok {
		colNode, defNode, constNode, ok = splitCoalesceEquality(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	col := cloneColumnRef(colNode)
	rhs := constNode
	if col == nil || rhs == nil {
		return nil, dateTruncRewrite{}, false
	}
	eq := cmpExpr("=", col, rhs)
	var replacement *pg_query.Node
	if constEqual(defNode, constNode) {
		replacement = orExpr(eq, nullTest(cloneColumnRef(colNode), true))
	} else {
		replacement = eq
	}
	return replacement, dateTruncRewrite{Column: columnRefName(colNode), Kind: "coalesce"}, true
}

func tryRewriteDateTruncBetween(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_BETWEEN {
		return nil, dateTruncRewrite{}, false
	}
	unit, colNode, ok := splitDateTruncFunc(ae.Lexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	low, high, ok := splitBetweenBounds(ae.Rexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	start := periodStartAtOrAfter(unit, low)
	endEx := periodEndExclusiveForTruncUpper(unit, high)
	return rangeOnColumn(colNode, start, endEx, unit, "date_trunc")
}

func tryRewriteCastDateBetween(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_BETWEEN {
		return nil, dateTruncRewrite{}, false
	}
	colNode, ok := splitCastDateColumn(ae.Lexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	low, high, ok := splitBetweenBounds(ae.Rexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	start := truncateTime(low, "day")
	endEx := truncateTime(high, "day").AddDate(0, 0, 1)
	return rangeOnColumn(colNode, start, endEx, "day", "cast_date")
}

func tryRewriteCastDateInequality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP {
		return nil, dateTruncRewrite{}, false
	}
	op := aExprOpName(ae)
	switch op {
	case ">=", ">", "<=", "<":
	default:
		return nil, dateTruncRewrite{}, false
	}
	colNode, constNode, ok := splitCastDateCompare(ae.Lexpr, ae.Rexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	bound, typ, ok := parseTemporalConst(constNode)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	bound = truncateTime(bound, "day")
	col := cloneColumnRef(colNode)
	if col == nil {
		return nil, dateTruncRewrite{}, false
	}
	info := dateTruncRewrite{Unit: "day", Column: columnRefName(colNode), Kind: "cast_date"}
	switch op {
	case ">=":
		lit := temporalLiteralNode(bound, typ, "day")
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr(">=", col, lit), info, true
	case ">":
		lit := temporalLiteralNode(bound.AddDate(0, 0, 1), typ, "day")
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr(">=", col, lit), info, true
	case "<=":
		lit := temporalLiteralNode(bound.AddDate(0, 0, 1), typ, "day")
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr("<", col, lit), info, true
	case "<":
		lit := temporalLiteralNode(bound, typ, "day")
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr("<", col, lit), info, true
	default:
		return nil, dateTruncRewrite{}, false
	}
}

func splitCastDateColumn(n *pg_query.Node) (*pg_query.Node, bool) {
	if n == nil {
		return nil, false
	}
	tc := n.GetTypeCast()
	if tc == nil || !strings.EqualFold(typeNameLast(tc.TypeName), "date") {
		return nil, false
	}
	col := columnRefUnder(tc.Arg)
	if col == nil {
		return nil, false
	}
	return col, true
}

func splitCastDateCompare(a, b *pg_query.Node) (col, constNode *pg_query.Node, ok bool) {
	col, ok = splitCastDateColumn(a)
	if !ok || !isTemporalConst(b) {
		return nil, nil, false
	}
	return col, b, true
}

func tryRewriteDateTruncInequality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP {
		return nil, dateTruncRewrite{}, false
	}
	op := aExprOpName(ae)
	switch op {
	case ">=", ">", "<=", "<":
	default:
		return nil, dateTruncRewrite{}, false
	}
	unit, colNode, constNode, ok := splitDateTruncCompare(ae.Lexpr, ae.Rexpr)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	bound, typ, ok := parseTemporalConst(constNode)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	col := cloneColumnRef(colNode)
	if col == nil {
		return nil, dateTruncRewrite{}, false
	}
	info := dateTruncRewrite{Unit: unit, Column: columnRefName(colNode), Kind: "date_trunc"}
	switch op {
	case ">=":
		start := periodStartAtOrAfter(unit, bound)
		lit := temporalLiteralNode(start, typ, unit)
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr(">=", col, lit), info, true
	case ">":
		start := periodStartAfter(unit, bound)
		lit := temporalLiteralNode(start, typ, unit)
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr(">=", col, lit), info, true
	case "<=":
		endEx := periodEndExclusiveForTruncUpper(unit, bound)
		lit := temporalLiteralNode(endEx, typ, unit)
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr("<", col, lit), info, true
	case "<":
		endEx := periodEndExclusiveForTruncLess(unit, bound)
		lit := temporalLiteralNode(endEx, typ, unit)
		if lit == nil {
			return nil, dateTruncRewrite{}, false
		}
		return cmpExpr("<", col, lit), info, true
	default:
		return nil, dateTruncRewrite{}, false
	}
}

func tryRewriteNumericCastEquality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	colNode, rhs, ok := splitNumericCastEquality(ae.Lexpr, ae.Rexpr)
	if !ok {
		colNode, rhs, ok = splitNumericCastEquality(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	col := cloneColumnRef(colNode)
	if col == nil || rhs == nil {
		return nil, dateTruncRewrite{}, false
	}
	return cmpExpr("=", col, rhs), dateTruncRewrite{Column: columnRefName(colNode), Kind: "numeric_cast"}, true
}

func tryRewriteTextCastEquality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	colNode, lit, ok := splitTextCastEquality(ae.Lexpr, ae.Rexpr)
	if !ok {
		colNode, lit, ok = splitTextCastEquality(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(lit), 10, 32)
	if err != nil {
		return nil, dateTruncRewrite{}, false
	}
	col := cloneColumnRef(colNode)
	if col == nil {
		return nil, dateTruncRewrite{}, false
	}
	return cmpExpr("=", col, aConstInt(int32(n))), dateTruncRewrite{Column: columnRefName(colNode), Kind: "text_cast"}, true
}

func rangeOnColumn(colNode *pg_query.Node, start, end time.Time, unit, kind string) (*pg_query.Node, dateTruncRewrite, bool) {
	startLit := temporalLiteralNode(start, temporalDate, unit)
	endLit := temporalLiteralNode(end, temporalDate, unit)
	colStart := cloneColumnRef(colNode)
	colEnd := cloneColumnRef(colNode)
	if colStart == nil || colEnd == nil || startLit == nil || endLit == nil {
		return nil, dateTruncRewrite{}, false
	}
	rangePred := andExpr(
		cmpExpr(">=", colStart, startLit),
		cmpExpr("<", colEnd, endLit),
	)
	return rangePred, dateTruncRewrite{Unit: unit, Column: columnRefName(colNode), Kind: kind}, true
}

func splitExtractEquality(a, b *pg_query.Node) (field string, col *pg_query.Node, value int64, ok bool) {
	fc := unwrapFuncCall(a)
	if fc == nil || !isExtractOrDatePart(fc) || len(fc.Args) < 2 {
		return "", nil, 0, false
	}
	field, ok = stringConstValue(fc.Args[0])
	if !ok || field == "" {
		return "", nil, 0, false
	}
	col = unwrapColumnRefNode(fc.Args[1])
	if col == nil {
		return "", nil, 0, false
	}
	value, ok = intConstValue(b)
	if !ok {
		return "", nil, 0, false
	}
	return strings.ToLower(field), col, value, true
}

func isExtractOrDatePart(fc *pg_query.FuncCall) bool {
	if fc == nil || len(fc.Funcname) == 0 {
		return false
	}
	last := fc.Funcname[len(fc.Funcname)-1]
	name, ok := stringNodeValue(last)
	return ok && (strings.EqualFold(name, "extract") || strings.EqualFold(name, "date_part"))
}

func splitToCharEquality(a, b *pg_query.Node) (col *pg_query.Node, format, value string, ok bool) {
	fc := unwrapFuncCall(a)
	if fc == nil || !isToCharFunc(fc) || len(fc.Args) < 2 {
		return nil, "", "", false
	}
	col = unwrapColumnRefNode(fc.Args[0])
	if col == nil {
		return nil, "", "", false
	}
	format, ok = stringConstValue(fc.Args[1])
	if !ok || format == "" {
		return nil, "", "", false
	}
	value, ok = stringConstValue(b)
	if !ok || value == "" {
		return nil, "", "", false
	}
	return col, format, value, true
}

func isToCharFunc(fc *pg_query.FuncCall) bool {
	if fc == nil || len(fc.Funcname) == 0 {
		return false
	}
	last := fc.Funcname[len(fc.Funcname)-1]
	name, ok := stringNodeValue(last)
	return ok && strings.EqualFold(name, "to_char")
}

func toCharRange(format, value string) (start, end time.Time, unit string, ok bool) {
	format = strings.TrimSpace(format)
	value = strings.TrimSpace(value)
	switch format {
	case "YYYY":
		year, err := strconv.Atoi(value)
		if err != nil || year < 1 || year > 9999 || len(value) != 4 {
			return time.Time{}, time.Time{}, "", false
		}
		start = time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC)
		return start, start.AddDate(1, 0, 0), "year", true
	case "YYYY-MM":
		t, err := time.ParseInLocation("2006-01", value, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, "", false
		}
		return t, t.AddDate(0, 1, 0), "month", true
	case "YYYY-MM-DD":
		t, err := time.ParseInLocation("2006-01-02", value, time.UTC)
		if err != nil {
			return time.Time{}, time.Time{}, "", false
		}
		return t, t.AddDate(0, 0, 1), "day", true
	default:
		return time.Time{}, time.Time{}, "", false
	}
}

func splitCoalesceEquality(a, b *pg_query.Node) (col, def, constNode *pg_query.Node, ok bool) {
	if a == nil || b == nil || !isConstNode(b) {
		return nil, nil, nil, false
	}
	ce := a.GetCoalesceExpr()
	if ce == nil || len(ce.Args) != 2 {
		return nil, nil, nil, false
	}
	col = unwrapColumnRefNode(ce.Args[0])
	if col == nil || !isConstNode(ce.Args[1]) {
		return nil, nil, nil, false
	}
	return col, ce.Args[1], b, true
}

func splitTextCastEquality(a, b *pg_query.Node) (col *pg_query.Node, lit string, ok bool) {
	if a == nil || b == nil {
		return nil, "", false
	}
	tc := a.GetTypeCast()
	if tc == nil || !isTextTypeName(typeNameLast(tc.TypeName)) {
		return nil, "", false
	}
	col = unwrapColumnRefNode(tc.Arg)
	if col == nil {
		return nil, "", false
	}
	lit, ok = stringConstValue(b)
	if !ok {
		if n, iok := intConstValue(b); iok {
			return col, strconv.FormatInt(n, 10), true
		}
		return nil, "", false
	}
	return col, lit, true
}

func isTextTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "text", "varchar", "bpchar", "character", "citext":
		return true
	default:
		return false
	}
}

func isNumericTypeName(name string) bool {
	switch strings.ToLower(name) {
	case "int2", "int4", "int8", "integer", "bigint", "smallint",
		"numeric", "decimal", "float4", "float8", "real", "double precision":
		return true
	default:
		return false
	}
}

func splitNumericCastEquality(a, b *pg_query.Node) (col *pg_query.Node, rhs *pg_query.Node, ok bool) {
	if a == nil || b == nil {
		return nil, nil, false
	}
	tc := a.GetTypeCast()
	if tc == nil || !isNumericTypeName(typeNameLast(tc.TypeName)) {
		return nil, nil, false
	}
	col = unwrapColumnRefNode(tc.Arg)
	if col == nil {
		return nil, nil, false
	}
	if isConstNode(b) {
		return col, b, true
	}
	return nil, nil, false
}

func splitDateTruncFunc(n *pg_query.Node) (unit string, col *pg_query.Node, ok bool) {
	fc := unwrapFuncCall(n)
	if fc == nil || !isDateTruncFunc(fc) || len(fc.Args) < 2 {
		return "", nil, false
	}
	unit, ok = stringConstValue(fc.Args[0])
	if !ok || unit == "" {
		return "", nil, false
	}
	col = unwrapColumnRefNode(fc.Args[1])
	if col == nil {
		return "", nil, false
	}
	return strings.ToLower(unit), col, true
}

func splitDateTruncCompare(a, b *pg_query.Node) (unit string, col, constNode *pg_query.Node, ok bool) {
	unit, col, ok = splitDateTruncFunc(a)
	if !ok || !isTemporalConst(b) {
		return "", nil, nil, false
	}
	return unit, col, b, true
}

func splitBetweenBounds(n *pg_query.Node) (low, high time.Time, ok bool) {
	if n == nil {
		return time.Time{}, time.Time{}, false
	}
	if lst := n.GetList(); lst != nil && len(lst.Items) == 2 {
		low, _, ok = parseTemporalConst(lst.Items[0])
		if !ok {
			return time.Time{}, time.Time{}, false
		}
		high, _, ok = parseTemporalConst(lst.Items[1])
		if !ok {
			return time.Time{}, time.Time{}, false
		}
		return low, high, true
	}
	be := n.GetBoolExpr()
	if be == nil || be.Boolop != pg_query.BoolExprType_AND_EXPR || len(be.Args) != 2 {
		return time.Time{}, time.Time{}, false
	}
	low, _, ok = parseTemporalConst(be.Args[0])
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	high, _, ok = parseTemporalConst(be.Args[1])
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return low, high, true
}

func aExprOpName(ae *pg_query.A_Expr) string {
	if ae == nil || len(ae.Name) != 1 {
		return ""
	}
	s, ok := stringNodeValue(ae.Name[0])
	if !ok {
		return ""
	}
	return s
}

func periodStartAtOrAfter(unit string, bound time.Time) time.Time {
	t := truncateTime(bound, unit)
	if !t.Before(bound) {
		return t
	}
	end, ok := rangeEnd(t, unit)
	if !ok {
		return bound
	}
	return end
}

func periodStartAfter(unit string, bound time.Time) time.Time {
	t := truncateTime(bound, unit)
	end, ok := rangeEnd(t, unit)
	if !ok {
		return bound.AddDate(0, 0, 1)
	}
	return end
}

func periodEndExclusiveForTruncUpper(unit string, bound time.Time) time.Time {
	t := truncateTime(bound, unit)
	end, ok := rangeEnd(t, unit)
	if !ok {
		return bound.AddDate(0, 0, 1)
	}
	return end
}

func periodEndExclusiveForTruncLess(unit string, bound time.Time) time.Time {
	t := truncateTime(bound, unit)
	if t.Equal(bound) {
		return t
	}
	end, ok := rangeEnd(t, unit)
	if !ok {
		return bound
	}
	return end
}

type extractEq struct {
	idx     int
	field   string
	col     string
	colNode *pg_query.Node
	value   int64
}

// rewriteExtractYearMonthAnd replaces EXTRACT(YEAR FROM col)=Y AND
// EXTRACT(MONTH FROM col)=M on the same column with a closed month range.
func rewriteExtractYearMonthAnd(node *pg_query.Node, out *[]dateTruncRewrite) (*pg_query.Node, int) {
	if node == nil {
		return nil, 0
	}
	be := node.GetBoolExpr()
	if be == nil {
		return node, 0
	}
	total := 0
	args := make([]*pg_query.Node, len(be.Args))
	changed := false
	for i, arg := range be.Args {
		rewritten, n := rewriteExtractYearMonthAnd(arg, out)
		total += n
		if n > 0 && rewritten != nil {
			args[i] = rewritten
			changed = true
		} else {
			args[i] = arg
		}
	}
	if be.Boolop != pg_query.BoolExprType_AND_EXPR {
		if !changed {
			return node, 0
		}
		newBe := &pg_query.BoolExpr{Boolop: be.Boolop, Args: args, Location: be.Location}
		return &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: newBe}}, total
	}

	var years, months []extractEq
	for i, arg := range args {
		field, colNode, val, ok := extractFieldEqualityNode(arg)
		if !ok {
			continue
		}
		item := extractEq{idx: i, field: field, col: columnRefName(colNode), colNode: colNode, value: val}
		switch field {
		case "year":
			years = append(years, item)
		case "month":
			months = append(months, item)
		}
	}
	used := map[int]struct{}{}
	replaceAt := map[int]*pg_query.Node{}
	for _, y := range years {
		if _, ok := used[y.idx]; ok {
			continue
		}
		for _, m := range months {
			if _, ok := used[m.idx]; ok {
				continue
			}
			if y.col == "" || y.col != m.col {
				continue
			}
			if m.value < 1 || m.value > 12 || y.value < 1 || y.value > 9999 {
				continue
			}
			start := time.Date(int(y.value), time.Month(m.value), 1, 0, 0, 0, 0, time.UTC)
			end := start.AddDate(0, 1, 0)
			rangePred, info, ok := rangeOnColumn(y.colNode, start, end, "month", "extract")
			if !ok {
				continue
			}
			*out = append(*out, info)
			replaceAt[y.idx] = rangePred
			used[y.idx] = struct{}{}
			used[m.idx] = struct{}{}
			total++
			changed = true
			break
		}
	}
	if !changed {
		return node, 0
	}
	newArgs := make([]*pg_query.Node, 0, len(args))
	for i, arg := range args {
		if _, skip := used[i]; skip {
			if repl, ok := replaceAt[i]; ok {
				newArgs = append(newArgs, repl)
			}
			continue
		}
		newArgs = append(newArgs, arg)
	}
	return combineAnd(newArgs), total
}

func extractFieldEqualityNode(n *pg_query.Node) (field string, col *pg_query.Node, value int64, ok bool) {
	if n == nil {
		return "", nil, 0, false
	}
	ae := n.GetAExpr()
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return "", nil, 0, false
	}
	field, col, value, ok = splitExtractEquality(ae.Lexpr, ae.Rexpr)
	if ok {
		return field, col, value, true
	}
	return splitExtractEquality(ae.Rexpr, ae.Lexpr)
}
