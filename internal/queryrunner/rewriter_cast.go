package queryrunner

import (
	"context"
	"strings"

	"github.com/jackc/pgx/v5"
	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// ColumnTypeResolver reports whether column of relation already has exactly
// the type castType, with both names resolved the way the database session
// that will run the rewritten query resolves them: relation is the FROM
// clause's (possibly schema-qualified) name and castType the cast's type
// name, each as a quoted SQL identifier chain exactly as written. Comparing
// resolved type OIDs, not spellings, is what makes "the cast is a no-op"
// provable: a same-named domain or a search_path-dependent relation can never
// pass for the built-in type. ok is false whenever either name cannot be
// resolved, or there is no catalog access; callers must then decline.
type ColumnTypeResolver func(ctx context.Context, relation, column, castType string) (same, ok bool)

// rewriteCtx carries the per-call context a sargable rewrite needs beyond the
// AST node itself. resolveType is nil for the two catalog-free entry points
// (SuggestRewrites, SuggestRewritesForInvestigation); every cast-rewrite
// attempt bails out immediately when it is, which is what keeps the
// `::text`/`::numeric` non-rewrite tests green for those callers.
type rewriteCtx struct {
	ctx         context.Context
	resolveType ColumnTypeResolver
	// cteNames holds every WITH name visible at this level. A schema-less
	// relation matching one is the CTE, not a catalog table, so its column
	// type is unknowable here. Copied on write (see withCTEs).
	cteNames map[string]bool
}

// withCTEs returns rc with ctes' names added to the visible set, leaving rc's
// own map untouched so sibling scopes never see each other's names.
func (rc rewriteCtx) withCTEs(ctes []*pg_query.Node) rewriteCtx {
	names := make(map[string]bool, len(rc.cteNames)+len(ctes))
	for n := range rc.cteNames {
		names[n] = true
	}
	for _, c := range ctes {
		if cte := c.GetCommonTableExpr(); cte != nil {
			names[cte.Ctename] = true
		}
	}
	rc.cteNames = names
	return rc
}

// tryRewriteNumericCastEquality and tryRewriteTextCastEquality drop a
// col::<type> = <typed literal> cast when, and only when, the catalog
// confirms the column's own real type already equals the cast's target type
// — the one case where the cast changes nothing about the comparison (see
// splitCastEquality and ColumnTypeResolver). Every other col::numeric/col::text
// shape — including every one a pre-catalog version of this rewriter used to
// drop unconditionally — is left untouched, because dropping the cast is
// only safe when it is a provable no-op:
//
//	amount numeric := 1.4 ;  amount::integer = 1  is TRUE
//	                         amount          = 1  is FALSE
//	price  numeric := 12.0;  price::text = '12'   is FALSE ('12.0')
//	                         price       = 12      is TRUE
//	sku    text            ;  sku::text = '5'  ->  sku = 5 does not even
//	                         parse (no text = integer operator)
func tryRewriteNumericCastEquality(rc rewriteCtx, sel *pg_query.SelectStmt, ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	return tryRewriteCastEquality(rc, sel, ae, isNumericTypeName, "numeric_cast")
}

func tryRewriteTextCastEquality(rc rewriteCtx, sel *pg_query.SelectStmt, ae *pg_query.A_Expr) (*pg_query.Node, dateTruncRewrite, bool) {
	return tryRewriteCastEquality(rc, sel, ae, isTextTypeName, "text_cast")
}

// tryRewriteCastEquality is the shared body of the numeric/text cast
// rewrites: find a col::<type> = <literal> (or reversed) shape whose cast
// target is in wantTypeClass, and rewrite only when the catalog proves the
// column's type is exactly the cast's target type.
func tryRewriteCastEquality(rc rewriteCtx, sel *pg_query.SelectStmt, ae *pg_query.A_Expr, wantTypeClass func(string) bool, kind string) (*pg_query.Node, dateTruncRewrite, bool) {
	if rc.resolveType == nil || ae == nil || ae.Kind != pg_query.A_Expr_Kind_AEXPR_OP || !aExprOpIs(ae, "=") {
		return nil, dateTruncRewrite{}, false
	}
	colNode, rhs, castType, ok := splitCastEquality(ae.Lexpr, ae.Rexpr, wantTypeClass)
	if !ok {
		colNode, rhs, castType, ok = splitCastEquality(ae.Rexpr, ae.Lexpr, wantTypeClass)
	}
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	schema, relation, column, ok := resolveCastColumnRelation(sel, colNode)
	if !ok || (schema == "" && rc.cteNames[relation]) {
		return nil, dateTruncRewrite{}, false
	}
	relName := pgx.Identifier{relation}
	if schema != "" {
		relName = pgx.Identifier{schema, relation}
	}
	same, ok := rc.resolveType(rc.ctx, relName.Sanitize(), column, castType)
	if !ok || !same {
		return nil, dateTruncRewrite{}, false
	}
	col := cloneColumnRef(colNode)
	if col == nil || rhs == nil {
		return nil, dateTruncRewrite{}, false
	}
	return cmpExpr("=", col, rhs), dateTruncRewrite{Column: columnRefName(colNode), Kind: kind}, true
}

// splitCastEquality matches a::<type> = b where a is a cast applied directly
// to a bare column (never a nested cast: (amount::integer)::numeric rounds
// first) whose target type satisfies wantTypeClass and b is any constant.
// castType is the cast's full type name as a quoted identifier chain, so a
// schema-qualified name (a same-named domain) is resolved as written. b is returned
// unmodified — dropping the cast changes nothing about what b must coerce to,
// since the catalog check above only fires when the column's real type is
// already the cast's target type.
func splitCastEquality(a, b *pg_query.Node, wantTypeClass func(string) bool) (col, rhs *pg_query.Node, castType string, ok bool) {
	if a == nil || b == nil || !isConstNode(b) {
		return nil, nil, "", false
	}
	tc := a.GetTypeCast()
	if tc == nil {
		return nil, nil, "", false
	}
	// A typmod (numeric(10,1) rounds, varchar(2) truncates, bare char is
	// char(1)) or array bound makes the cast a real conversion even when the
	// base type matches the column's.
	tn := tc.TypeName
	if tn == nil || len(tn.Typmods) > 0 || len(tn.ArrayBounds) > 0 || tn.Setof || tn.PctType {
		return nil, nil, "", false
	}
	if !wantTypeClass(typeNameLast(tn)) || tc.Arg.GetColumnRef() == nil {
		return nil, nil, "", false
	}
	names := make(pgx.Identifier, 0, len(tn.Names))
	for _, n := range tn.Names {
		v, ok := stringNodeValue(n)
		if !ok {
			return nil, nil, "", false
		}
		names = append(names, v)
	}
	return tc.Arg, b, names.Sanitize(), true
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

// singleFromRelation returns the schema and relation name of sel's FROM
// clause when it is exactly one plain table reference (no JOIN, no
// subquery), plus the name a qualified column in this SELECT would use to
// refer to it (its alias, or its table name when unaliased). ok is false for
// anything else: a JOIN or multi-table FROM makes which table an unqualified
// or ambiguously-qualified column belongs to something this package is not
// going to guess at just to drop a cast.
func singleFromRelation(sel *pg_query.SelectStmt) (schema, relation, refName string, ok bool) {
	if sel == nil || len(sel.FromClause) != 1 {
		return "", "", "", false
	}
	rv := sel.FromClause[0].GetRangeVar()
	// A column alias list (FROM t AS x(a, b)) renames the table's columns, so
	// a column name no longer identifies the catalog attribute; a
	// catalog-qualified name is not resolved here either.
	if rv == nil || rv.Relname == "" || rv.Catalogname != "" || (rv.Alias != nil && len(rv.Alias.Colnames) > 0) {
		return "", "", "", false
	}
	refName = rv.Relname
	if rv.Alias != nil && rv.Alias.Aliasname != "" {
		refName = rv.Alias.Aliasname
	}
	return rv.Schemaname, rv.Relname, refName, true
}

// columnRefParts splits a ColumnRef into its qualifier (table/alias name, or
// "" when the column is unqualified) and its own name. ok is false for
// anything pg_query didn't resolve to plain name parts, or a
// schema.table.column reference (three parts) — resolveCastColumnRelation
// only needs to handle the single-table FROM clauses singleFromRelation
// already allows, where a column is never qualified by schema.
func columnRefParts(n *pg_query.Node) (qualifier, column string, ok bool) {
	cr := n.GetColumnRef()
	if cr == nil || len(cr.Fields) == 0 {
		return "", "", false
	}
	parts := make([]string, 0, len(cr.Fields))
	for _, f := range cr.Fields {
		s, ok := stringNodeValue(f)
		if !ok {
			return "", "", false
		}
		parts = append(parts, s)
	}
	switch len(parts) {
	case 1:
		return "", parts[0], true
	case 2:
		return parts[0], parts[1], true
	default:
		return "", "", false
	}
}

// resolveCastColumnRelation returns the schema/relation/column a cast's
// column node refers to, using sel's own single-table FROM clause (see
// singleFromRelation) — declining whenever that relation can't be determined
// unambiguously, or a qualified column names some other table/alias.
func resolveCastColumnRelation(sel *pg_query.SelectStmt, colNode *pg_query.Node) (schema, relation, column string, ok bool) {
	schema, relation, refName, ok := singleFromRelation(sel)
	if !ok {
		return "", "", "", false
	}
	qualifier, col, ok := columnRefParts(colNode)
	if !ok || col == "" {
		return "", "", "", false
	}
	if qualifier != "" && !strings.EqualFold(qualifier, refName) {
		return "", "", "", false
	}
	return schema, relation, col, true
}

// cachedColumnTypeResolver memoizes resolveType for the lifetime of one
// SuggestRewrites call: a query with several predicates on the same column
// would otherwise repeat an identical catalog round trip once per predicate.
// Not safe for concurrent use — each call site builds its own.
func cachedColumnTypeResolver(resolveType ColumnTypeResolver) ColumnTypeResolver {
	if resolveType == nil {
		return nil
	}
	type cached struct{ same, ok bool }
	cache := map[[3]string]cached{}
	return func(ctx context.Context, relation, column, castType string) (bool, bool) {
		key := [3]string{relation, column, castType}
		if v, found := cache[key]; found {
			return v.same, v.ok
		}
		same, ok := resolveType(ctx, relation, column, castType)
		cache[key] = cached{same, ok}
		return same, ok
	}
}
