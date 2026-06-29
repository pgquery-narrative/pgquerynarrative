package llm

// PromptOptions controls what query result data is included in LLM narrative prompts.
type PromptOptions struct {
	MaxSampleRows int  // Maximum rows to include (0 = none when SendRowData is true uses default cap)
	SendRowData   bool // When false, omit row values from the prompt
	RedactPII     bool // When true, mask sensitive columns and PII patterns in sample rows
}

// DefaultPromptOptions is used when no options are configured.
func DefaultPromptOptions() PromptOptions {
	return PromptOptions{MaxSampleRows: 5, SendRowData: true}
}
