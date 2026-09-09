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
	enforceSchemas bool            // When true, schema checks run even if the allowlist is empty
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
		enforceSchemas: len(allowedSchemas) > 0,
	}
}

// ValidateForSchemas clones the validator's non-schema settings for a request-
// specific schema allowlist and returns the derived validator.
// Schema enforcement is always enabled for request overrides so an empty tenant
// allowlist cannot silently disable checks.
func (v *Validator) ValidateForSchemas(allowedSchemas []string) *Validator {
	var maxLen int
	if v != nil {
		maxLen = v.maxQueryLength
	}
	derived := NewValidator(allowedSchemas, maxLen)
	derived.enforceSchemas = true
	return derived
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

	// SELECT ... INTO creates a table. Its parse-tree representation is a field
	// key, not a node type, so it needs its own check.
	if hasIntoClause(readOnlyQuery) {
		return errors.ErrSelectIntoNotAllowed
	}

	// FOR UPDATE / FOR SHARE take row locks and are not read-only.
	if _, locked := findFirstByKey(readOnlyQuery, "LockingClause"); locked {
		return errors.ErrLockingClauseNotAllowed
	}

	// The schema allowlist below governs table references. Apply the function
	// policy too, so a disallowed schema cannot be reached through a FuncCall
	// and side-effecting functions are rejected inside the read-only wrapper.
	if err := v.checkFunctionPolicy(readOnlyQuery); err != nil {
		return err
	}

	// Check schema access when schema restrictions are configured or explicitly enforced.
	if v.enforceSchemas || len(v.allowedSchemas) > 0 {
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

	if err := validateExplainOptionNodes(explainMap["options"]); err != nil {
		return nil, err
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

// validateExplainOptionNodes rejects user-supplied EXPLAIN options in the JSON
// parse tree. Only FORMAT JSON is tolerated; ANALYZE, BUFFERS, VERBOSE and
// non-JSON formats must be requested through the explain endpoint so server
// policy (e.g. SECURITY_EXPLAIN_ANALYZE_ENABLED) cannot be bypassed.
func validateExplainOptionNodes(options interface{}) error {
	if options == nil {
		return nil
	}
	list, ok := options.([]interface{})
	if !ok {
		return errors.ErrExplainOptionsNotAllowed
	}
	for _, item := range list {
		wrapper, ok := item.(map[string]interface{})
		if !ok {
			return errors.ErrExplainOptionsNotAllowed
		}
		def, ok := wrapper["DefElem"].(map[string]interface{})
		if !ok {
			return errors.ErrExplainOptionsNotAllowed
		}
		name, _ := def["defname"].(string)
		if !strings.EqualFold(name, "format") {
			return errors.ErrExplainOptionsNotAllowed
		}
		if !strings.EqualFold(defElemJSONStringValue(def), "json") {
			return errors.ErrExplainOptionsNotAllowed
		}
	}
	return nil
}

func defElemJSONStringValue(def map[string]interface{}) string {
	arg, ok := def["arg"].(map[string]interface{})
	if !ok {
		return ""
	}
	str, ok := arg["String"].(map[string]interface{})
	if !ok {
		return ""
	}
	val, _ := str["sval"].(string)
	return val
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

// findFirstByKey returns the first value stored under key anywhere in the parse
// tree. It serves both kinds of lookup this validator needs: node-type keys
// (a "LockingClause" wrapper) and plain field keys ("intoClause"), which node
// type names alone cannot express.
func findFirstByKey(node interface{}, want string) (interface{}, bool) {
	switch n := node.(type) {
	case map[string]interface{}:
		if v, ok := n[want]; ok {
			return v, true
		}
		for _, value := range n {
			if v, ok := findFirstByKey(value, want); ok {
				return v, true
			}
		}
	case []interface{}:
		for _, item := range n {
			if v, ok := findFirstByKey(item, want); ok {
				return v, true
			}
		}
	}
	return nil, false
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

// deniedFunctions are rejected by bare name regardless of the schema they
// resolve in. A read-only transaction blocks writes, but it does not make a
// SELECT side-effect free — these run fine inside one:
//
//   - advisory locks are *session* scoped, so they outlive the transaction and
//     poison the pooled connection they were taken on;
//   - set_config mutates session GUCs on that same pooled connection;
//   - pg_sleep holds a connection for its whole duration;
//   - server-side file and large-object access reads outside the database;
//   - backend/replication control affects the whole server;
//   - dblink and the query_to_xml family execute a *second*, unvalidated query.
//
// Sequence and large-object writers are listed for an explicit, named error
// even though the read-only transaction would also reject them.
var deniedFunctions = map[string]struct{}{
	// Session-scoped advisory locks (survive COMMIT on a pooled connection).
	"pg_advisory_lock": {}, "pg_advisory_lock_shared": {},
	"pg_advisory_unlock": {}, "pg_advisory_unlock_all": {},
	"pg_advisory_unlock_shared": {},
	"pg_advisory_xact_lock":     {}, "pg_advisory_xact_lock_shared": {},
	"pg_try_advisory_lock": {}, "pg_try_advisory_lock_shared": {},
	"pg_try_advisory_xact_lock": {}, "pg_try_advisory_xact_lock_shared": {},

	// Session/transaction state mutation.
	"set_config": {},

	// Connection-holding.
	"pg_sleep": {}, "pg_sleep_for": {}, "pg_sleep_until": {},

	// Server-side file access.
	"pg_read_file": {}, "pg_read_binary_file": {},
	"pg_ls_dir": {}, "pg_stat_file": {},
	"pg_ls_logdir": {}, "pg_ls_waldir": {}, "pg_ls_tmpdir": {},
	"pg_ls_archive_statusdir": {},

	// Large objects (server-side file I/O and writes).
	"lo_import": {}, "lo_export": {}, "lo_create": {}, "lo_unlink": {},
	"lo_put": {}, "lo_from_bytea": {}, "lo_open": {}, "lo_write": {},
	"lowrite": {}, "loread": {},

	// Sequence mutation.
	"nextval": {}, "setval": {},

	// Backend, WAL and replication control.
	"pg_terminate_backend": {}, "pg_cancel_backend": {},
	"pg_reload_conf": {}, "pg_rotate_logfile": {},
	"pg_switch_wal": {}, "pg_create_restore_point": {}, "pg_promote": {},
	"pg_start_backup": {}, "pg_stop_backup": {},
	"pg_backup_start": {}, "pg_backup_stop": {},
	"pg_create_physical_replication_slot": {},
	"pg_create_logical_replication_slot":  {},
	"pg_copy_physical_replication_slot":   {},
	"pg_copy_logical_replication_slot":    {},
	"pg_drop_replication_slot":            {},
	"pg_logical_slot_get_changes":         {},
	"pg_logical_slot_peek_changes":        {},
	"pg_logical_emit_message":             {},
	"pg_replication_origin_create":        {},
	"pg_replication_origin_drop":          {},
	"pg_replication_origin_session_setup": {},
	"pg_replication_origin_session_reset": {},
	"pg_replication_origin_xact_setup":    {},
	"pg_replication_origin_advance":       {},
	"pg_wal_replay_pause":                 {},
	"pg_wal_replay_resume":                {},

	// Asynchronous notification (observable outside the transaction).
	"pg_notify": {},

	// Execute a second, unvalidated query string.
	"query_to_xml": {}, "query_to_xmlschema": {},
	"query_to_xml_and_xmlschema": {},
	"table_to_xml":               {}, "table_to_xmlschema": {},
	"table_to_xml_and_xmlschema": {},
	"cursor_to_xml":              {}, "cursor_to_xmlschema": {},

	// Reach external systems.
	"dblink": {}, "dblink_exec": {}, "dblink_connect": {},
	"dblink_connect_u": {}, "dblink_send_query": {},
	"dblink_open": {}, "dblink_fetch": {},

	// Statistics reset.
	"pg_stat_reset": {}, "pg_stat_reset_shared": {},
	"pg_stat_reset_single_table_counters":    {},
	"pg_stat_reset_single_function_counters": {},
	"pg_stat_statements_reset":               {},
}

// functionSchemaAlwaysAllowed are the read-only system catalogs whose functions
// stay callable regardless of the tenant schema allowlist. Anything they expose
// that is *not* read-only is caught by deniedFunctions above.
var functionSchemaAlwaysAllowed = map[string]bool{
	"pg_catalog":         true,
	"information_schema": true,
}

// checkFunctionPolicy walks every FuncCall in the tree and applies two rules:
//
//  1. the bare function name must not be in deniedFunctions, whatever schema it
//     is written with — pg_catalog.pg_advisory_lock(1) is still an advisory lock;
//  2. a schema-qualified call must target an allowed schema (or a read-only
//     system catalog), mirroring the RangeVar rule for tables.
//
// Unqualified names are deliberately NOT required to be schema-qualified the way
// tables are: count(), now() and coalesce() are unqualified in almost every real
// query. They resolve through the analytical role's pinned search_path, which is
// itself restricted to the allowed schemas.
func (v *Validator) checkFunctionPolicy(node interface{}) error {
	var firstErr error
	walkFuncCalls(node, func(schema, name string) bool {
		if _, denied := deniedFunctions[name]; denied {
			firstErr = errors.ErrFunctionNotAllowed
			return false
		}
		if schema == "" || functionSchemaAlwaysAllowed[schema] {
			return true
		}
		if v.enforceSchemas || len(v.allowedSchemas) > 0 {
			if !v.allowedSchemas[schema] {
				firstErr = errors.ErrFunctionSchemaNotAllowed
				return false
			}
		}
		return true
	})
	return firstErr
}

// walkFuncCalls invokes fn(schema, name) for every FuncCall node, with both
// parts lowercased; schema is "" for an unqualified call. fn returns false to
// stop the walk, so a rejected query does not pay to traverse the rest of its
// tree — this runs on the read path in front of every user SELECT.
func walkFuncCalls(node interface{}, fn func(schema, name string) bool) bool {
	switch n := node.(type) {
	case map[string]interface{}:
		for key, value := range n {
			if key == "FuncCall" {
				if call, ok := value.(map[string]interface{}); ok {
					schema, name := funcCallName(call)
					if name != "" && !fn(schema, name) {
						return false
					}
				}
			}
			if !walkFuncCalls(value, fn) {
				return false
			}
		}
	case []interface{}:
		for _, item := range n {
			if !walkFuncCalls(item, fn) {
				return false
			}
		}
	}
	return true
}

// funcCallName extracts (schema, name) from a FuncCall's funcname list. The list
// holds String nodes: ["count"] unqualified, ["pg_catalog","count"] qualified.
// A longer list (database.schema.function) uses the last two parts.
func funcCallName(call map[string]interface{}) (schema, name string) {
	parts, ok := call["funcname"].([]interface{})
	if !ok || len(parts) == 0 {
		return "", ""
	}
	names := make([]string, 0, len(parts))
	for _, p := range parts {
		wrapper, ok := p.(map[string]interface{})
		if !ok {
			continue
		}
		str, ok := wrapper["String"].(map[string]interface{})
		if !ok {
			continue
		}
		sval, _ := str["sval"].(string)
		names = append(names, strings.ToLower(strings.TrimSpace(sval)))
	}
	if len(names) == 0 {
		return "", ""
	}
	name = names[len(names)-1]
	if len(names) >= 2 {
		schema = names[len(names)-2]
	}
	return schema, name
}

// hasIntoClause reports whether the SELECT carries an INTO target. SELECT INTO
// creates a table; the read-only transaction would also reject it, but the
// validator claims to enforce read-only semantics and should say so first.
// "intoClause" is a field key in the parse tree, not a node-type key, so it
// cannot be expressed in disallowedASTNodes.
func hasIntoClause(node interface{}) bool {
	v, ok := findFirstByKey(node, "intoClause")
	if !ok {
		return false
	}
	m, isMap := v.(map[string]interface{})
	return isMap && len(m) > 0
}
