package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestOpenAI_UsesBaseURLOverride is a regression test: NewOpenAIClient used to
// ignore its baseURL entirely and always call the real OpenAI host. It must
// send requests to a configured override instead.
func TestOpenAI_UsesBaseURLOverride(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`))
	}))
	defer srv.Close()

	c := NewOpenAIClient("key", "gpt-4o-mini", srv.URL)
	text, err := c.Generate(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if text != "ok" {
		t.Fatalf("got text %q, want %q", text, "ok")
	}
	if gotPath != "/chat/completions" {
		t.Fatalf("request did not reach the mock server (got path %q); baseURL override was not honored", gotPath)
	}
}

// TestOpenAI_RetriesOn5xx is a regression test: only HTTP 429 was retried;
// a transient 503 failed immediately with no retry, unlike Ollama's provider.
func TestOpenAI_RetriesOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"temporary"}`))
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"content":"ok after retry"}}]}`))
	}))
	defer srv.Close()

	if testing.Short() {
		t.Skip("waits out the real openaiRetryDelay (6s); skipped with -short")
	}

	c := NewOpenAIClient("key", "gpt-4o-mini", srv.URL)
	text, err := c.Generate(context.Background(), "hi")
	if err != nil {
		t.Fatalf("unexpected error after retry: %v", err)
	}
	if text != "ok after retry" {
		t.Fatalf("got text %q", text)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 calls (1 failure + 1 retry), got %d", got)
	}
}

// TestGemini_EmptyTextIsAnError is a regression test: Gemini's empty-response
// check only looked at Candidates/Parts length, not Text content, so a
// candidate with an empty-string part was returned as a silent success.
func TestGemini_EmptyTextIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"candidates":[{"content":{"parts":[{"text":""}]}}]}`))
	}))
	defer srv.Close()

	c := NewGeminiClient("key", "gemini-2.0-flash", srv.URL)
	_, err := c.Generate(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected an error for an empty-text candidate, got nil")
	}
}
