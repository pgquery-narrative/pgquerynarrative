package queryrunner

import (
	"context"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
)

// ColumnTypeResolver resolves the real PostgreSQL type of schema.relation.column
// (e.g. "integer", "numeric", "text", "character varying" — the names
// pg_catalog's regtype output uses, not necessarily the spelling a cast used).
// schema may be empty when the caller could not determine it from the SQL
// text alone; the resolver is responsible for finding it from context (e.g.
// search_path), the same way EXPLAIN's own schema-less partition children are
// resolved (see Runner.resolveRelationSchema). ok is false whenever the
// column cannot be resolved for any reason — unknown relation/column,
// ambiguous schema, or no catalog access at all — and callers must treat that
// exactly like having no resolver: decline the rewrite.
type ColumnTypeResolver func(ctx context.Context, schema, relation, column string) (string, bool)

// rewriteCtx carries the per-call context a sargable rewrite needs beyond the
// AST node itself. resolveType is nil for the two catalog-free entry points
// (SuggestRewrites, SuggestRewritesForInvestigation); every cast-rewrite
// attempt bails out immediately when it is, which is what keeps the
// `::text`/`::numeric` non-rewrite tests green for those callers.
type rewriteCtx struct {
	ctx         context.Context
	resolveType ColumnTypeResolver
}

// tryRewriteNumericCastEquality and tryRewriteTextCastEquality drop a
// col::<type> = <typed literal> cast when, and only when, the catalog
// confirms the column's own real type already equals the cast's target type
// — the one case where the cast changes nothing about the comparison (see
// splitCastEquality and canonicalPgType). Every other col::numeric/col::text
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
// target is in wantTypeClass, resolve the column's real catalog type, and
// rewrite only when the two are the same type under PostgreSQL's own
// built-in aliases (int4/integer, float8/"double precision", ...).
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
	if !ok {
		return nil, dateTruncRewrite{}, false
	}
	colType, ok := rc.resolveType(rc.ctx, schema, relation, column)
	if !ok || canonicalPgType(colType) != canonicalPgType(castType) {
		return nil, dateTruncRewrite{}, false
	}
	col := cloneColumnRef(colNode)
	if col == nil || rhs == nil {
		return nil, dateTruncRewrite{}, false
	}
	return cmpExpr("=", col, rhs), dateTruncRewrite{Column: columnRefName(colNode), Kind: kind}, true
}

// splitCastEquality matches a::<type> = b where a is a bare-column cast whose
// target type satisfies wantTypeClass and b is any constant. b is returned
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
	castType = typeNameLast(tc.TypeName)
	if !wantTypeClass(castType) {
		return nil, nil, "", false
	}
	col = unwrapColumnRefNode(tc.Arg)
	if col == nil {
		return nil, nil, "", false
	}
	return col, b, castType, true
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

// canonicalPgType maps every spelling PostgreSQL uses for the same built-in
// type — a cast's own target name (typeNameLast's "int4", "float8", ...) and
// pg_catalog's display name for a real column (regtype's "integer", "double
// precision", ...) — onto one token, so the two can be compared for exact
// type equality regardless of which side used which spelling. Deliberately
// narrow: text and varchar are NOT folded together (nor is either with
// bpchar, whose blank-padding semantics can differ from a string compare),
// so only a cast whose target is the column's own real type — never merely
// a same-family type — is ever treated as a no-op. That is the one case
// this package proves safe; a general source->target coercion matrix is not
// in scope (see internal/queryrunner's cast-rewrite tests).
func canonicalPgType(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "int2", "smallint":
		return "smallint"
	case "int4", "int", "integer":
		return "integer"
	case "int8", "bigint":
		return "bigint"
	case "numeric", "decimal":
		return "numeric"
	case "float4", "real":
		return "real"
	case "float8", "double precision":
		return "double precision"
	case "varchar", "character varying":
		return "character varying"
	case "bpchar", "character":
		return "character"
	default:
		return strings.ToLower(strings.TrimSpace(name))
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
	if rv == nil || rv.Relname == "" {
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
	type cached struct {
		pgType string
		ok     bool
	}
	cache := map[string]cached{}
	return func(ctx context.Context, schema, relation, column string) (string, bool) {
		key := schema + "." + relation + "." + column
		if v, found := cache[key]; found {
			return v.pgType, v.ok
		}
		pgType, ok := resolveType(ctx, schema, relation, column)
		cache[key] = cached{pgType, ok}
		return pgType, ok
	}
}
