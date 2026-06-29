package service

import (
	"errors"
	"strings"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// SanitizeClientMessage returns a safe user-facing message for API responses.
func SanitizeClientMessage(err error) string {
	if err == nil {
		return "An error occurred."
	}
	kind, msg := ClassifyRunError(err)
	_ = kind
	if msg != "" {
		return msg
	}
	return "An error occurred."
}

// SanitizeStoredError returns a safe message persisted or returned from background jobs.
func SanitizeStoredError(err error) string {
	if err == nil {
		return ""
	}
	for _, sentinel := range []error{
		apperrors.ErrQueryTooLong,
		apperrors.ErrOnlySelectAllowed,
		apperrors.ErrDisallowedKeyword,
		apperrors.ErrSchemaNotAllowed,
		apperrors.ErrUnqualifiedTable,
		apperrors.ErrMultipleStatements,
		apperrors.ErrQueryTimeout,
		apperrors.ErrStatStatementsUnavailable,
		apperrors.ErrExplainAnalyzeDisabled,
	} {
		if errors.Is(err, sentinel) {
			return sentinel.Error()
		}
	}
	msg := strings.TrimSpace(err.Error())
	if strings.Contains(msg, "query validation failed:") {
		return sanitizeValidationMessage(msg)
	}
	if strings.Contains(msg, "webhook") {
		return "Webhook delivery failed."
	}
	return "Operation failed."
}
