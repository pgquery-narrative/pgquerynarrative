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

const groqBaseURL = "https://api.groq.com/openai/v1"

// GroqClient calls the Groq OpenAI-compatible Chat Completions API for text generation.
type GroqClient struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGroqClient returns a client for the Groq API.
// apiKey is the Groq API key (from LLM_API_KEY). model is the model name (e.g. llama-3.3-70b-versatile, llama-3.1-8b-instant, mixtral-8x7b-32768).
func NewGroqClient(apiKey, model string) *GroqClient {
	if model == "" {
		model = "llama-3.3-70b-versatile"
	}
	return &GroqClient{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Name returns the provider name.
func (c *GroqClient) Name() string {
	return "groq"
}

func (c *GroqClient) Model() string {
	return c.model
}

const groqMaxRetries = 3
const groqRetryDelay = 6 * time.Second

// Generate sends the prompt to Groq Chat Completions and returns the generated text.
func (c *GroqClient) Generate(ctx context.Context, prompt string) (string, error) {
	result, err := c.GenerateWithUsage(ctx, prompt)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// GenerateWithUsage returns generated text and provider-reported token usage when available.
func (c *GroqClient) GenerateWithUsage(ctx context.Context, prompt string) (GenerationResult, error) {
	if c.apiKey == "" {
		return GenerationResult{}, fmt.Errorf("groq: LLM_API_KEY is required")
	}

	url := groqBaseURL + "/chat/completions"

	payload := map[string]interface{}{
		"model":       c.model,
		"messages":    []map[string]interface{}{{"role": "user", "content": prompt}},
		"max_tokens":  2048,
		"temperature": 0.7,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("groq: marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < groqMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return GenerationResult{}, fmt.Errorf("groq: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.client.Do(req)
		if err != nil {
			return GenerationResult{}, fmt.Errorf("groq: request: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024))
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var result struct {
				Choices []struct {
					Message struct {
						Content string `json:"content"`
					} `json:"message"`
				} `json:"choices"`
				Usage struct {
					PromptTokens     int `json:"prompt_tokens"`
					CompletionTokens int `json:"completion_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return GenerationResult{}, fmt.Errorf("groq: decode response: %w", err)
			}
			if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
				return GenerationResult{}, fmt.Errorf("groq: empty response")
			}
			usage := Usage{PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens}
			reported := usage.PromptTokens > 0 || usage.CompletionTokens > 0
			return GenerationResult{
				Text:          result.Choices[0].Message.Content,
				Usage:         usage,
				UsageReported: reported,
			}, nil
		}

		lastErr = fmt.Errorf("groq API error: %d - %s", resp.StatusCode, string(body))

		if resp.StatusCode == 429 && attempt < groqMaxRetries-1 {
			select {
			case <-ctx.Done():
				return GenerationResult{}, ctx.Err()
			case <-time.After(groqRetryDelay):
				// retry
			}
			continue
		}

		return GenerationResult{}, lastErr
	}
	return GenerationResult{}, lastErr
}
