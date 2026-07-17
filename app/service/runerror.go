package service

import (
	"context"
	"errors"
	"strings"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// RunErrorKind classifies a query-runner error as timeout or generic validation.
type RunErrorKind int

const (
	RunErrorValidation RunErrorKind = iota
	RunErrorTimeout
	RunErrorTooLarge
)

// ClassifyRunError inspects err from queryrunner.Run and returns the kind and a user-facing message.
func ClassifyRunError(err error) (RunErrorKind, string) {
	if err == nil {
		return RunErrorValidation, "Query failed."
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, apperrors.ErrQueryTimeout) {
		return RunErrorTimeout, "Query timed out. Try a simpler query or reduce the amount of data."
	}
	if errors.Is(err, apperrors.ErrQueryResultTooLarge) {
		return RunErrorTooLarge, "Query result is too large. Reduce selected columns, rows, or value sizes."
	}
	for _, sentinel := range []error{
		apperrors.ErrQueryTooLong,
		apperrors.ErrOnlySelectAllowed,
		apperrors.ErrDisallowedKeyword,
		apperrors.ErrSchemaNotAllowed,
		apperrors.ErrUnqualifiedTable,
		apperrors.ErrMultipleStatements,
		apperrors.ErrStatStatementsUnavailable,
		apperrors.ErrExplainAnalyzeDisabled,
	} {
		if errors.Is(err, sentinel) {
			return RunErrorValidation, sentinel.Error()
		}
	}
	msg := err.Error()
	if strings.Contains(msg, "query execution timeout") ||
		strings.Contains(msg, "context deadline exceeded") ||
		strings.Contains(msg, "deadline exceeded") {
		return RunErrorTimeout, "Query timed out. Try a simpler query or reduce the amount of data."
	}
	if strings.Contains(msg, "query validation failed:") {
		return RunErrorValidation, sanitizeValidationMessage(msg)
	}
	return RunErrorValidation, "Query execution failed. Check your SQL and try again."
}

func sanitizeValidationMessage(msg string) string {
	const prefix = "query validation failed: "
	if idx := strings.Index(msg, prefix); idx >= 0 {
		inner := strings.TrimSpace(msg[idx+len(prefix):])
		switch {
		case strings.Contains(inner, apperrors.ErrQueryTooLong.Error()):
			return apperrors.ErrQueryTooLong.Error()
		case strings.Contains(inner, apperrors.ErrOnlySelectAllowed.Error()):
			return apperrors.ErrOnlySelectAllowed.Error()
		case strings.Contains(inner, apperrors.ErrDisallowedKeyword.Error()):
			return apperrors.ErrDisallowedKeyword.Error()
		case strings.Contains(inner, apperrors.ErrSchemaNotAllowed.Error()):
			return apperrors.ErrSchemaNotAllowed.Error()
		case strings.Contains(inner, apperrors.ErrUnqualifiedTable.Error()):
			return apperrors.ErrUnqualifiedTable.Error()
		case strings.Contains(inner, apperrors.ErrMultipleStatements.Error()):
			return apperrors.ErrMultipleStatements.Error()
		}
	}
	return "Query validation failed."
}
