// Package service provides business logic for queries, reports, and schema.
package service

import (
	"context"

	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"

	"github.com/pgquerynarrative/pgquerynarrative/app/catalog"
)

// SchemaService implements the schema service API (catalog discovery).
type SchemaService struct {
	loader *catalog.Loader
	connectionResolver
}

// NewSchemaService creates a schema service that returns allowed schemas,
// tables, and columns from the database (read-only pool).
func NewSchemaService(loader *catalog.Loader) *SchemaService {
	return &SchemaService{
		loader:             loader,
		connectionResolver: newConnectionResolver("default", nil, map[string]*catalog.Loader{"default": loader}, nil),
	}
}

// NewSchemaServiceMultiConnection creates schema service with one catalog loader per connection.
func NewSchemaServiceMultiConnection(loaders map[string]*catalog.Loader, defaultConnectionID string) *SchemaService {
	var defaultLoader *catalog.Loader
	if l, ok := loaders[defaultConnectionID]; ok {
		defaultLoader = l
	} else {
		for _, l := range loaders {
			defaultLoader = l
			break
		}
	}
	return &SchemaService{
		loader:             defaultLoader,
		connectionResolver: newConnectionResolver(defaultConnectionID, nil, loaders, nil),
	}
}

// Get returns the list of allowed schemas with their tables and columns.
func (s *SchemaService) Get(ctx context.Context, payload *schema.GetPayload) (*schema.SchemaResult, error) {
	return s.connectionResolver.loaderFor(payload.ConnectionID).Load(ctx)
}
