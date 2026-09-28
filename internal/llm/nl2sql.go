package llm

import (
	"regexp"
	"strings"

	schema "github.com/pgquerynarrative/pgquerynarrative/api/gen/schema"
)

// BuildNL2SQLPrompt builds a prompt that asks the LLM to generate a single
// SELECT statement for the given natural-language question, using only the
// provided schema. The schema text should describe allowed tables and columns
// (e.g. from FormatSchemaForPrompt).
func BuildNL2SQLPrompt(question, schemaText string) string {
	return FlattenMessages(BuildNL2SQLMessages(question, schemaText))
}

// BuildNL2SQLMessages separates system SQL rules from untrusted schema/question data.
func BuildNL2SQLMessages(question, schemaText string) []ChatMessage {
	system := strings.Join([]string{
		"You are a SQL expert. The database is PostgreSQL. Use PostgreSQL syntax only.",
		"RULES:",
		"- Use only the tables and columns in the schema below. Do not use other tables or invent columns.",
		"- Prefer parent tables (e.g. demo.sales) over partition or default partition tables.",
		"- Query demo.sales for sales data; do not use sales_YYYY_MM or sales_default.",
		"- For \"top N\" or \"first N\" rows use ORDER BY ... LIMIT N at the end (e.g. LIMIT 5). Do NOT use TOP N or FETCH FIRST.",
		"- Use single quotes for string literals. Use double quotes only for identifiers if needed.",
		"- Dates: use CURRENT_DATE, date_trunc('month', date), INTERVAL '30 days', etc.",
		"- Respond with ONLY the SQL statement. No explanation, no markdown code fences, no extra text. Single SELECT (or WITH ... SELECT) only.",
	}, "\n")
	user := "SCHEMA:\n" + wrapUntrusted("SCHEMA", schemaText) + "\n\nQUESTION:\n" + wrapUntrusted("QUESTION", question)
	return []ChatMessage{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
}

// FormatSchemaForPrompt turns a schema result into a short text description
// suitable for the NL→SQL prompt (e.g. "demo.sales: id (uuid), date (date), ...").
func FormatSchemaForPrompt(sr *schema.SchemaResult) string {
	if sr == nil || len(sr.Schemas) == 0 {
		return "(no schema)"
	}
	var parts []string
	for _, s := range sr.Schemas {
		if s == nil {
			continue
		}
		for _, t := range s.Tables {
			if t == nil {
				continue
			}
			tableRef := s.Name + "." + t.Name
			var cols []string
			for _, c := range t.Columns {
				if c == nil {
					continue
				}
				cols = append(cols, c.Name+" ("+c.Type+")")
			}
			parts = append(parts, tableRef+": "+strings.Join(cols, ", "))
		}
	}
	return strings.Join(parts, "\n")
}

// FormatSchemaForNL2SQL is like FormatSchemaForPrompt but omits monthly partition
// child tables when the parent table is present (e.g. demo.sales vs sales_2024_01).
func FormatSchemaForNL2SQL(sr *schema.SchemaResult) string {
	return FormatSchemaForPrompt(filterSchemaForNL2SQL(sr))
}

func filterSchemaForNL2SQL(sr *schema.SchemaResult) *schema.SchemaResult {
	if sr == nil {
		return sr
	}
	out := &schema.SchemaResult{Schemas: make([]*schema.SchemaInfo, 0, len(sr.Schemas))}
	for _, s := range sr.Schemas {
		if s == nil {
			continue
		}
		tableNames := make(map[string]struct{}, len(s.Tables))
		for _, t := range s.Tables {
			if t != nil {
				tableNames[t.Name] = struct{}{}
			}
		}
		filtered := &schema.SchemaInfo{Name: s.Name, Tables: make([]*schema.TableInfo, 0, len(s.Tables))}
		for _, t := range s.Tables {
			if t == nil || shouldOmitFromNL2SQL(t.Name, tableNames) {
				continue
			}
			filtered.Tables = append(filtered.Tables, t)
		}
		out.Schemas = append(out.Schemas, filtered)
	}
	return out
}

func shouldOmitFromNL2SQL(tableName string, tables map[string]struct{}) bool {
	if _, hasParent := tables["sales"]; !hasParent {
		return false
	}
	if tableName == "sales_default" {
		return true
	}
	return isMonthlyPartitionChild(tableName, tables)
}

func isMonthlyPartitionChild(tableName string, tables map[string]struct{}) bool {
	m := partitionChildRegex.FindStringSubmatch(tableName)
	if m == nil {
		return false
	}
	_, ok := tables[m[1]]
	return ok
}

var (
	// Match ```sql ... ``` or ``` ... ```
	sqlBlockRegex = regexp.MustCompile(`(?s)\x60\x60\x60(?:sql)?\s*(.*?)\s*\x60\x60\x60`)
	// sales_2024_03 style partition names when demo.sales exists.
	partitionChildRegex = regexp.MustCompile(`^(.+)_\d{4}_\d{2}$`)
	sqlStartRegex       = regexp.MustCompile(`(?is)\b(WITH\b|SELECT\b)`)
)

// BuildExplainPrompt builds a short prompt asking the LLM to explain the given
// SQL in one or two plain-English sentences.
func BuildExplainPrompt(sql string) string {
	return FlattenMessages([]ChatMessage{
		{Role: "system", Content: "Explain SQL queries in one or two short sentences in plain English. Describe what data it returns or what it computes. Do not include code or markdown."},
		{Role: "user", Content: "SQL:\n" + wrapUntrusted("SQL_QUERY", sql)},
	})
}

// ParseSQLFromResponse extracts a single SQL statement from the LLM response.
// It strips markdown code fences (```sql or ```) and trims whitespace. If no
// code block is found, the whole response is trimmed and returned. Returns
// the empty string if the result would be blank.
func ParseSQLFromResponse(response string) string {
	trimmed := strings.TrimSpace(response)
	if trimmed == "" {
		return ""
	}
	if m := sqlBlockRegex.FindStringSubmatch(trimmed); len(m) > 1 {
		trimmed = strings.TrimSpace(m[1])
	}
	trimmed = extractSQLStatement(trimmed)
	return strings.TrimSpace(trimmed)
}

func extractSQLStatement(s string) string {
	if s == "" {
		return ""
	}
	loc := sqlStartRegex.FindStringIndex(s)
	if loc == nil {
		return s
	}
	stmt := strings.TrimSpace(s[loc[0]:])
	if idx := strings.Index(stmt, ";"); idx >= 0 {
		stmt = strings.TrimSpace(stmt[:idx])
	}
	return stmt
}
