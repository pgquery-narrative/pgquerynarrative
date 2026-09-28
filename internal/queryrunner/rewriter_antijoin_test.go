package queryrunner

import (
	"strings"
	"testing"
)

func TestSuggestRewrites_LeftJoinAntiJoin(t *testing.T) {
	sql := `SELECT c.id, c.name FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE o.customer_id IS NULL`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "left_join_antijoin_to_not_exists")
	got := normalizeSQL(c.SQL)
	if strings.Contains(strings.ToLower(got), "left join") {
		t.Fatalf("LEFT JOIN should be gone: %s", c.SQL)
	}
	if !strings.Contains(strings.ToLower(got), "not exists (select 1 from orders o where o.customer_id = c.id)") {
		t.Fatalf("expected NOT EXISTS anti-join, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_LeftJoinAntiJoin_KeepsOtherPredicates(t *testing.T) {
	sql := `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id AND o.status = 'paid' WHERE o.customer_id IS NULL AND c.region = 'EU'`
	c := mustFindCategory(t, SuggestRewrites(sql, nil), "left_join_antijoin_to_not_exists")
	got := normalizeSQL(c.SQL)
	if !strings.Contains(got, "c.region = 'eu'") {
		t.Fatalf("outer predicate dropped: %s", c.SQL)
	}
	if !strings.Contains(got, "o.status = 'paid'") {
		t.Fatalf("ON-clause predicate dropped from subquery: %s", c.SQL)
	}
	if !strings.Contains(strings.ToLower(got), "not exists") {
		t.Fatalf("expected NOT EXISTS, got: %s", c.SQL)
	}
}

func TestSuggestRewrites_LeftJoinAntiJoin_FindingsRaiseConfidence(t *testing.T) {
	sql := `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE o.customer_id IS NULL`
	findings := []PlanFinding{{Category: CategorySeqScan, Message: "Seq Scan on orders"}}
	c := mustFindCategory(t, SuggestRewrites(sql, findings), "left_join_antijoin_to_not_exists")
	if c.Confidence != "high" {
		t.Fatalf("expected high confidence with a seq-scan finding, got %q", c.Confidence)
	}
}

func TestSuggestRewrites_LeftJoinAntiJoin_OutOfScope(t *testing.T) {
	cases := map[string]string{
		"is null on a non-key column": `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE o.shipped_at IS NULL`,
		"right column used in select": `SELECT c.id, o.total FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE o.customer_id IS NULL`,
		"unqualified column survives": `SELECT id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE o.customer_id IS NULL`,
		"right column in order by":    `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE o.customer_id IS NULL ORDER BY o.created_at`,
		"or in the on clause":         `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id OR o.alt_id = c.id WHERE o.customer_id IS NULL`,
		"inner join, not left":        `SELECT c.id FROM customers c JOIN orders o ON o.customer_id = c.id WHERE o.customer_id IS NULL`,
		"no is null test":             `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE c.region = 'EU'`,
		"subquery in the on clause":   `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id AND o.total > (SELECT avg(total) FROM orders) WHERE o.customer_id IS NULL`,
		"three-table join":            `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id LEFT JOIN refunds r ON r.order_id = o.id WHERE o.customer_id IS NULL`,
	}
	for name, sql := range cases {
		t.Run(name, func(t *testing.T) {
			for _, c := range SuggestRewrites(sql, nil) {
				if c.Category == "left_join_antijoin_to_not_exists" {
					t.Fatalf("expected no anti-join rewrite, got: %s", c.SQL)
				}
			}
		})
	}
}

func TestSuggestRewrites_LeftJoinAntiJoin_ParamQuerySkipped(t *testing.T) {
	sql := `SELECT c.id FROM customers c LEFT JOIN orders o ON o.customer_id = c.id WHERE o.customer_id IS NULL AND c.region = $1`
	for _, c := range SuggestRewrites(sql, nil) {
		if c.Category == "left_join_antijoin_to_not_exists" {
			t.Fatalf("param queries stay literals-only for this rewrite, got: %s", c.SQL)
		}
	}
}
