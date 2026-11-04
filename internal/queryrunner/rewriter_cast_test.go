package queryrunner

import (
	"context"
	"strings"
	"testing"
)

// fakeColumnTypeResolver returns a ColumnTypeResolver backed by a static
// column -> type map, keyed "schema.relation.column" (schema may be "" to
// match a resolver call that could not determine it from the SQL text and
// had to fall back — this fake never needs that fallback, since every test
// query below names its schema explicitly).
func fakeColumnTypeResolver(types map[string]string) ColumnTypeResolver {
	return func(_ context.Context, schema, relation, column string) (string, bool) {
		pgType, ok := types[schema+"."+relation+"."+column]
		return pgType, ok
	}
}

func suggestWithTypes(t *testing.T, sql string, types map[string]string) []RewriteCandidate {
	t.Helper()
	return SuggestRewritesForInvestigationWithTypes(context.Background(), sql, nil, false, fakeColumnTypeResolver(types))
}

// Bug (#200): the pre-catalog cast rewriter dropped col::text/col::numeric
// casts unconditionally, which could change the query's result (see #197).
// With a resolver that confirms the cast's target type already equals the
// column's own real type, dropping the cast is a provable no-op and the
// rewrite is offered; a resolver that reports any other type — or no
// resolver at all — must decline exactly like before.
func TestSuggestRewritesWithTypes_NumericCastDroppedWhenTypeMatches(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE amount::numeric = 5`
	cands := suggestWithTypes(t, sql, map[string]string{"demo.sales.amount": "numeric"})
	c := mustFindCategory(t, cands, "implicit_cast")
	got := normalizeSQL(c.SQL)
	if strings.Contains(got, "::") {
		t.Fatalf("cast must be dropped once the catalog confirms the type, got: %s", c.SQL)
	}
	if !strings.Contains(got, "amount = 5") {
		t.Fatalf("expected a direct column comparison, got: %s", c.SQL)
	}
}

func TestSuggestRewritesWithTypes_TextCastDroppedWhenTypeMatches(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE sku::text = '5'`
	cands := suggestWithTypes(t, sql, map[string]string{"demo.sales.sku": "text"})
	c := mustFindCategory(t, cands, "implicit_cast")
	got := normalizeSQL(c.SQL)
	if strings.Contains(got, "::") {
		t.Fatalf("cast must be dropped once the catalog confirms the type, got: %s", c.SQL)
	}
	if !strings.Contains(got, "sku = '5'") {
		t.Fatalf("expected the literal kept as a string, got: %s", c.SQL)
	}
}

// Built-in type aliases (int4/integer, float8/"double precision", ...) are
// the same type under two spellings; the resolver reports pg_catalog's own
// display name, which must still match a cast written with the alias.
func TestSuggestRewritesWithTypes_BuiltinAliasesMatch(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		colType string
	}{
		{"int cast, integer column", `SELECT 1 FROM demo.sales WHERE qty::int = 5`, "integer"},
		{"integer cast, int4-reported column", `SELECT 1 FROM demo.sales WHERE qty::integer = 5`, "integer"},
		{"bigint cast, bigint column", `SELECT 1 FROM demo.sales WHERE qty::bigint = 5`, "bigint"},
		{"double precision cast, real-named column", `SELECT 1 FROM demo.sales WHERE qty::double precision = 5`, "double precision"},
		{"decimal cast, numeric column", `SELECT 1 FROM demo.sales WHERE qty::decimal = 5`, "numeric"},
		{"varchar cast, character varying column", `SELECT 1 FROM demo.sales WHERE qty::varchar = '5'`, "character varying"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cands := suggestWithTypes(t, tc.sql, map[string]string{"demo.sales.qty": tc.colType})
			c := mustFindCategory(t, cands, "implicit_cast")
			if strings.Contains(c.SQL, "::") {
				t.Fatalf("alias-equivalent cast must be dropped, got: %s", c.SQL)
			}
		})
	}
}

// Every one of the original #197 danger cases must still decline, even with
// a resolver available, because the resolved type never equals the cast's
// target — exactly the condition that made dropping the cast unsafe.
func TestSuggestRewritesWithTypes_DeclinesWhenTypeMismatches(t *testing.T) {
	tests := []struct {
		name    string
		sql     string
		colType string
	}{
		{"amount numeric cast to integer", `SELECT 1 FROM demo.sales WHERE amount::integer = 1`, "numeric"},
		{"price numeric cast to text", `SELECT 1 FROM demo.sales WHERE price::text = '12'`, "numeric"},
		{"sku text cast compared as if numeric", `SELECT 1 FROM demo.sales WHERE sku::text = '5'`, "integer"},
		{"text cast to varchar (not folded together)", `SELECT 1 FROM demo.sales WHERE sku::varchar = '5'`, "text"},
		{"varchar cast to text (not folded together)", `SELECT 1 FROM demo.sales WHERE sku::text = '5'`, "character varying"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cands := suggestWithTypes(t, tc.sql, map[string]string{"demo.sales.amount": tc.colType, "demo.sales.price": tc.colType, "demo.sales.sku": tc.colType})
			for _, c := range cands {
				got := normalizeSQL(c.SQL)
				if !strings.Contains(got, "::") && !strings.Contains(got, "cast(") {
					t.Fatalf("mismatched cast must not be dropped.\n in: %s\nout: %s", tc.sql, c.SQL)
				}
			}
		})
	}
}

// An unresolvable column (unknown relation/column, or no catalog access at
// all) must decline exactly like the type-mismatch case — never assume safe
// just because a resolver was supplied.
func TestSuggestRewritesWithTypes_DeclinesWhenResolverCannotResolve(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE amount::numeric = 5`
	for _, cands := range [][]RewriteCandidate{
		suggestWithTypes(t, sql, map[string]string{}),                                        // empty map: unresolvable
		SuggestRewritesForInvestigationWithTypes(context.Background(), sql, nil, false, nil), // nil resolver
	} {
		for _, c := range cands {
			got := normalizeSQL(c.SQL)
			if !strings.Contains(got, "::") {
				t.Fatalf("an unresolvable column must not have its cast dropped, got: %s", c.SQL)
			}
		}
	}
}

// A JOIN (or any multi-table FROM) makes which table an unqualified column
// belongs to ambiguous without a full name-resolution pass; the cast rewrite
// declines entirely rather than guess.
func TestSuggestRewritesWithTypes_DeclinesAcrossJoins(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales s JOIN demo.customers c ON c.id = s.customer_id WHERE s.amount::numeric = 5`
	cands := suggestWithTypes(t, sql, map[string]string{"demo.sales.amount": "numeric"})
	for _, c := range cands {
		if !strings.Contains(c.SQL, "::") {
			t.Fatalf("a cast in a joined query must not be dropped, got: %s", c.SQL)
		}
	}
}

// A qualified column naming some table/alias other than the FROM clause's
// one relation must decline — the resolver must never be asked about the
// wrong table.
func TestSuggestRewritesWithTypes_DeclinesWrongQualifier(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales s WHERE other.amount::numeric = 5`
	cands := suggestWithTypes(t, sql, map[string]string{"demo.sales.amount": "numeric"})
	for _, c := range cands {
		if !strings.Contains(c.SQL, "::") {
			t.Fatalf("a mis-qualified column must not be resolved against the wrong table, got: %s", c.SQL)
		}
	}
}

// The alias a table is given is the only legal way to refer to it once
// aliased; the resolver still needs the table's real name, not the alias.
func TestSuggestRewritesWithTypes_ResolvesThroughAlias(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales s WHERE s.amount::numeric = 5`
	cands := suggestWithTypes(t, sql, map[string]string{"demo.sales.amount": "numeric"})
	mustFindCategory(t, cands, "implicit_cast")
}

// SuggestRewrites/SuggestRewritesForInvestigation (no resolver parameter at
// all) must behave exactly as before this feature existed — this is the
// explicit #200 acceptance criterion that the #197 non-rewrite tests stay
// green for every caller that never asked for catalog-aware casts.
func TestSuggestRewrites_PlainEntryPointsNeverDropCasts(t *testing.T) {
	sql := `SELECT 1 FROM demo.sales WHERE amount::numeric = 5`
	for _, cands := range [][]RewriteCandidate{
		SuggestRewrites(sql, nil),
		SuggestRewritesForInvestigation(sql, nil, false),
	} {
		for _, c := range cands {
			if !strings.Contains(c.SQL, "::") {
				t.Fatalf("plain entry points must never drop a cast, got: %s", c.SQL)
			}
		}
	}
}
