package llm_test

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/llm"
)

func TestGeminiGenerateWithUsage_ParsesProviderTokens(t *testing.T) {
	stub := usageStub{
		name: "gemini",
		result: llm.GenerationResult{
			Text:          "hello gemini",
			Usage:         llm.Usage{PromptTokens: 11, CompletionTokens: 7},
			UsageReported: true,
		},
	}
	gen, err := llm.GenerateWithUsage(context.Background(), stub, "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if gen.Usage.PromptTokens != 11 || gen.Usage.CompletionTokens != 7 {
		t.Fatalf("usage=%+v", gen.Usage)
	}
}

func TestClaudeGenerateWithUsage_ParsesProviderTokens(t *testing.T) {
	stub := usageStub{
		name: "claude",
		result: llm.GenerationResult{
			Text:          "hello claude",
			Usage:         llm.Usage{PromptTokens: 20, CompletionTokens: 15},
			UsageReported: true,
		},
	}
	gen, err := llm.GenerateWithUsage(context.Background(), stub, "prompt")
	if err != nil {
		t.Fatal(err)
	}
	if !gen.UsageReported || gen.Usage.CompletionTokens != 15 {
		t.Fatalf("usage=%+v reported=%v", gen.Usage, gen.UsageReported)
	}
}

type usageStub struct {
	name   string
	result llm.GenerationResult
	err    error
}

func (u usageStub) Generate(context.Context, string) (string, error) {
	if u.err != nil {
		return "", u.err
	}
	return u.result.Text, nil
}
func (u usageStub) Name() string { return u.name }
func (u usageStub) GenerateWithUsage(_ context.Context, _ string) (llm.GenerationResult, error) {
	if u.err != nil {
		return llm.GenerationResult{}, u.err
	}
	return u.result, nil
}
