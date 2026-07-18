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

const openaiBaseURL = "https://api.openai.com/v1"

// OpenAIClient calls the OpenAI Chat Completions API for text generation (GPT).
type OpenAIClient struct {
	apiKey string
	model  string
	client *http.Client
}

// NewOpenAIClient returns a client for the OpenAI API.
// apiKey is the OpenAI API key (from LLM_API_KEY). model is the model name (e.g. gpt-4o, gpt-4o-mini, gpt-4-turbo).
func NewOpenAIClient(apiKey, model string) *OpenAIClient {
	if model == "" {
		model = "gpt-4o-mini"
	}
	return &OpenAIClient{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// Name returns the provider name.
func (c *OpenAIClient) Name() string {
	return "openai"
}

func (c *OpenAIClient) Model() string {
	return c.model
}

const openaiMaxRetries = 3
const openaiRetryDelay = 6 * time.Second

// Generate sends the prompt to OpenAI Chat Completions and returns the generated text.
func (c *OpenAIClient) Generate(ctx context.Context, prompt string) (string, error) {
	result, err := c.GenerateWithUsage(ctx, prompt)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

// GenerateWithUsage returns generated text and provider-reported token usage when available.
func (c *OpenAIClient) GenerateWithUsage(ctx context.Context, prompt string) (GenerationResult, error) {
	return c.GenerateMessages(ctx, PromptToChatMessages(prompt))
}

// GenerateMessages sends structured system/user messages to OpenAI Chat Completions.
func (c *OpenAIClient) GenerateMessages(ctx context.Context, messages []ChatMessage) (GenerationResult, error) {
	if c.apiKey == "" {
		return GenerationResult{}, fmt.Errorf("openai: LLM_API_KEY is required")
	}
	if len(messages) == 0 {
		return GenerationResult{}, fmt.Errorf("openai: empty messages")
	}

	url := openaiBaseURL + "/chat/completions"

	payload := map[string]interface{}{
		"model":       c.model,
		"messages":    chatMessagesPayload(messages),
		"max_tokens":  2048,
		"temperature": 0.7,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("openai: marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt < openaiMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return GenerationResult{}, fmt.Errorf("openai: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.client.Do(req)
		if err != nil {
			return GenerationResult{}, fmt.Errorf("openai: request: %w", err)
		}

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // #nosec G104 -- best-effort error-body read; empty body on failure just yields a less detailed error message.
		resp.Body.Close()                                           // #nosec G104 -- close error on a body we're discarding is not actionable.

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
				return GenerationResult{}, fmt.Errorf("openai: decode response: %w", err)
			}
			if len(result.Choices) == 0 || result.Choices[0].Message.Content == "" {
				return GenerationResult{}, fmt.Errorf("openai: empty response")
			}
			usage := Usage{PromptTokens: result.Usage.PromptTokens, CompletionTokens: result.Usage.CompletionTokens}
			reported := usage.PromptTokens > 0 || usage.CompletionTokens > 0
			return GenerationResult{
				Text:          result.Choices[0].Message.Content,
				Usage:         usage,
				UsageReported: reported,
			}, nil
		}

		lastErr = fmt.Errorf("openai API error: %d - %s", resp.StatusCode, string(body))

		if resp.StatusCode == 429 && attempt < openaiMaxRetries-1 {
			select {
			case <-ctx.Done():
				return GenerationResult{}, ctx.Err()
			case <-time.After(openaiRetryDelay):
				// retry
			}
			continue
		}

		return GenerationResult{}, lastErr
	}
	return GenerationResult{}, lastErr
}
