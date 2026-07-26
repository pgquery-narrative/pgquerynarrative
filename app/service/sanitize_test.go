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
