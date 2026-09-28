package catalog_test

import (
	"context"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/internal/catalog"
)

func TestLoad_EmptyAllowedSchemas_ReturnsEmpty(t *testing.T) {
	loader := catalog.NewLoader(nil, nil)
	ctx := context.Background()

	res, err := loader.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res == nil || len(res.Schemas) != 0 {
		t.Errorf("expected empty schemas, got %v", res)
	}
}

func TestLoad_EmptyAllowedSchemasSlice_ReturnsEmpty(t *testing.T) {
	loader := catalog.NewLoader(nil, []string{})
	ctx := context.Background()

	res, err := loader.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res == nil || len(res.Schemas) != 0 {
		t.Errorf("expected empty schemas, got %v", res)
	}
}
