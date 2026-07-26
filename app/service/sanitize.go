package service

import (
	"errors"
	"strings"
	"unicode"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// SanitizeClientMessage returns a safe user-facing message for API responses.
func SanitizeClientMessage(err error) string {
	if err == nil {
		return "An error occurred."
	}
	_, msg := ClassifyRunError(err)
	if msg != "" {
		return msg
	}
	return "An error occurred."
}

// SanitizeAPIError returns a client-safe message. Hand-authored validation strings
// (short, no driver fingerprints) pass through; Postgres/driver detail is replaced
// with fallback.
func SanitizeAPIError(err error, fallback string) string {
	if err == nil {
		if fallback != "" {
			return fallback
		}
		return "An error occurred."
	}
	if fallback == "" {
		fallback = "An error occurred."
	}
	for _, sentinel := range []error{
		apperrors.ErrQueryTooLong,
		apperrors.ErrOnlySelectAllowed,
		apperrors.ErrDisallowedKeyword,
		apperrors.ErrSchemaNotAllowed,
		apperrors.ErrUnqualifiedTable,
		apperrors.ErrMultipleStatements,
		apperrors.ErrQueryTimeout,
		apperrors.ErrQueryResultTooLarge,
		apperrors.ErrStatStatementsUnavailable,
		apperrors.ErrExplainAnalyzeDisabled,
		apperrors.ErrQueryExecutionFailed,
	} {
		if errors.Is(err, sentinel) {
			if errors.Is(err, apperrors.ErrQueryExecutionFailed) {
				return "Query execution failed. Check your SQL and try again."
			}
			if errors.Is(err, apperrors.ErrQueryTimeout) {
				return "Query timed out. Try a simpler query or reduce the amount of data."
			}
			if errors.Is(err, apperrors.ErrQueryResultTooLarge) {
				return "Query result is too large. Reduce selected columns, rows, or value sizes."
			}
			return sentinel.Error()
		}
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return fallback
	}
	if strings.Contains(msg, "query validation failed:") {
		return sanitizeValidationMessage(msg)
	}
	if looksLikeDriverOrInternalDetail(msg) {
		return fallback
	}
	// Allow short application validation messages we author ourselves.
	if len(msg) <= 180 && !strings.Contains(msg, "\n") && isPrintableASCII(msg) {
		return msg
	}
	return fallback
}

// SanitizeStoredError returns a safe message persisted or returned from background jobs.
func SanitizeStoredError(err error) string {
	if err == nil {
		return ""
	}
	return SanitizeAPIError(err, "Operation failed.")
}

func looksLikeDriverOrInternalDetail(msg string) bool {
	lower := strings.ToLower(msg)
	needles := []string{
		"sqlstate",
		"pq:",
		"pgconn",
		"pgx",
		`relation "`,
		`column "`,
		`database "`,
		"permission denied for",
		"violates foreign key",
		"violates unique",
		"duplicate key",
		"deadlock detected",
		"could not connect",
		"connection refused",
		"dial tcp",
		"tls:",
		"x509:",
		"stack trace",
		"runtime error",
		"goroutine ",
		" /users/",
		" /home/",
		"c:\\",
	}
	for _, n := range needles {
		if strings.Contains(lower, n) {
			return true
		}
	}
	return false
}

func isPrintableASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII || (!unicode.IsPrint(r) && !unicode.IsSpace(r)) {
			return false
		}
	}
	return true
}
