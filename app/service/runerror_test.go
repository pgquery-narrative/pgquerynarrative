package service

import (
	"errors"
	"testing"

	apperrors "github.com/pgquerynarrative/pgquerynarrative/app/errors"
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
