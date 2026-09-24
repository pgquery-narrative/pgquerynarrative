package service

import (
	"errors"
	"strings"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

func TestSanitizeAPIError_ScrubsDriverDetail(t *testing.T) {
	err := errors.New(`ERROR: relation "app.saved_queries" does not exist (SQLSTATE 42P01)`)
	got := SanitizeAPIError(err, "fallback")
	if got != "fallback" {
		t.Fatalf("expected fallback for driver detail, got %q", got)
	}
	if strings.Contains(got, "saved_queries") || strings.Contains(got, "SQLSTATE") {
		t.Fatalf("leaked driver detail: %q", got)
	}
}

func TestSanitizeAPIError_KeepsAppValidation(t *testing.T) {
	err := errors.New("destination_type must be webhook or log")
	got := SanitizeAPIError(err, "fallback")
	if got != err.Error() {
		t.Fatalf("expected app validation message, got %q", got)
	}
}

func TestSanitizeAPIError_Sentinel(t *testing.T) {
	got := SanitizeAPIError(apperrors.ErrQueryExecutionFailed, "fallback")
	if !strings.Contains(got, "Query execution failed") {
		t.Fatalf("expected sanitized execution message, got %q", got)
	}
}

// Regression: raw LLM provider error bodies (claude/openai/gemini/groq/ollama
// all format failures as "<provider> API error: <code> - <body>") must not
// reach the client or a log line verbatim, since the body can carry
// rate-limit, account, or model detail from the provider.
func TestSanitizeAPIError_ScrubsLLMProviderErrorBody(t *testing.T) {
	err := errors.New(`claude API error: 401 - {"error":{"type":"authentication_error","message":"org_01a2b3 revoked"}}`)
	got := SanitizeAPIError(err, "fallback")
	if got != "fallback" {
		t.Fatalf("expected fallback for a raw provider error body, got %q", got)
	}
	if strings.Contains(got, "org_01a2b3") {
		t.Fatalf("leaked provider account detail: %q", got)
	}
}

func TestSanitizeAPIError_SchemaNotAllowed(t *testing.T) {
	got := SanitizeAPIError(apperrors.ErrSchemaNotAllowed, "fallback")
	if got != apperrors.ErrSchemaNotAllowed.Error() {
		t.Fatalf("expected schema sentinel, got %q", got)
	}
}

// SF-08's whole point is that a typo surfaces its own message rather than
// falling back to a generic one that reads like a policy rejection — pin
// that the allowlist addition actually does that.
func TestSanitizeAPIError_SyntaxError(t *testing.T) {
	got := SanitizeAPIError(apperrors.ErrSyntaxError, "fallback")
	if got != apperrors.ErrSyntaxError.Error() {
		t.Fatalf("expected syntax-error sentinel, got %q", got)
	}
	if got == apperrors.ErrOnlySelectAllowed.Error() {
		t.Fatal("a syntax error must not fall back to the disallowed-statement message")
	}
}
