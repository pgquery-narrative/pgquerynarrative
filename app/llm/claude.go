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

const claudeBaseURL = "https://api.anthropic.com/v1"
const claudeAPIVersion = "2023-06-01"

// ClaudeClient calls the Anthropic Messages API for text generation.
type ClaudeClient struct {
	apiKey string
	model  string
	client *http.Client
}

// NewClaudeClient returns a client for the Claude API.
func NewClaudeClient(apiKey, model string) *ClaudeClient {
	if model == "" {
		model = "claude-3-5-sonnet-20241022"
	}
	return &ClaudeClient{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *ClaudeClient) Name() string  { return "claude" }
func (c *ClaudeClient) Model() string { return c.model }

const claudeMaxRetries = 3
const claudeRetryDelay = 6 * time.Second

func (c *ClaudeClient) Generate(ctx context.Context, prompt string) (string, error) {
	result, err := c.GenerateWithUsage(ctx, prompt)
	if err != nil {
		return "", err
	}
	return result.Text, nil
}

func (c *ClaudeClient) GenerateWithUsage(ctx context.Context, prompt string) (GenerationResult, error) {
	return c.GenerateMessages(ctx, PromptToChatMessages(prompt))
}

// GenerateMessages sends structured messages to Anthropic. System content is
// lifted into the top-level system field; remaining turns go in messages.
func (c *ClaudeClient) GenerateMessages(ctx context.Context, messages []ChatMessage) (GenerationResult, error) {
	if c.apiKey == "" {
		return GenerationResult{}, fmt.Errorf("claude: LLM_API_KEY is required")
	}
	if len(messages) == 0 {
		return GenerationResult{}, fmt.Errorf("claude: empty messages")
	}
	var system string
	var turns []map[string]string
	for _, m := range messages {
		role := strings.TrimSpace(m.Role)
		switch role {
		case "system":
			if system != "" {
				system += "\n\n"
			}
			system += m.Content
		case "assistant":
			turns = append(turns, map[string]string{"role": "assistant", "content": m.Content})
		default:
			turns = append(turns, map[string]string{"role": "user", "content": m.Content})
		}
	}
	if len(turns) == 0 {
		turns = []map[string]string{{"role": "user", "content": system}}
		system = ""
	}
	url := claudeBaseURL + "/messages"
	payload := map[string]interface{}{
		"model":       c.model,
		"max_tokens":  2048,
		"messages":    turns,
		"temperature": 0.7,
	}
	if system != "" {
		payload["system"] = system
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return GenerationResult{}, fmt.Errorf("claude: marshal request: %w", err)
	}
	var lastErr error
	for attempt := 0; attempt < claudeMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBuffer(jsonData))
		if err != nil {
			return GenerationResult{}, fmt.Errorf("claude: create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", c.apiKey)
		req.Header.Set("anthropic-version", claudeAPIVersion)
		resp, err := c.client.Do(req)
		if err != nil {
			return GenerationResult{}, fmt.Errorf("claude: request: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024*1024)) // #nosec G104 -- best-effort error-body read; empty body on failure just yields a less detailed error message.
		resp.Body.Close()                                           // #nosec G104 -- close error on a body we're discarding is not actionable.
		if resp.StatusCode == http.StatusOK {
			var result struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Usage struct {
					InputTokens  int `json:"input_tokens"`
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal(body, &result); err != nil {
				return GenerationResult{}, fmt.Errorf("claude: decode response: %w", err)
			}
			var text string
			for _, block := range result.Content {
				if block.Type == "text" {
					text += block.Text
				}
			}
			if text == "" {
				return GenerationResult{}, fmt.Errorf("claude: empty response")
			}
			usage := Usage{
				PromptTokens:     result.Usage.InputTokens,
				CompletionTokens: result.Usage.OutputTokens,
			}
			reported := usage.PromptTokens > 0 || usage.CompletionTokens > 0
			return GenerationResult{Text: text, Usage: usage, UsageReported: reported}, nil
		}
		lastErr = fmt.Errorf("claude API error: %d - %s", resp.StatusCode, string(body))
		if resp.StatusCode == 429 && attempt < claudeMaxRetries-1 {
			select {
			case <-ctx.Done():
				return GenerationResult{}, ctx.Err()
			case <-time.After(claudeRetryDelay):
			}
			continue
		}
		return GenerationResult{}, lastErr
	}
	return GenerationResult{}, lastErr
}
