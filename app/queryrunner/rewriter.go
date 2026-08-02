package queryrunner

import (
	"fmt"
	"strings"
	"time"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// RewriteCandidate is a system-generated SQL rewrite proposal with a short
// rationale. Candidates are suggestions for human review — nothing executes them.
type RewriteCandidate struct {
	SQL        string
	Rationale  string
	Category   string
	Confidence string
}

// SuggestRewrites analyzes sql (and optional plan findings) and returns
// candidate rewrites. The first supported pattern unwraps
// DATE_TRUNC(unit, col) = <const> into a closed sargable range on col so
// partition pruning and indexes can apply.
//
// Findings are optional evidence: when they mention date_trunc / function-wrap
// / partition pruning, confidence is raised to "high". The engine still works
// from pasted SQL alone when findings are empty.
func SuggestRewrites(sql string, findings []PlanFinding) []RewriteCandidate {
	trimmed := strings.TrimSpace(sql)
	trimmed = strings.TrimSuffix(trimmed, ";")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return nil
	}

	result, err := pg_query.Parse(trimmed)
	if err != nil || len(result.Stmts) != 1 {
		return nil
	}
	stmt := result.Stmts[0].GetStmt()
	if stmt == nil || stmt.GetSelectStmt() == nil {
		return nil
	}
	sel := stmt.GetSelectStmt()
	if sel.WhereClause == nil {
		return nil
	}

	evidenceBoost := findingsSuggestDateTruncRewrite(findings)
	var replacements []dateTruncRewrite
	newWhere, n := rewriteDateTruncInExpr(sel.WhereClause, &replacements)
	if n == 0 || newWhere == nil {
		return nil
	}
	sel.WhereClause = newWhere

	outSQL, err := pg_query.Deparse(result)
	if err != nil {
		return nil
	}
	outSQL = strings.TrimSpace(outSQL)
	if outSQL == "" || strings.EqualFold(outSQL, trimmed) {
		return nil
	}

	units := uniqueUnits(replacements)
	rationale := fmt.Sprintf(
		"unwrap DATE_TRUNC(%s) equality to a sargable range predicate so PostgreSQL can prune partitions and use indexes on the column",
		strings.Join(quoteUnits(units), "/"),
	)
	confidence := "medium"
	if evidenceBoost {
		confidence = "high"
	}

	return []RewriteCandidate{{
		SQL:        outSQL,
		Rationale:  rationale,
		Category:   "function_wrap",
		Confidence: confidence,
	}}
}

type dateTruncRewrite struct {
	Unit   string
	Column string
}

func findingsSuggestDateTruncRewrite(findings []PlanFinding) bool {
	for _, f := range findings {
		blob := strings.ToLower(f.Category + " " + f.Message + " " + strings.Join(f.Evidence, " "))
		if strings.Contains(blob, "date_trunc") ||
			strings.Contains(blob, "function-wrapped") ||
			strings.Contains(blob, "function wrap") ||
			f.Category == CategoryPartitionPruning {
			return true
		}
	}
	return false
}

func rewriteDateTruncInExpr(node *pg_query.Node, out *[]dateTruncRewrite) (*pg_query.Node, int) {
	if node == nil {
		return nil, 0
	}
	if ae := node.GetAExpr(); ae != nil {
		if replacement, info, ok := tryRewriteDateTruncEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		return node, 0
	}
	if be := node.GetBoolExpr(); be != nil {
		total := 0
		args := make([]*pg_query.Node, len(be.Args))
		changed := false
		for i, arg := range be.Args {
			rewritten, n := rewriteDateTruncInExpr(arg, out)
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
		newBe := &pg_query.BoolExpr{
			Boolop:   be.Boolop,
			Args:     args,
			Location: be.Location,
		}
		return &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: newBe}}, total
	}
	return node, 0
}

func tryRewriteDateTruncEquality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}

	unit, colNode, constNode, ok := splitDateTruncEquality(ae.Lexpr, ae.Rexpr)
	if !ok {
		unit, colNode, constNode, ok = splitDateTruncEquality(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}

	start, typ, ok := parseTemporalConst(constNode)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	start = truncateTime(start, unit)
	end, ok := rangeEnd(start, unit)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}

	startLit := temporalLiteralNode(start, typ, unit)
	endLit := temporalLiteralNode(end, typ, unit)
	colStart := cloneColumnRef(colNode)
	colEnd := cloneColumnRef(colNode)
	if colStart == nil || colEnd == nil || startLit == nil || endLit == nil {
		return nil, dateTruncRewrite{}, false
	}

	rangePred := andExpr(
		cmpExpr(">=", colStart, startLit),
		cmpExpr("<", colEnd, endLit),
	)
	info := dateTruncRewrite{Unit: unit, Column: columnRefName(colNode)}
	return rangePred, info, true
}

func splitDateTruncEquality(a, b *pg_query.Node) (unit string, col, constNode *pg_query.Node, ok bool) {
	fc := unwrapFuncCall(a)
	if fc == nil || !isDateTruncFunc(fc) || len(fc.Args) < 2 {
		return "", nil, nil, false
	}
	unit, ok = stringConstValue(fc.Args[0])
	if !ok || unit == "" {
		return "", nil, nil, false
	}
	col = unwrapColumnRefNode(fc.Args[1])
	if col == nil {
		return "", nil, nil, false
	}
	if !isTemporalConst(b) {
		return "", nil, nil, false
	}
	return strings.ToLower(unit), col, b, true
}

func unwrapFuncCall(n *pg_query.Node) *pg_query.FuncCall {
	if n == nil {
		return nil
	}
	if fc := n.GetFuncCall(); fc != nil {
		return fc
	}
	// Allow trivial TypeCast wrappers around the call (rare).
	if tc := n.GetTypeCast(); tc != nil {
		return unwrapFuncCall(tc.Arg)
	}
	return nil
}

func isDateTruncFunc(fc *pg_query.FuncCall) bool {
	if fc == nil || len(fc.Funcname) == 0 {
		return false
	}
	// Funcname may be ["pg_catalog","date_trunc"] or ["date_trunc"].
	last := fc.Funcname[len(fc.Funcname)-1]
	name, ok := stringNodeValue(last)
	return ok && strings.EqualFold(name, "date_trunc")
}

func unwrapColumnRefNode(n *pg_query.Node) *pg_query.Node {
	if n == nil {
		return nil
	}
	if n.GetColumnRef() != nil {
		return n
	}
	if tc := n.GetTypeCast(); tc != nil {
		return unwrapColumnRefNode(tc.Arg)
	}
	return nil
}

func columnRefName(n *pg_query.Node) string {
	cr := n.GetColumnRef()
	if cr == nil || len(cr.Fields) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cr.Fields))
	for _, f := range cr.Fields {
		if s, ok := stringNodeValue(f); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, ".")
}

func cloneColumnRef(n *pg_query.Node) *pg_query.Node {
	cr := n.GetColumnRef()
	if cr == nil {
		return nil
	}
	fields := make([]*pg_query.Node, 0, len(cr.Fields))
	for _, f := range cr.Fields {
		if s, ok := stringNodeValue(f); ok {
			fields = append(fields, stringNode(s))
		} else {
			return nil
		}
	}
	return &pg_query.Node{Node: &pg_query.Node_ColumnRef{ColumnRef: &pg_query.ColumnRef{
		Fields:   fields,
		Location: cr.Location,
	}}}
}

func isTemporalConst(n *pg_query.Node) bool {
	_, _, ok := parseTemporalConst(n)
	return ok
}

type temporalType int

const (
	temporalDate temporalType = iota
	temporalTimestamp
	temporalUnknown
)

func parseTemporalConst(n *pg_query.Node) (time.Time, temporalType, bool) {
	if n == nil {
		return time.Time{}, temporalUnknown, false
	}
	if tc := n.GetTypeCast(); tc != nil {
		typ := typeNameLast(tc.TypeName)
		inner := tc.Arg
		s, ok := stringConstValue(inner)
		if !ok {
			// Numeric-ish constants are not dates.
			return time.Time{}, temporalUnknown, false
		}
		t, ok := parseTimeString(s)
		if !ok {
			return time.Time{}, temporalUnknown, false
		}
		switch strings.ToLower(typ) {
		case "date":
			return t, temporalDate, true
		case "timestamp", "timestamptz":
			return t, temporalTimestamp, true
		default:
			if looksLikeDateOnly(s) {
				return t, temporalDate, true
			}
			return t, temporalTimestamp, true
		}
	}
	if s, ok := stringConstValue(n); ok {
		t, ok := parseTimeString(s)
		if !ok {
			return time.Time{}, temporalUnknown, false
		}
		if looksLikeDateOnly(s) {
			return t, temporalDate, true
		}
		return t, temporalTimestamp, true
	}
	return time.Time{}, temporalUnknown, false
}

func typeNameLast(tn *pg_query.TypeName) string {
	if tn == nil || len(tn.Names) == 0 {
		return ""
	}
	s, _ := stringNodeValue(tn.Names[len(tn.Names)-1])
	return s
}

func parseTimeString(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, layout := range layouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func looksLikeDateOnly(s string) bool {
	s = strings.TrimSpace(s)
	_, err := time.ParseInLocation("2006-01-02", s, time.UTC)
	return err == nil && len(s) == 10
}

func truncateTime(t time.Time, unit string) time.Time {
	switch normalizeTruncUnit(unit) {
	case "year":
		return time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	case "quarter":
		m := ((int(t.Month())-1)/3)*3 + 1
		return time.Date(t.Year(), time.Month(m), 1, 0, 0, 0, 0, t.Location())
	case "month":
		return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	case "week":
		// PostgreSQL DATE_TRUNC('week') uses Monday as week start.
		weekday := int(t.Weekday())
		if weekday == 0 {
			weekday = 7
		}
		day := t.AddDate(0, 0, -(weekday - 1))
		return time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, t.Location())
	case "day":
		return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	case "hour":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	case "minute":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location())
	case "second":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, t.Location())
	default:
		return t
	}
}

func rangeEnd(start time.Time, unit string) (time.Time, bool) {
	switch normalizeTruncUnit(unit) {
	case "year":
		return start.AddDate(1, 0, 0), true
	case "quarter":
		return start.AddDate(0, 3, 0), true
	case "month":
		return start.AddDate(0, 1, 0), true
	case "week":
		return start.AddDate(0, 0, 7), true
	case "day":
		return start.AddDate(0, 0, 1), true
	case "hour":
		return start.Add(time.Hour), true
	case "minute":
		return start.Add(time.Minute), true
	case "second":
		return start.Add(time.Second), true
	default:
		return time.Time{}, false
	}
}

func normalizeTruncUnit(unit string) string {
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "year", "years", "yyyy", "yy":
		return "year"
	case "quarter", "quarters":
		return "quarter"
	case "month", "months", "mon", "mons", "mm":
		return "month"
	case "week", "weeks":
		return "week"
	case "day", "days", "dd", "d":
		return "day"
	case "hour", "hours", "hh":
		return "hour"
	case "minute", "minutes", "mi":
		return "minute"
	case "second", "seconds", "ss", "s":
		return "second"
	default:
		return strings.ToLower(unit)
	}
}

func temporalLiteralNode(t time.Time, typ temporalType, unit string) *pg_query.Node {
	useDate := typ == temporalDate || normalizeTruncUnit(unit) == "year" ||
		normalizeTruncUnit(unit) == "quarter" || normalizeTruncUnit(unit) == "month" ||
		normalizeTruncUnit(unit) == "week" || normalizeTruncUnit(unit) == "day"
	if useDate && typ != temporalTimestamp {
		s := t.Format("2006-01-02")
		return &pg_query.Node{Node: &pg_query.Node_TypeCast{TypeCast: &pg_query.TypeCast{
			Arg: aConstString(s),
			TypeName: &pg_query.TypeName{
				Names:   []*pg_query.Node{stringNode("date")},
				Typemod: -1,
			},
		}}}
	}
	s := t.Format("2006-01-02 15:04:05")
	return &pg_query.Node{Node: &pg_query.Node_TypeCast{TypeCast: &pg_query.TypeCast{
		Arg: aConstString(s),
		TypeName: &pg_query.TypeName{
			Names:   []*pg_query.Node{stringNode("timestamp")},
			Typemod: -1,
		},
	}}}
}

func aExprOpIs(ae *pg_query.A_Expr, op string) bool {
	if ae == nil || len(ae.Name) != 1 {
		return false
	}
	s, ok := stringNodeValue(ae.Name[0])
	return ok && s == op
}

func cmpExpr(op string, left, right *pg_query.Node) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_AExpr{AExpr: &pg_query.A_Expr{
		Kind:  pg_query.A_Expr_Kind_AEXPR_OP,
		Name:  []*pg_query.Node{stringNode(op)},
		Lexpr: left,
		Rexpr: right,
	}}}
}

func andExpr(a, b *pg_query.Node) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: &pg_query.BoolExpr{
		Boolop: pg_query.BoolExprType_AND_EXPR,
		Args:   []*pg_query.Node{a, b},
	}}}
}

func stringNode(s string) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_String_{String_: &pg_query.String{Sval: s}}}
}

func aConstString(s string) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_AConst{AConst: &pg_query.A_Const{
		Val: &pg_query.A_Const_Sval{Sval: &pg_query.String{Sval: s}},
	}}}
}

func stringNodeValue(n *pg_query.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	if s := n.GetString_(); s != nil {
		return s.GetSval(), true
	}
	return "", false
}

func stringConstValue(n *pg_query.Node) (string, bool) {
	if n == nil {
		return "", false
	}
	if ac := n.GetAConst(); ac != nil {
		if sval := ac.GetSval(); sval != nil {
			return sval.GetSval(), true
		}
	}
	if tc := n.GetTypeCast(); tc != nil {
		return stringConstValue(tc.Arg)
	}
	return "", false
}

func uniqueUnits(rewrites []dateTruncRewrite) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range rewrites {
		u := normalizeTruncUnit(r.Unit)
		if _, ok := seen[u]; ok {
			continue
		}
		seen[u] = struct{}{}
		out = append(out, u)
	}
	return out
}

func quoteUnits(units []string) []string {
	out := make([]string, len(units))
	for i, u := range units {
		out[i] = "'" + u + "'"
	}
	return out
}
