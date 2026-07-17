package llm

import "context"

// Usage reports token consumption from an LLM provider when available.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
}

// GenerationResult is the output of a single LLM call.
type GenerationResult struct {
	Text          string
	Usage         Usage
	UsageReported bool // true when provider returned token counts
}

// UsageReportingClient is implemented by providers that expose token usage metadata.
type UsageReportingClient interface {
	Client
	GenerateWithUsage(ctx context.Context, prompt string) (GenerationResult, error)
}

// GenerateWithUsage calls GenerateWithUsage when supported, otherwise estimates tokens.
func GenerateWithUsage(ctx context.Context, client Client, prompt string) (GenerationResult, error) {
	if ur, ok := client.(UsageReportingClient); ok {
		return ur.GenerateWithUsage(ctx, prompt)
	}
	text, err := client.Generate(ctx, prompt)
	if err != nil {
		return GenerationResult{}, err
	}
	promptTokens := EstimateTokenCount(prompt)
	completionTokens := EstimateTokenCount(text)
	return GenerationResult{
		Text: text,
		Usage: Usage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
		},
	}, nil
}
