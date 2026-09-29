package llm

import (
	"context"
)

// Client is the interface for LLM providers
type Client interface {
	// Generate generates a narrative from the given prompt
	Generate(ctx context.Context, prompt string) (string, error)

	// Name returns the provider name (e.g., "ollama", "gemini", "claude", "openai", "groq")
	Name() string
}

// Modeler is implemented by LLM clients that can expose the configured model name.
type Modeler interface {
	Model() string
}

// Config contains LLM configuration
type Config struct {
	Provider string // "ollama", "gemini", "claude", "openai", "groq"
	Model    string // Model name
	APIKey   string // API key (for cloud providers)
	BaseURL  string // Base URL (for local providers like Ollama)
}
