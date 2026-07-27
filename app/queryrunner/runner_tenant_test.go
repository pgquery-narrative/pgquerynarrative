package queryrunner

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/auth"
)

func TestValidateForSchemasEmptyAllowlistStillEnforces(t *testing.T) {
	base := NewValidator([]string{"demo"}, 10000)
	v := base.ValidateForSchemas(nil)
	if err := v.Validate(`SELECT * FROM demo.sales`); err == nil {
		t.Fatal("empty tenant allowlist must reject previously allowed schemas")
	}
}

func TestRunnerValidateQueryUsesRequestAwareSchemas(t *testing.T) {
	validator := NewValidator([]string{"demo"}, 10000)
	r := &Runner{
		connectionID:   "default",
		validator:      validator,
		schemaResolver: stubRunnerSchemaResolver{schemas: []string{"tenant_demo"}},
	}
	ctx := auth.WithPrincipal(context.Background(), auth.Principal{OrgID: "org-1"})
	if err := r.ValidateQueryWithContext(ctx, `SELECT * FROM tenant_demo.sales`); err != nil {
		t.Fatalf("expected tenant schema to validate, got %v", err)
	}
	if err := r.ValidateQueryWithContext(ctx, `SELECT * FROM demo.sales`); err == nil {
		t.Fatal("expected static schema to be rejected when tenant override is active")
	}
}

type stubRunnerSchemaResolver struct {
	schemas []string
}

func (s stubRunnerSchemaResolver) AllowedSchemas(context.Context, string) []string {
	return s.schemas
}
