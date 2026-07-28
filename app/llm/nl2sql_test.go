package llm

import (
	"strings"
	"testing"

	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
)

func TestParseSQLFromResponse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain select",
			in:   "SELECT 1 FROM demo.sales",
			want: "SELECT 1 FROM demo.sales",
		},
		{
			name: "markdown fence",
			in:   "```sql\nSELECT 1 FROM demo.sales\n```",
			want: "SELECT 1 FROM demo.sales",
		},
		{
			name: "prose prefix",
			in:   "Here is the query:\nSELECT product_category FROM demo.sales",
			want: "SELECT product_category FROM demo.sales",
		},
		{
			name: "strip trailing semicolon statement",
			in:   "SELECT 1; SELECT 2",
			want: "SELECT 1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseSQLFromResponse(tt.in)
			if got != tt.want {
				t.Fatalf("ParseSQLFromResponse() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFormatSchemaForNL2SQL_omitsPartitionChildren(t *testing.T) {
	sr := &schema.SchemaResult{
		Schemas: []*schema.SchemaInfo{{
			Name: "demo",
			Tables: []*schema.TableInfo{
				{Name: "sales", Columns: []*schema.ColumnInfo{{Name: "id", Type: "uuid"}}},
				{Name: "sales_2024_01", Columns: []*schema.ColumnInfo{{Name: "id", Type: "uuid"}}},
				{Name: "sales_default", Columns: []*schema.ColumnInfo{{Name: "id", Type: "uuid"}}},
				{Name: "customers", Columns: []*schema.ColumnInfo{{Name: "id", Type: "uuid"}}},
			},
		}},
	}
	out := FormatSchemaForNL2SQL(sr)
	if strings.Contains(out, "sales_2024_01") {
		t.Fatalf("partition child should be omitted: %q", out)
	}
	if !strings.Contains(out, "demo.sales:") {
		t.Fatalf("parent table should remain: %q", out)
	}
	if !strings.Contains(out, "demo.customers:") {
		t.Fatalf("non-partition table should remain: %q", out)
	}
	if strings.Contains(out, "sales_default") {
		t.Fatalf("default partition should be omitted: %q", out)
	}
}
