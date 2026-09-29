package errors_test

import (
	"errors"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/internal/errors"
)

func TestSentinels_Is(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		target error
		want   bool
	}{
		{"ErrQueryTooLong", apperrors.ErrQueryTooLong, apperrors.ErrQueryTooLong, true},
		{"ErrOnlySelectAllowed", apperrors.ErrOnlySelectAllowed, apperrors.ErrOnlySelectAllowed, true},
		{"ErrDisallowedKeyword", apperrors.ErrDisallowedKeyword, apperrors.ErrDisallowedKeyword, true},
		{"ErrSchemaNotAllowed", apperrors.ErrSchemaNotAllowed, apperrors.ErrSchemaNotAllowed, true},
		{"ErrMultipleStatements", apperrors.ErrMultipleStatements, apperrors.ErrMultipleStatements, true},
		{"ErrQueryTimeout", apperrors.ErrQueryTimeout, apperrors.ErrQueryTimeout, true},
		{"ErrQueryExecutionFailed", apperrors.ErrQueryExecutionFailed, apperrors.ErrQueryExecutionFailed, true},
		{"ErrSavedQueryNotFound", apperrors.ErrSavedQueryNotFound, apperrors.ErrSavedQueryNotFound, true},
		{"ErrReportNotFound", apperrors.ErrReportNotFound, apperrors.ErrReportNotFound, true},
		{"ErrInvalidQueryLimit", apperrors.ErrInvalidQueryLimit, apperrors.ErrInvalidQueryLimit, true},
		{"ErrLLMRequestFailed", apperrors.ErrLLMRequestFailed, apperrors.ErrLLMRequestFailed, true},
		{"ErrLLMResponseInvalid", apperrors.ErrLLMResponseInvalid, apperrors.ErrLLMResponseInvalid, true},
		{"wrong target", apperrors.ErrQueryTooLong, apperrors.ErrSchemaNotAllowed, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errors.Is(tt.err, tt.target); got != tt.want {
				t.Errorf("errors.Is(%v, %v) = %v, want %v", tt.err, tt.target, got, tt.want)
			}
		})
	}
}

func TestWrappers_PreserveSentinel(t *testing.T) {
	wrapped := apperrors.WrapQueryError(apperrors.ErrSchemaNotAllowed, "validate")
	if !errors.Is(wrapped, apperrors.ErrSchemaNotAllowed) {
		t.Errorf("WrapQueryError: errors.Is(wrapped, ErrSchemaNotAllowed) = false, want true")
	}
}
