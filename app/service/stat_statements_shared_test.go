package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/api/gen/queries"
)

func TestIsStatStatementsSharedError(t *testing.T) {
	shared := &queries.ValidationError{Name: "validation_error", Message: "x", Code: strPtr(statStatementsSharedCode)}
	if !isStatStatementsSharedError(shared) || !isStatStatementsSharedError(fmt.Errorf("wrapped: %w", shared)) {
		t.Error("the shared-role refusal must be recognised, also when wrapped")
	}
	for name, err := range map[string]error{
		"nil":                nil,
		"plain error":        errors.New("boom"),
		"other code":         &queries.ValidationError{Name: "validation_error", Message: "x", Code: strPtr("STAT_STATEMENTS_UNAVAILABLE")},
		"validation no code": &queries.ValidationError{Name: "validation_error", Message: "x"},
	} {
		if isStatStatementsSharedError(err) {
			t.Errorf("%s must not be treated as the shared-role refusal", name)
		}
	}
}
