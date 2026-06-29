package llm

import "testing"

func TestRedactRows_sensitiveColumns(t *testing.T) {
	cols := []string{"id", "user_email", "amount"}
	rows := [][]interface{}{
		{1, "alice@example.com", 99.5},
	}
	out := RedactRows(cols, rows)
	if out[0][0] != 1 {
		t.Fatalf("id should not be redacted, got %v", out[0][0])
	}
	if out[0][1] != "[REDACTED]" {
		t.Fatalf("email column should be redacted, got %v", out[0][1])
	}
	if out[0][2] != 99.5 {
		t.Fatalf("amount should not be redacted, got %v", out[0][2])
	}
}

func TestRedactRows_patternValues(t *testing.T) {
	cols := []string{"note"}
	rows := [][]interface{}{
		{"contact me at bob@corp.com or 555-123-4567"},
	}
	out := RedactRows(cols, rows)
	s := out[0][0].(string)
	if s == rows[0][0] {
		t.Fatal("expected PII patterns to be redacted in free-text column")
	}
}
