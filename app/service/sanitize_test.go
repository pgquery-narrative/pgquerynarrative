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
