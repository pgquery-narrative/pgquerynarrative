// Package queryrunner provides SQL query validation to ensure queries are safe
// and read-only. It prevents SQL injection and enforces security policies.
package queryrunner

import (
	"encoding/json"
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// Validator validates SQL queries to ensure they are safe to execute.
// It enforces:
//   - Read-only queries (SELECT/WITH, or EXPLAIN of a SELECT/WITH)
//   - Single statement execution
//   - Allowed schemas only
//   - No dangerous statement types in the parse tree
type Validator struct {
	allowedSchemas map[string]bool // Set of allowed schema names (lowercase)
	maxQueryLength int             // Maximum query length in bytes
}

// NewValidator creates a new query validator with the specified configuration.
//
// Parameters:
//   - allowedSchemas: List of schema names that queries are allowed to access
//   - maxQueryLength: Maximum query length in bytes (prevents DoS)
//
// Returns a configured Validator instance.
func NewValidator(allowedSchemas []string, maxQueryLength int) *Validator {
	// Build schema map for O(1) lookup
	schemaMap := make(map[string]bool, len(allowedSchemas))
	for _, schema := range allowedSchemas {
		schemaMap[strings.ToLower(schema)] = true
	}

	return &Validator{
		allowedSchemas: schemaMap,
		maxQueryLength: maxQueryLength,
	}
}

// Validate checks if a SQL query is safe to execute.
type parseTree struct {
	Stmts []struct {
		Stmt map[string]interface{} `json:"stmt"`
	} `json:"stmts"`
}

var disallowedASTNodes = map[string]struct{}{
	"InsertStmt":        {},
	"UpdateStmt":        {},
	"DeleteStmt":        {},
	"MergeStmt":         {},
	"TruncateStmt":      {},
	"CopyStmt":          {},
	"DoStmt":            {},
	"CallStmt":          {},
	"ExecuteStmt":       {},
	"DeclareCursorStmt": {},
	"CreateStmt":        {},
	"CreateTableAsStmt": {},
	"DropStmt":          {},
	"AlterTableStmt":    {},
	"IndexStmt":         {},
	"ViewStmt":          {},
	"GrantStmt":         {},
	"VacuumStmt":        {},
}

// Validation rules:
//   - Query length must not exceed maxQueryLength
//   - Must be a single statement
//   - Must be a top-level SELECT/WITH SELECT, or EXPLAIN of one
//   - Must reject write/unsafe statements in nested CTE/subquery contexts
//   - Must only reference allowed schemas
//
// Parameters:
//   - sql: SQL query string to validate
//
// Returns:
//   - nil if query is valid
//   - Error describing why the query is invalid
func (v *Validator) Validate(sql string) error {
	// Check query length
	if len(sql) > v.maxQueryLength {
		return errors.ErrQueryTooLong
	}

	// Normalize query: trim whitespace
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return errors.ErrOnlySelectAllowed
	}

	treeJSON, err := pg_query.ParseToJSON(trimmed)
	if err != nil {
		return errors.ErrOnlySelectAllowed
	}

	var tree parseTree
	if err := json.Unmarshal([]byte(treeJSON), &tree); err != nil {
		return errors.ErrOnlySelectAllowed
	}

	if len(tree.Stmts) != 1 {
		return errors.ErrMultipleStatements
	}
	if len(tree.Stmts[0].Stmt) == 0 {
		return errors.ErrOnlySelectAllowed
	}

	readOnlyQuery, err := extractReadOnlyQuery(tree.Stmts[0].Stmt)
	if err != nil {
		return err
	}

	// Reject writes/unsafe statements that can be nested inside CTEs/subqueries.
	if containsDisallowedNodes(readOnlyQuery) {
		return errors.ErrDisallowedKeyword
	}

	// Check schema access (if schema restrictions are configured)
	if len(v.allowedSchemas) > 0 {
		cteNames := collectCTENames(readOnlyQuery)
		if hasUnqualifiedTables(readOnlyQuery, cteNames) {
			return errors.ErrUnqualifiedTable
		}
		for _, schemaName := range collectSchemaNames(readOnlyQuery) {
			if !v.allowedSchemas[schemaName] {
				return errors.ErrSchemaNotAllowed
			}
		}
	}

	return nil
}

// extractReadOnlyQuery returns the SelectStmt subtree to validate.
// Top-level SELECT/WITH queries and EXPLAIN (including FORMAT JSON) of a SELECT are allowed.
func extractReadOnlyQuery(rootStmt map[string]interface{}) (interface{}, error) {
	if rootSelect, ok := rootStmt["SelectStmt"]; ok {
		return rootSelect, nil
	}

	explain, ok := rootStmt["ExplainStmt"]
	if !ok {
		return nil, errors.ErrOnlySelectAllowed
	}

	explainMap, ok := explain.(map[string]interface{})
	if !ok {
		return nil, errors.ErrOnlySelectAllowed
	}

	query, ok := explainMap["query"].(map[string]interface{})
	if !ok || len(query) == 0 {
		return nil, errors.ErrOnlySelectAllowed
	}

	rootSelect, ok := query["SelectStmt"]
	if !ok {
		return nil, errors.ErrOnlySelectAllowed
	}

	return rootSelect, nil
}

func containsDisallowedNodes(node interface{}) bool {
	switch n := node.(type) {
	case map[string]interface{}:
		for key, value := range n {
			if _, blocked := disallowedASTNodes[key]; blocked {
				return true
			}
			if containsDisallowedNodes(value) {
				return true
			}
		}
	case []interface{}:
		for _, item := range n {
			if containsDisallowedNodes(item) {
				return true
			}
		}
	}
	return false
}

func collectSchemaNamesInto(node interface{}, out map[string]struct{}) {
	switch n := node.(type) {
	case map[string]interface{}:
		for key, value := range n {
			if key == "RangeVar" {
				if rangeVar, ok := value.(map[string]interface{}); ok {
					if schemaVal, ok := rangeVar["schemaname"].(string); ok {
						schema := strings.ToLower(strings.TrimSpace(schemaVal))
						if schema != "" {
							out[schema] = struct{}{}
						}
					}
				}
				continue
			}
			collectSchemaNamesInto(value, out)
		}
	case []interface{}:
		for _, item := range n {
			collectSchemaNamesInto(item, out)
		}
	}
}

func collectSchemaNames(node interface{}) []string {
	schemaSet := make(map[string]struct{})
	collectSchemaNamesInto(node, schemaSet)

	out := make([]string, 0, len(schemaSet))
	for schema := range schemaSet {
		out = append(out, schema)
	}
	return out
}

func hasUnqualifiedTables(node interface{}, cteNames map[string]struct{}) bool {
	found := false
	collectUnqualifiedTables(node, cteNames, &found)
	return found
}

func collectCTENames(node interface{}) map[string]struct{} {
	out := make(map[string]struct{})
	collectCTENamesInto(node, out)
	return out
}

func collectCTENamesInto(node interface{}, out map[string]struct{}) {
	switch n := node.(type) {
	case map[string]interface{}:
		for key, value := range n {
			if key == "CommonTableExpr" {
				if cte, ok := value.(map[string]interface{}); ok {
					if name, ok := cte["ctename"].(string); ok {
						out[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
					}
				}
			}
			collectCTENamesInto(value, out)
		}
	case []interface{}:
		for _, item := range n {
			collectCTENamesInto(item, out)
		}
	}
}

func collectUnqualifiedTables(node interface{}, cteNames map[string]struct{}, found *bool) {
	if *found {
		return
	}
	switch n := node.(type) {
	case map[string]interface{}:
		for key, value := range n {
			if key == "RangeVar" {
				if rangeVar, ok := value.(map[string]interface{}); ok {
					schemaVal, _ := rangeVar["schemaname"].(string)
					if strings.TrimSpace(schemaVal) == "" {
						relName, _ := rangeVar["relname"].(string)
						if _, isCTE := cteNames[strings.ToLower(strings.TrimSpace(relName))]; isCTE {
							continue
						}
						*found = true
						return
					}
				}
				continue
			}
			collectUnqualifiedTables(value, cteNames, found)
		}
	case []interface{}:
		for _, item := range n {
			collectUnqualifiedTables(item, cteNames, found)
		}
	}
}
