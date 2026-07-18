package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const geminiBaseURL = "https://generativelanguage.googleapis.com/v1"

// GeminiClient calls the Google Gemini API for text generation.
type GeminiClient struct {
	apiKey string
	model  string
	client *http.Client
}

// NewGeminiClient returns a client for the Gemini API.
func NewGeminiClient(apiKey, model string) *GeminiClient {
	if model == "" {
		model = "gemini-2.0-flash"
	}
	return &GeminiClient{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *GeminiClient) Name() string  { return "gemini" }
func (c *GeminiClient) Model() string { return c.model }

const geminiMaxRetries = 3
const geminiRetryDelay = 6 * time.Second

func (c *GeminiClient) Generate(ctx context.Context, prompt string) (string, error) {
	result, err := c.GenerateWithUsage(ctx, prompt)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func (c *GeminiClient) GenerateWithUsage(ctx context.Context, prompt string) (GenerationResult, error) {
	return c.GenerateMessages(ctx, PromptToChatMessages(prompt))
}

// GenerateMessages flattens role-tagged messages into Gemini's single-turn contents API.
func (c *GeminiClient) GenerateMessages(ctx context.Context, messages []ChatMessage) (GenerationResult, error) {
	if c.apiKey == "" {
		return GenerationResult{}, fmt.Errorf("gemini: LLM_API_KEY is required")
	}
	var b strings.Builder
	for i, m := range messages {
		if i > 0 {
			b.WriteString("\n\n")
		}
		role := strings.TrimSpace(m.Role)
		if role == "" {
			role = "user"
		}
		b.WriteString(strings.ToUpper(role))
		b.WriteString(":\n")
		b.WriteString(m.Content)
	}
	prompt := b.String()
	url := fmt.Sprintf("%s/models/%s:generateContent", geminiBaseURL, c.model)
	payload := map[string]interface{}{
		"contents": []map[string]interface{}{
			{"parts": []map[string]interface{}{{"text": prompt}}},
		},
		"generationConfig": map[string]interface{}{
			"temperature":     0.7,
			"maxOutputTokens": 2048,
		},
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("gemini: marshal request: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < geminiMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return GenerationResult{}, fmt.Errorf("gemini: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", c.apiKey)
		resp, err := c.client.Do(req)
		if err != nil {
			return GenerationResult{}, fmt.Errorf("gemini: request: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // #nosec G104 -- best-effort error-body read; empty body on failure just yields a less detailed error message.
		resp.Body.Close()                                           // #nosec G104 -- close error on a body we're discarding is not actionable.
		if resp.StatusCode == http.StatusOK {
			var result struct {
				Candidates []struct {
					Content struct {
						Parts []struct {
							Text string `json:"text"`
						} `json:"parts"`
					} `json:"content"`
				} `json:"candidates"`
				UsageMetadata struct {
					PromptTokenCount     int `json:"promptTokenCount"`
					CandidatesTokenCount int `json:"candidatesTokenCount"`
				} `json:"usageMetadata"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return GenerationResult{}, fmt.Errorf("gemini: decode response: %w", err)
			}
			if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
				return GenerationResult{}, fmt.Errorf("gemini: empty response")
			}
			text := result.Candidates[0].Content.Parts[0].Text
			usage := Usage{
				PromptTokens:     result.UsageMetadata.PromptTokenCount,
				CompletionTokens: result.UsageMetadata.CandidatesTokenCount,
			}
			reported := usage.PromptTokens > 0 || usage.CompletionTokens > 0
			return GenerationResult{Text: text, Usage: usage, UsageReported: reported}, nil
		}
		lastErr = fmt.Errorf("gemini API error: %d - %s", resp.StatusCode, string(body))
		if resp.StatusCode == 429 && attempt < geminiMaxRetries-1 {
			select {
			case <-ctx.Done():
				return GenerationResult{}, ctx.Err()
			case <-time.After(geminiRetryDelay):
			}
			continue
		}
		return GenerationResult{}, lastErr
	}
	return GenerationResult{}, lastErr
}
