// Command mockollama serves a minimal Ollama-compatible /api/generate endpoint
// for Playwright E2E so report generation does not require a real LLM.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

const defaultNarrative = `{"headline":"Playwright E2E headline","takeaways":["Takeaway one","Takeaway two"],"drivers":["Driver one"],"limitations":["Limitation one"],"recommendations":["Recommendation one"]}`

func main() {
	addr := os.Getenv("MOCK_OLLAMA_ADDR")
	if addr == "" {
		addr = "127.0.0.1:11435"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/generate", handleGenerate)
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"name":"playwright"}]}`))
	})
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("OK"))
	})
	log.Printf("mock Ollama listening on http://%s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil { // #nosec G114 -- local test-only stub
		log.Fatal(err)
	}
}

func handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Drain body so clients can reuse connections; content is unused.
	_ = json.NewDecoder(r.Body).Decode(&map[string]any{})
	_ = r.Body.Close()

	resp := map[string]any{
		"model":             "playwright",
		"created_at":        "2026-01-01T00:00:00Z",
		"response":          defaultNarrative,
		"done":              true,
		"prompt_eval_count": 10,
		"eval_count":        20,
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("encode: %v", err)
	}
}
