package queryrunner

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"google.golang.org/protobuf/proto"
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
// candidate rewrites. Supported patterns:
//
//   - DATE_TRUNC / EXTRACT / date_part / to_char / col::date equality → sargable range
//   - DATE_TRUNC / col::date inequalities and BETWEEN → sargable range
//   - COALESCE(col, default) = const → sargable column predicate
//   - col::text / col::numeric = typed literal → compare the column to a typed literal
//   - OR of predicates on different columns → UNION ALL of indexable branches
//   - col IN/NOT IN (SELECT ...) → EXISTS / NULL-safe NOT EXISTS
//   - LEFT JOIN b ON b.k = a.id WHERE b.k IS NULL → NOT EXISTS anti-join
//
// Findings are optional evidence: function-wrap / partition-pruning language
// raises confidence to "high". The engine still works from pasted SQL alone.
// Nested CTEs / FROM subqueries are rewritten for sargable unwraps.
//
// Parameterized SQL ($1, $2, ...) — the shape the poller reads from
// pg_stat_statements — is rewritten only for the sargable `=` / `BETWEEN` shapes
// where the transform is unambiguous (DATE_TRUNC and col::date). The bind value
// is preserved as a placeholder; equivalence/compare still need sample binds.
// OR→UNION ALL and IN/NOT IN→EXISTS stay literals-only.
func SuggestRewrites(sql string, findings []PlanFinding) []RewriteCandidate {
	trimmed := trimSQL(sql)
	if trimmed == "" {
		return nil
	}
	hasParam := false
	if _, sel, ok := parseSingleSelect(trimmed); ok {
		hasParam = selectTreeContainsParamRef(sel)
	}
	var out []RewriteCandidate
	if c := suggestSargableRewrites(trimmed, findings); c != nil {
		out = append(out, *c)
	}
	if !hasParam {
		if c := suggestOrToUnion(trimmed, findings); c != nil {
			out = append(out, *c)
		}
		if c := suggestInToExists(trimmed, findings); c != nil {
			out = append(out, *c)
		}
		if c := suggestLeftJoinAntiJoinToNotExists(trimmed, findings); c != nil {
			out = append(out, *c)
		}
	}
	return uniqueRewriteSQL(out)
}

func trimSQL(sql string) string {
	trimmed := strings.TrimSpace(sql)
	trimmed = strings.TrimSuffix(trimmed, ";")
	return strings.TrimSpace(trimmed)
}

func uniqueRewriteSQL(cands []RewriteCandidate) []RewriteCandidate {
	seen := map[string]struct{}{}
	var out []RewriteCandidate
	for _, c := range cands {
		key := strings.ToLower(strings.Join(strings.Fields(c.SQL), " "))
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

func parseSingleSelect(sql string) (*pg_query.ParseResult, *pg_query.SelectStmt, bool) {
	trimmed := trimSQL(sql)
	if trimmed == "" {
		return nil, nil, false
	}
	result, err := pg_query.Parse(trimmed)
	if err != nil || len(result.Stmts) != 1 {
		return nil, nil, false
	}
	stmt := result.Stmts[0].GetStmt()
	if stmt == nil || stmt.GetSelectStmt() == nil {
		return nil, nil, false
	}
	return result, stmt.GetSelectStmt(), true
}

func suggestSargableRewrites(sql string, findings []PlanFinding) *RewriteCandidate {
	result, sel, ok := parseSingleSelect(sql)
	if !ok {
		return nil
	}
	var replacements []dateTruncRewrite
	n := applySargableRewritesToSelectTree(sel, &replacements)
	if n == 0 {
		return nil
	}

	outSQL, err := pg_query.Deparse(result)
	if err != nil {
		return nil
	}
	outSQL = strings.TrimSpace(outSQL)
	if outSQL == "" || strings.EqualFold(outSQL, sql) {
		return nil
	}

	kinds := uniqueKinds(replacements)
	confidence := "medium"
	if findingsSuggestSargableRewrite(findings) {
		confidence = "high"
	}
	return &RewriteCandidate{
		SQL:        outSQL,
		Rationale:  rewriteRationale(kinds, uniqueUnits(replacements)),
		Category:   rewriteCategory(kinds),
		Confidence: confidence,
	}
}

type dateTruncRewrite struct {
	Unit   string
	Column string
	Kind   string // date_trunc | cast_date
}

func rewriteRationale(kinds, units []string) string {
	has := func(want string) bool {
		for _, k := range kinds {
			if k == want {
				return true
			}
		}
		return false
	}
	hasTrunc := has("date_trunc")
	hasCast := has("cast_date")
	var parts []string

	if has("date_trunc_param") || has("cast_date_param") {
		what := "DATE_TRUNC(...)"
		if has("cast_date_param") && !has("date_trunc_param") {
			what = "column::date"
		}
		parts = append(parts, fmt.Sprintf(
			"unwrap the parameterized %s = $n / BETWEEN into a sargable range on the column (placeholders preserved) so PostgreSQL can prune partitions and use indexes; supply sample bind values to run compare/equivalence",
			what,
		))
	}
	switch {
	case hasTrunc && hasCast:
		parts = append(parts, fmt.Sprintf(
			"unwrap DATE_TRUNC(%s) and column::date equality to sargable range predicates so PostgreSQL can prune partitions and use indexes",
			strings.Join(quoteUnits(units), "/"),
		))
	case hasTrunc:
		parts = append(parts, fmt.Sprintf(
			"unwrap DATE_TRUNC(%s) equality to a sargable range predicate so PostgreSQL can prune partitions and use indexes on the column",
			strings.Join(quoteUnits(units), "/"),
		))
	case hasCast:
		parts = append(parts, "unwrap column::date / CAST(col AS date) equality to a sargable day-range predicate so PostgreSQL can prune partitions and use indexes on the column")
	}
	if has("extract") {
		parts = append(parts, "unwrap EXTRACT/date_part equality to a sargable range predicate so PostgreSQL can prune partitions and use indexes on the column")
	}
	if has("to_char") {
		parts = append(parts, "unwrap to_char(...) equality to a sargable range predicate so PostgreSQL can prune partitions and use indexes on the column")
	}
	if has("coalesce") {
		parts = append(parts, "unwrap COALESCE(col, default) equality so the underlying column is sargable")
	}
	if has("text_cast") {
		parts = append(parts, "move the text cast off the column onto a typed literal so an index on the column can be used")
	}
	if has("numeric_cast") {
		parts = append(parts, "move the numeric cast off the column onto a typed literal so an index on the column can be used")
	}
	if len(parts) == 0 {
		return "unwrap non-sargable predicates to index- and partition-friendly forms"
	}
	return strings.Join(parts, "; ")
}

func rewriteCategory(kinds []string) string {
	if len(kinds) == 1 {
		switch kinds[0] {
		case "coalesce":
			return "coalesce_unwrap"
		case "text_cast", "numeric_cast":
			return "implicit_cast"
		}
	}
	return "function_wrap"
}

func uniqueKinds(rs []dateTruncRewrite) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, r := range rs {
		k := r.Kind
		if k == "" {
			k = "date_trunc"
		}
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		out = append(out, k)
	}
	return out
}

func findingsSuggestSargableRewrite(findings []PlanFinding) bool {
	for _, f := range findings {
		blob := strings.ToLower(f.Category + " " + f.Message + " " + strings.Join(f.Evidence, " "))
		if strings.Contains(blob, "date_trunc") ||
			strings.Contains(blob, "function-wrapped") ||
			strings.Contains(blob, "function wrap") ||
			strings.Contains(blob, "extract(") ||
			strings.Contains(blob, "date_part") ||
			strings.Contains(blob, "to_char") ||
			strings.Contains(blob, "coalesce") ||
			strings.Contains(blob, "::date") ||
			strings.Contains(blob, "::text") ||
			strings.Contains(blob, "cast(") ||
			f.Category == CategoryPartitionPruning {
			return true
		}
	}
	return false
}

func rewriteFunctionWrapInExpr(node *pg_query.Node, out *[]dateTruncRewrite) (*pg_query.Node, int) {
	if node == nil {
		return nil, 0
	}
	if ae := node.GetAExpr(); ae != nil {
		if replacement, info, ok := tryRewriteDateTruncBetween(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteCastDateBetween(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteDateTruncInequality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteCastDateInequality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteDateTruncEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteCastDateEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteExtractYearEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteToCharEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteCoalesceEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteNumericCastEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteTextCastEquality(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		// Parameterized shapes ($1, $2, ...) — equality only.
		// DATE_TRUNC(unit, col) BETWEEN $a AND $b is intentionally not rewritten
		// (see tryRewriteDateTruncBetweenParam): a misaligned bind bound shifts
		// the range rather than emptying it, and alignment is unknowable here.
		if replacement, info, ok := tryRewriteDateTruncEqualityParam(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteCastDateEqualityParam(ae); ok {
			*out = append(*out, info)
			return replacement, 1
		}
		if replacement, info, ok := tryRewriteCastDateBetweenParam(ae); ok {
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
			rewritten, n := rewriteFunctionWrapInExpr(arg, out)
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

	rawConst, typ, ok := parseTemporalConst(constNode)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	// A literal with an explicit non-UTC zone offset is compared as an instant,
	// while DATE_TRUNC of a timestamptz column uses the session TimeZone — the
	// range bounds we emit (zoneless `::timestamp`) would not line up. Leave it.
	if constHasExplicitZoneOffset(constNode) {
		return nil, dateTruncRewrite{}, false
	}
	start := truncateTime(rawConst, unit)
	// DATE_TRUNC(unit, col) only ever yields a value on the unit boundary, so
	// `DATE_TRUNC(unit, col) = <const>` with a <const> that is not itself on that
	// boundary matches no rows at all. Rewriting it to the surrounding [start, end)
	// range would turn an always-empty predicate into a whole month/day of matches,
	// so bail and leave the original predicate untouched. Alignment-safe constants
	// ('2025-01-01' for 'month', etc.) still rewrite below.
	if !start.Equal(rawConst) {
		return nil, dateTruncRewrite{}, false
	}
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
	info := dateTruncRewrite{Unit: unit, Column: columnRefName(colNode), Kind: "date_trunc"}
	return rangePred, info, true
}

// tryRewriteCastDateEquality rewrites col::date = <const> / CAST(col AS date) = <const>
// into a closed day range on the underlying column.
func tryRewriteCastDateEquality(ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	if ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	colNode, constNode, ok := splitCastDateEquality(ae.Lexpr, ae.Rexpr)
	if !ok {
		colNode, constNode, ok = splitCastDateEquality(ae.Rexpr, ae.Lexpr)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	rawConst, typ, ok := parseTemporalConst(constNode)
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	if constHasExplicitZoneOffset(constNode) {
		return nil, dateTruncRewrite{}, false
	}
	unit := "day"
	start := truncateTime(rawConst, unit)
	// `col::date = <const>` with a <const> that carries a time component is a
	// timestamp compared against col::date promoted to midnight, so it matches
	// no rows. Only a day-aligned constant maps to a real [day, day+1) range;
	// rewriting a misaligned one would invent a day of matches.
	if !start.Equal(rawConst) {
		return nil, dateTruncRewrite{}, false
	}
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
	info := dateTruncRewrite{Unit: unit, Column: columnRefName(colNode), Kind: "cast_date"}
	return rangePred, info, true
}

func splitCastDateEquality(a, b *pg_query.Node) (col, constNode *pg_query.Node, ok bool) {
	if a == nil || b == nil {
		return nil, nil, false
	}
	tc := a.GetTypeCast()
	if tc == nil || !strings.EqualFold(typeNameLast(tc.TypeName), "date") {
		return nil, nil, false
	}
	col = columnRefUnder(tc.Arg)
	if col == nil {
		return nil, nil, false
	}
	if !isTemporalConst(b) {
		return nil, nil, false
	}
	return col, b, true
}

func columnRefUnder(n *pg_query.Node) *pg_query.Node {
	if n == nil {
		return nil
	}
	if n.GetColumnRef() != nil {
		return n
	}
	// Reject nested casts that are not a bare column (e.g. (expr)::date).
	if tc := n.GetTypeCast(); tc != nil {
		return columnRefUnder(tc.Arg)
	}
	return nil
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

// zoneOffsetRe matches a trailing explicit numeric UTC offset that follows a
// time component (e.g. `12:30:00+02`, `08:00 -05:30`). A bare `Z` (UTC), a
// zoneless timestamp, and a bare date (whose own `-DD` is not an offset) are
// not flagged.
var zoneOffsetRe = regexp.MustCompile(`\d{2}:\d{2}(:\d{2})?(\.\d+)?\s*[+-]\d{2}(:?\d{2})?\s*$`)

// constHasExplicitZoneOffset reports whether n's string value carries an
// explicit numeric timezone offset. Such a literal is an instant, so the
// zoneless range bounds the date rewrites emit would not line up with a
// timestamptz column truncated in the session TimeZone.
func constHasExplicitZoneOffset(n *pg_query.Node) bool {
	if n == nil {
		return false
	}
	if tc := n.GetTypeCast(); tc != nil {
		n = tc.Arg
	}
	s, ok := stringConstValue(n)
	if !ok {
		return false
	}
	return zoneOffsetRe.MatchString(strings.TrimSpace(s))
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

func cloneSelectStmt(sel *pg_query.SelectStmt) *pg_query.SelectStmt {
	if sel == nil {
		return nil
	}
	cloned, ok := proto.Clone(sel).(*pg_query.SelectStmt)
	if !ok {
		return nil
	}
	return cloned
}

func intConstValue(n *pg_query.Node) (int64, bool) {
	if n == nil {
		return 0, false
	}
	if tc := n.GetTypeCast(); tc != nil {
		return intConstValue(tc.Arg)
	}
	ac := n.GetAConst()
	if ac == nil {
		return 0, false
	}
	if ival := ac.GetIval(); ival != nil {
		return int64(ival.GetIval()), true
	}
	if sval := ac.GetSval(); sval != nil {
		v, err := strconv.ParseInt(strings.TrimSpace(sval.GetSval()), 10, 64)
		if err != nil {
			return 0, false
		}
		return v, true
	}
	return 0, false
}

func aConstInt(v int32) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_AConst{AConst: &pg_query.A_Const{
		Val: &pg_query.A_Const_Ival{Ival: &pg_query.Integer{Ival: v}},
	}}}
}

func isConstNode(n *pg_query.Node) bool {
	if n == nil {
		return false
	}
	if n.GetAConst() != nil {
		return true
	}
	if tc := n.GetTypeCast(); tc != nil {
		return isConstNode(tc.Arg)
	}
	return false
}

func constEqual(a, b *pg_query.Node) bool {
	if sa, ok := stringConstValue(a); ok {
		if sb, ok := stringConstValue(b); ok {
			return sa == sb
		}
	}
	if ia, ok := intConstValue(a); ok {
		if ib, ok := intConstValue(b); ok {
			return ia == ib
		}
	}
	return false
}

func notExpr(n *pg_query.Node) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: &pg_query.BoolExpr{
		Boolop: pg_query.BoolExprType_NOT_EXPR,
		Args:   []*pg_query.Node{n},
	}}}
}

// isNotTrueExpr wraps n in `(n) IS NOT TRUE` — a NULL-safe negation. Unlike
// `NOT n`, this evaluates to TRUE (not NULL) when n itself is NULL, so it can
// subtract an already-covered predicate branch without dropping rows whose
// predicate column is NULL. Used by the OR -> UNION ALL rewrite so branch
// multiplicity matches the original OR under three-valued logic.
func isNotTrueExpr(n *pg_query.Node) *pg_query.Node {
	return &pg_query.Node{Node: &pg_query.Node_BooleanTest{BooleanTest: &pg_query.BooleanTest{
		Arg:          n,
		Booltesttype: pg_query.BoolTestType_IS_NOT_TRUE,
	}}}
}

func nullTest(arg *pg_query.Node, isNull bool) *pg_query.Node {
	t := pg_query.NullTestType_IS_NULL
	if !isNull {
		t = pg_query.NullTestType_IS_NOT_NULL
	}
	return &pg_query.Node{Node: &pg_query.Node_NullTest{NullTest: &pg_query.NullTest{
		Arg:          arg,
		Nulltesttype: t,
	}}}
}

func orExpr(args ...*pg_query.Node) *pg_query.Node {
	filtered := make([]*pg_query.Node, 0, len(args))
	for _, a := range args {
		if a != nil {
			filtered = append(filtered, a)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: &pg_query.BoolExpr{
		Boolop: pg_query.BoolExprType_OR_EXPR,
		Args:   filtered,
	}}}
}

func combineAnd(args []*pg_query.Node) *pg_query.Node {
	filtered := make([]*pg_query.Node, 0, len(args))
	for _, a := range args {
		if a != nil {
			filtered = append(filtered, a)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	if len(filtered) == 1 {
		return filtered[0]
	}
	return &pg_query.Node{Node: &pg_query.Node_BoolExpr{BoolExpr: &pg_query.BoolExpr{
		Boolop: pg_query.BoolExprType_AND_EXPR,
		Args:   filtered,
	}}}
}

func singleFromAlias(sel *pg_query.SelectStmt) (string, bool) {
	if sel == nil || len(sel.FromClause) != 1 {
		return "", false
	}
	rv := sel.FromClause[0].GetRangeVar()
	if rv == nil || rv.Relname == "" {
		return "", false
	}
	if rv.Alias != nil && rv.Alias.Aliasname != "" {
		return rv.Alias.Aliasname, true
	}
	return rv.Relname, true
}

func qualifyColumnRef(n *pg_query.Node, alias string) *pg_query.Node {
	if n == nil || alias == "" {
		return n
	}
	cr := n.GetColumnRef()
	if cr == nil {
		return n
	}
	if len(cr.Fields) >= 2 {
		return cloneColumnRef(n)
	}
	name := columnRefName(n)
	if name == "" {
		return n
	}
	return &pg_query.Node{Node: &pg_query.Node_ColumnRef{ColumnRef: &pg_query.ColumnRef{
		Fields: []*pg_query.Node{stringNode(alias), stringNode(name)},
	}}}
}

func resTargetVal(n *pg_query.Node) *pg_query.Node {
	if n == nil {
		return nil
	}
	rt := n.GetResTarget()
	if rt == nil {
		return nil
	}
	return rt.Val
}
