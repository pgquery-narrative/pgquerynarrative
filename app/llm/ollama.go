package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type OllamaClient struct {
	baseURL string
	model   string
	client  *http.Client
}

func NewOllamaClient(baseURL, model string) *OllamaClient {
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	if model == "" {
		model = "llama3.2"
	}

	return &OllamaClient{
		baseURL: baseURL,
		model:   model,
		client: &http.Client{
			// CPU-backed Ollama can take longer to return first token on cold start.
			Timeout: 300 * time.Second,
		},
	}
}

func (c *OllamaClient) Name() string {
	return "ollama"
}

func (c *OllamaClient) Model() string {
	return c.model
}
func (c *OllamaClient) Generate(ctx context.Context, prompt string) (string, error) {
	result, err := c.GenerateWithUsage(ctx, prompt)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func (c *OllamaClient) GenerateWithUsage(ctx context.Context, prompt string) (GenerationResult, error) {
	url := fmt.Sprintf("%s/api/generate", c.baseURL)

	payload := map[string]interface{}{
		"model":  c.model,
		"prompt": prompt,
		"stream": false,
		"options": map[string]interface{}{
			"temperature": 0.7,
			// Keep response size bounded to reduce end-to-end latency for Ask/Explain.
			"num_predict": 768,
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("failed to marshal request: %w", err)
	}

	maxRetries := 3
	retryDelay := 1 * time.Second
	var lastErr error

	for attempt := 0; attempt < maxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return GenerationResult{}, fmt.Errorf("failed to create request: %w", err)
		}

		req.Header.Set("Content-Type", "application/json")

		resp, err := c.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("failed to send request: %w", err)
			if attempt < maxRetries-1 {
				time.Sleep(retryDelay)
				retryDelay *= 2
				continue
			}
			return GenerationResult{}, lastErr
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			lastErr = fmt.Errorf("ollama API error: %d - %s", resp.StatusCode, string(body))
			if resp.StatusCode >= 500 && attempt < maxRetries-1 {
				time.Sleep(retryDelay)
				retryDelay *= 2
				continue
			}
			return GenerationResult{}, lastErr
		}

		var result struct {
			Response        string `json:"response"`
			Done            bool   `json:"done"`
			PromptEvalCount int    `json:"prompt_eval_count"`
			EvalCount       int    `json:"eval_count"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			return GenerationResult{}, fmt.Errorf("failed to decode response: %w", err)
		}
		resp.Body.Close()

		if result.Done && result.Response != "" {
			usage := Usage{PromptTokens: result.PromptEvalCount, CompletionTokens: result.EvalCount}
			reported := usage.PromptTokens > 0 || usage.CompletionTokens > 0
			return GenerationResult{
				Text:          result.Response,
				Usage:         usage,
				UsageReported: reported,
			}, nil
		}

		if result.Done && result.Response == "" && attempt < maxRetries-1 {
			time.Sleep(retryDelay)
			retryDelay *= 2
			continue
		}

		return GenerationResult{Text: result.Response}, nil
	}

	return GenerationResult{}, lastErr
}
