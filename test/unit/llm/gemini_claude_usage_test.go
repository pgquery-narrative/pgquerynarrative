package llm

import (
	"encoding/json"
	"testing"
)

func TestGeminiResponseUsageMetadata(t *testing.T) {
	body := []byte(`{
		"candidates":[{"content":{"parts":[{"text":"hello"}]}}],
		"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":7}
	}`)
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
		t.Fatal(err)
	}
	if result.UsageMetadata.PromptTokenCount != 11 || result.UsageMetadata.CandidatesTokenCount != 7 {
		t.Fatalf("usage=%+v", result.UsageMetadata)
	}
}

func TestClaudeResponseUsage(t *testing.T) {
	body := []byte(`{
		"content":[{"type":"text","text":"hello"}],
		"usage":{"input_tokens":20,"output_tokens":15}
	}`)
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
		t.Fatal(err)
	}
	if result.Usage.InputTokens != 20 || result.Usage.OutputTokens != 15 {
		t.Fatalf("usage=%+v", result.Usage)
	}
}
