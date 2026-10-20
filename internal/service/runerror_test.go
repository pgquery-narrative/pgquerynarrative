package service

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/internal/errors"
)

func TestClassifyRunError_SanitizesDBErrors(t *testing.T) {
	kind, msg := ClassifyRunError(errors.New(`ERROR: relation "secret" does not exist (SQLSTATE 42P01)`))
	if kind != RunErrorValidation {
		t.Fatalf("kind = %v, want validation", kind)
	}
	if msg == `ERROR: relation "secret" does not exist (SQLSTATE 42P01)` {
		t.Fatalf("raw DB error leaked: %q", msg)
	}
}

func TestClassifyRunError_KnownValidationErrors(t *testing.T) {
	_, msg := ClassifyRunError(apperrors.ErrUnqualifiedTable)
	if msg != apperrors.ErrUnqualifiedTable.Error() {
		t.Fatalf("msg = %q", msg)
	}
}

func TestClassifyRunError_SyntaxError(t *testing.T) {
	kind, msg := ClassifyRunError(apperrors.ErrSyntaxError)
	if kind != RunErrorValidation {
		t.Fatalf("kind = %v, want validation", kind)
	}
	if msg != apperrors.ErrSyntaxError.Error() {
		t.Fatalf("msg = %q, want the syntax-error sentinel, not a generic/policy message", msg)
	}
}

// Bug: ClassifyRunError started returning PostgreSQL's SQLSTATE and up to
// 300 bytes of the raw server message for an executionFailed-wrapped error.
// It is shared by every caller in the product (ask, queries, queries_compare,
// reports_generate, schedules, the PostgREST investigation path), so that
// widened every one of them at once. Only the PostgREST replay-role grant
// check wants that detail, and it must get it by calling
// executionFailedMessage directly, never through this shared classifier.
func TestClassifyRunError_ExecutionFailedStaysGenericEvenWithSQLSTATEDetail(t *testing.T) {
	wrapped := fmt.Errorf("%w: SQLSTATE 42501: permission denied to set role \"secret_role\"", apperrors.ErrQueryExecutionFailed)
	kind, msg := ClassifyRunError(wrapped)
	if kind != RunErrorValidation {
		t.Fatalf("kind = %v, want validation", kind)
	}
	if msg != "Query execution failed. Check your SQL and try again." {
		t.Fatalf("ClassifyRunError leaked PostgreSQL detail to a shared caller: %q", msg)
	}
	// executionFailedMessage itself must still produce the detail — it has
	// exactly one legitimate caller left (investigations_postgrest.go's
	// postgrestReplay), which calls it directly, not through this classifier.
	if detail := executionFailedMessage(wrapped); !strings.Contains(detail, "SQLSTATE 42501") {
		t.Fatalf("executionFailedMessage must still carry SQLSTATE detail for its one direct caller, got %q", detail)
	}
}
