// Package catalog provides read-only database schema discovery from
// PostgreSQL information_schema for allowed schemas (e.g. demo).
package catalog

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"

	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
)

// Loader loads schema metadata from the database using the read-only pool.
// Only tables and views in allowed schemas are returned (information_schema.columns
// includes both), so the result matches what the query validator permits.
type Loader struct {
	pool           *pgxpool.Pool
	poolResolver   poolResolver
	connectionID   string
	allowedSchemas []string
}

type poolResolver interface {
	ReadOnly(ctx context.Context, connectionID string) *pgxpool.Pool
}

// NewLoader creates a catalog loader that queries information_schema
// and returns only the given allowed schema names (e.g. []string{"demo"}).
func NewLoader(pool *pgxpool.Pool, allowedSchemas []string) *Loader {
	return &Loader{pool: pool, allowedSchemas: allowedSchemas}
}

// NewLoaderForConnection resolves the read-only pool lazily when loading schema.
func NewLoaderForConnection(resolver poolResolver, connectionID string, allowedSchemas []string) *Loader {
	return &Loader{poolResolver: resolver, connectionID: connectionID, allowedSchemas: allowedSchemas}
}

func (l *Loader) activePool(ctx context.Context) *pgxpool.Pool {
	if l.pool != nil {
		return l.pool
	}
	if l.poolResolver != nil {
		if ctx == nil {
			ctx = context.Background()
		}
		return l.poolResolver.ReadOnly(ctx, l.connectionID)
	}
	return nil
}

// infoSchemaColumns lists columns from information_schema for allowed schemas.
// Uses ANY($1) so schema names are parameterized (no string interpolation).
const infoSchemaColumns = `
	SELECT table_schema, table_name, column_name, data_type, ordinal_position
	FROM information_schema.columns
	WHERE table_schema = ANY($1)
	  AND table_catalog = current_database()
	ORDER BY table_schema, table_name, ordinal_position
`

// Load returns the list of allowed schemas with their tables, views, and columns.
// It uses the read-only pool so only objects visible to that user are included.
// Views in allowed schemas (e.g. demo.sales_summary) are included automatically.
func (l *Loader) Load(ctx context.Context) (*schema.SchemaResult, error) {
	if len(l.allowedSchemas) == 0 {
		return &schema.SchemaResult{Schemas: []*schema.SchemaInfo{}}, nil
	}
	pool := l.activePool(ctx)
	if pool == nil {
		return nil, fmt.Errorf("read-only pool unavailable")
	}

	rows, err := pool.Query(ctx, infoSchemaColumns, l.allowedSchemas)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	raw, err := scanColumnRows(rows)
	if err != nil {
		return nil, err
	}
	return buildSchemaResult(l.allowedSchemas, raw), nil
}

type columnRow struct {
	tableSchema string
	tableName   string
	columnName  string
	dataType    string
	ordPosition int32
}

func scanColumnRows(rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}) ([]columnRow, error) {
	var out []columnRow
	for rows.Next() {
		var r columnRow
		if err := rows.Scan(&r.tableSchema, &r.tableName, &r.columnName, &r.dataType, &r.ordPosition); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// buildSchemaResult groups column rows by schema and table, preserving allowedSchemas order.
func buildSchemaResult(allowedSchemas []string, raw []columnRow) *schema.SchemaResult {
	schemaMap := make(map[string]map[string][]*schema.ColumnInfo)
	for _, r := range raw {
		if schemaMap[r.tableSchema] == nil {
			schemaMap[r.tableSchema] = make(map[string][]*schema.ColumnInfo)
		}
		tables := schemaMap[r.tableSchema]
		tables[r.tableName] = append(tables[r.tableName], &schema.ColumnInfo{Name: r.columnName, Type: r.dataType})
	}

	var schemas []*schema.SchemaInfo
	for _, name := range allowedSchemas {
		tablesMap, ok := schemaMap[name]
		if !ok {
			schemas = append(schemas, &schema.SchemaInfo{Name: name, Tables: []*schema.TableInfo{}})
			continue
		}
		var tables []*schema.TableInfo
		for tableName, cols := range tablesMap {
			tables = append(tables, &schema.TableInfo{Name: tableName, Columns: cols})
		}
		sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
		schemas = append(schemas, &schema.SchemaInfo{Name: name, Tables: tables})
	}
	return &schema.SchemaResult{Schemas: schemas}
}
