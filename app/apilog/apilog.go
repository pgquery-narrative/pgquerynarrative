// Package apilog provides console logging for API errors and important events.
// All messages use structured format (timestamp, level, message, key=value) for visibility.
package apilog

import (
	"github.com/pgquerynarrative/pgquerynarrative/internal/logger"
)

// ValidationError logs a validation error (query or report) for the given endpoint.
func ValidationError(endpoint, name, message string) {
	logger.DefaultLogger().Err("validation_error", "endpoint", endpoint, "name", name, "message", message)
}

// LLMError logs an LLM/narrative generation error.
func LLMError(message string) {
	logger.DefaultLogger().Err("llm_error", "message", message)
}

// APIError logs a generic API error response (e.g. 4xx/5xx).
func APIError(method, path string, status int) {
	logger.DefaultLogger().Err("api_error", "method", method, "path", path, "status", status)
}

// Request logs a successful API request (e.g. report generated).
func Request(endpoint, detail string) {
	logger.DefaultLogger().Info("api", "endpoint", endpoint, "detail", detail)
}
