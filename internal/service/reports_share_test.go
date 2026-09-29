package service

import (
	"strings"
	"testing"
)

func TestHashShareTokenDeterministic(t *testing.T) {
	a := hashShareToken("abc")
	b := hashShareToken("abc")
	if a == "" || a != b {
		t.Fatalf("expected stable hash, got %q vs %q", a, b)
	}
	if a == hashShareToken("abd") {
		t.Fatal("different tokens must not collide")
	}
	if len(a) != 64 {
		t.Fatalf("expected sha256 hex length 64, got %d", len(a))
	}
}

func TestRedactExplainPlanJSON(t *testing.T) {
	plan := map[string]interface{}{
		"Node Type": "Seq Scan",
		"Filter":    "(email = 'secret@example.com')",
		"Plans": []interface{}{
			map[string]interface{}{"Index Cond": "(id = 42)"},
		},
	}
	raw := redactExplainPlanJSON(plan)
	if raw == nil {
		t.Fatal("expected redacted plan JSON")
	}
	s := string(raw)
	if strings.Contains(s, "secret@example.com") {
		t.Fatalf("expected email literal redacted, got %s", s)
	}
}
