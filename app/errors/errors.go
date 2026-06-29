// Package errors provides centralized error definitions for the application.
// All application-specific errors are defined here for better organization
// and easier maintenance.
package errors

import (
	"errors"
	"fmt"
)

// Query validation errors
var (
	// ErrQueryTooLong indicates the query exceeds the maximum allowed length.
	// This prevents DoS attacks through extremely long queries.
	ErrQueryTooLong = errors.New("query exceeds maximum length")

	// ErrOnlySelectAllowed indicates the query is not a SELECT or WITH statement.
	// Only read-only queries are allowed for security.
	ErrOnlySelectAllowed = errors.New("only SELECT statements are allowed")

	// ErrDisallowedKeyword indicates the query contains dangerous keywords.
	// Keywords like INSERT, UPDATE, DELETE, DROP, etc. are not allowed.
	ErrDisallowedKeyword = errors.New("query contains disallowed keywords")

	// ErrSchemaNotAllowed indicates the query references a schema that is not allowed.
	// This enforces schema-level access control.
	ErrSchemaNotAllowed = errors.New("query references disallowed schema")

	// ErrUnqualifiedTable indicates a table reference without an explicit schema qualifier.
	// Unqualified names resolve via search_path and bypass schema allowlists.
	ErrUnqualifiedTable = errors.New("table references must use an explicit schema (e.g. demo.sales)")

	// ErrMultipleStatements indicates the query contains multiple SQL statements.
	// Only single-statement queries are allowed for security.
	ErrMultipleStatements = errors.New("multiple SQL statements are not allowed")
)

// Query execution errors
var (
	// ErrQueryTimeout indicates the query execution exceeded the maximum allowed time.
	// This prevents long-running queries from blocking the system.
	ErrQueryTimeout = errors.New("query execution timeout: query exceeded the maximum execution time")

	// ErrQueryExecutionFailed indicates the query execution failed for reasons other than timeout.
	// This is a generic error for database execution failures.
	ErrQueryExecutionFailed = errors.New("query execution failed")
)

// Database connection errors
var (
	// ErrDatabaseConnectionFailed indicates a failure to establish database connection.
	ErrDatabaseConnectionFailed = errors.New("failed to create database connection")

	// ErrReadOnlyPoolFailed indicates a failure to create the read-only connection pool.
	ErrReadOnlyPoolFailed = errors.New("failed to create read-only pool")

	// ErrAppPoolFailed indicates a failure to create the application connection pool.
	ErrAppPoolFailed = errors.New("failed to create app pool")

	// ErrPoolHealthCheckFailed indicates a failure in database pool health check.
	ErrPoolHealthCheckFailed = errors.New("database pool health check failed")
)

// LLM/Narrative generation errors
var (
	// ErrLLMRequestFailed indicates a failure in LLM API request.
	ErrLLMRequestFailed = errors.New("LLM request failed")

	// ErrLLMResponseInvalid indicates the LLM response is invalid or malformed.
	ErrLLMResponseInvalid = errors.New("LLM response is invalid")

	// ErrNarrativeGenerationFailed indicates a failure in narrative generation.
	ErrNarrativeGenerationFailed = errors.New("narrative generation failed")
)

// Input validation errors
var (
	// ErrEmptyQuery is returned when a query is called with empty or whitespace-only SQL.
	ErrEmptyQuery = errors.New("query SQL cannot be empty")
)

// Service layer errors
var (
	// ErrSavedQueryNotFound indicates the requested saved query does not exist.
	ErrSavedQueryNotFound = errors.New("saved query not found")

	// ErrReportNotFound indicates the requested report does not exist.
	ErrReportNotFound = errors.New("report not found")

	// ErrInvalidQueryLimit indicates the query limit is invalid (negative or too large).
	ErrInvalidQueryLimit = errors.New("invalid query limit")

	// ErrInvalidStatStatementsOrder indicates an unsupported order_by for pg_stat_statements.
	ErrInvalidStatStatementsOrder = errors.New("invalid stat_statements order_by")

	// ErrStatStatementsUnavailable indicates pg_stat_statements is not enabled or not readable.
	ErrStatStatementsUnavailable = errors.New("pg_stat_statements is not available")

	// ErrExplainAnalyzeDisabled indicates EXPLAIN ANALYZE was requested but is disabled by policy.
	ErrExplainAnalyzeDisabled = errors.New("EXPLAIN ANALYZE is disabled")
)

// Helper functions for wrapping errors with context. Use %w so callers can use errors.Is/As.

// WrapQueryError wraps a query-related error with additional context.
func WrapQueryError(err error, ctx string) error {
	return fmt.Errorf("%s: %w", ctx, err)
}

// WrapDatabaseError wraps a database-related error with additional context.
func WrapDatabaseError(err error, operation string) error {
	return fmt.Errorf("%s: %w", operation, err)
}

// WrapLLMError wraps an LLM-related error with additional context.
func WrapLLMError(err error, operation string) error {
	return fmt.Errorf("%s: %w", operation, err)
}
