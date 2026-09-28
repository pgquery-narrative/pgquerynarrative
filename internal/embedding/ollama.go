package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OllamaEmbedder calls Ollama's /api/embed endpoint (e.g. nomic-embed-text).
type OllamaEmbedder struct {
	baseURL string
	model   string
	client  *http.Client
}

// NewOllamaEmbedder creates an embedder that uses Ollama. baseURL is the
// Ollama server (e.g. http://localhost:11434). model is the embedding model
// name (e.g. nomic-embed-text).
func NewOllamaEmbedder(baseURL, model string) *OllamaEmbedder {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "nomic-embed-text"
	}
	return &OllamaEmbedder{
		baseURL: baseURL,
		model:   model,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Embed implements Embedder using Ollama /api/embed. Input is sent as single string.
func (e *OllamaEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	url := e.baseURL + "/api/embed"
	body := map[string]string{
		"model": e.model,
		"input": text,
	}
	jsonData, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("embedding marshal: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(jsonData))
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embedding response %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Embeddings [][]float64 `json:"embeddings"`
		Embedding  []float64   `json:"embedding"` // some Ollama versions use singular
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("embedding decode: %w", err)
	}
	var raw []float64
	if len(out.Embeddings) > 0 && len(out.Embeddings[0]) > 0 {
		raw = out.Embeddings[0]
	} else if len(out.Embedding) > 0 {
		raw = out.Embedding
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("embedding empty response")
	}
	vec := make([]float32, len(raw))
	for i, v := range raw {
		vec[i] = float32(v)
	}
	return vec, nil
}
