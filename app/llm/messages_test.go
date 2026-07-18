package llm

import "testing"

func TestFlattenAndRestoreMessages(t *testing.T) {
	msgs := []ChatMessage{
		{Role: "system", Content: "be careful"},
		{Role: "user", Content: wrapUntrusted("SQL_QUERY", "SELECT 1")},
	}
	flat := FlattenMessages(msgs)
	got := PromptToChatMessages(flat)
	if len(got) != 2 {
		t.Fatalf("expected 2 messages, got %d (%q)", len(got), flat)
	}
	if got[0].Role != "system" || got[0].Content != "be careful" {
		t.Fatalf("system message mismatch: %+v", got[0])
	}
	if got[1].Role != "user" || got[1].Content == "" {
		t.Fatalf("user message mismatch: %+v", got[1])
	}
}

func TestBuildNarrativeMessagesWrapsUntrusted(t *testing.T) {
	msgs := BuildNarrativeMessages("SELECT 1", []string{"id"}, nil, `{"n":1}`, false, "", DefaultPromptOptions())
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[1].Role != "user" {
		t.Fatalf("unexpected messages: %+v", msgs)
	}
	if !containsAll(msgs[1].Content, "UNTRUSTED_DATA_BEGIN:SQL_QUERY", "UNTRUSTED_DATA_BEGIN:COLUMN_NAMES", "UNTRUSTED_DATA_BEGIN:METRICS_JSON") {
		t.Fatalf("user turn missing untrusted wrappers: %s", msgs[1].Content)
	}
}

func containsAll(s string, parts ...string) bool {
	for _, p := range parts {
		if stringIndex(s, p) < 0 {
			return false
		}
	}
	return true
}

func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
