package queryrunner

import (
	"strings"

	pg_query "github.com/pganalyze/pg_query_go/v6"
	"github.com/pgquerynarrative/pgquerynarrative/app/errors"
)

// ExtractReadOnlySQL parses sql with the PostgreSQL parser and returns the
// canonical read-only statement to execute or explain.
//
// Behavior:
//   - A plain SELECT/WITH statement is returned trimmed (trailing semicolon removed).
//   - An EXPLAIN wrapping a SELECT/WITH has its inner query extracted via the
//     AST deparser (never string slicing) and wasExplain is true.
//   - EXPLAIN options supplied by the user are rejected with
//     errors.ErrExplainOptionsNotAllowed, except FORMAT JSON which is what the
//     server emits anyway. ANALYZE/BUFFERS/etc. must be requested through the
//     explain endpoint parameters so policy checks cannot be bypassed.
//   - Anything else fails with errors.ErrOnlySelectAllowed.
func ExtractReadOnlySQL(sql string) (inner string, wasExplain bool, err error) {
	trimmed := strings.TrimSpace(sql)
	trimmed = strings.TrimSuffix(trimmed, ";")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return "", false, errors.ErrOnlySelectAllowed
	}

	result, parseErr := pg_query.Parse(trimmed)
	if parseErr != nil {
		return "", false, errors.ErrOnlySelectAllowed
	}
	if len(result.Stmts) != 1 {
		return "", false, errors.ErrMultipleStatements
	}

	stmt := result.Stmts[0].GetStmt()
	if stmt == nil {
		return "", false, errors.ErrOnlySelectAllowed
	}

	if explain := stmt.GetExplainStmt(); explain != nil {
		if optErr := validateExplainStmtOptions(explain); optErr != nil {
			return "", true, optErr
		}
		innerNode := explain.GetQuery()
		if innerNode == nil || innerNode.GetSelectStmt() == nil {
			return "", true, errors.ErrOnlySelectAllowed
		}
		deparsed, depErr := deparseNode(innerNode)
		if depErr != nil {
			return "", true, errors.ErrOnlySelectAllowed
		}
		return deparsed, true, nil
	}

	if stmt.GetSelectStmt() == nil {
		return "", false, errors.ErrOnlySelectAllowed
	}
	return trimmed, false, nil
}

// validateExplainStmtOptions rejects EXPLAIN options that the server does not
// accept from users. Only FORMAT JSON is tolerated because the server always
// re-issues EXPLAIN with its own option set; everything else (ANALYZE, BUFFERS,
// VERBOSE, SETTINGS, non-JSON formats, ...) must go through API parameters.
func validateExplainStmtOptions(explain *pg_query.ExplainStmt) error {
	for _, opt := range explain.GetOptions() {
		def := opt.GetDefElem()
		if def == nil {
			return errors.ErrExplainOptionsNotAllowed
		}
		name := strings.ToLower(def.GetDefname())
		if name != "format" {
			return errors.ErrExplainOptionsNotAllowed
		}
		if !strings.EqualFold(defElemStringValue(def), "json") {
			return errors.ErrExplainOptionsNotAllowed
		}
	}
	return nil
}

func defElemStringValue(def *pg_query.DefElem) string {
	arg := def.GetArg()
	if arg == nil {
		return ""
	}
	if s := arg.GetString_(); s != nil {
		return s.GetSval()
	}
	return ""
}

// RedactConstants replaces literal values (string, numeric, boolean) in sql with
// positional placeholders ($1, $2, ...) using the real PostgreSQL parser, the same
// technique pg_stat_statements uses to normalize queries. This is used before
// persisting EXPLAIN snapshots so stored sql_text cannot leak literal predicate
// values (customer IDs, emails, tokens, etc.) that may appear in a WHERE clause,
// while still preserving the query's shape for plan-analysis review. Falls back to
// returning the original sql (via ok=false) when the statement cannot be parsed,
// so callers can decide on a conservative fallback (e.g. a regex-based redaction).
func RedactConstants(sql string) (redacted string, ok bool) {
	trimmed := strings.TrimSpace(sql)
	if trimmed == "" {
		return "", true
	}
	out, err := pg_query.Normalize(trimmed)
	if err != nil {
		return "", false
	}
	return out, true
}

// deparseNode renders a single statement node back to SQL using the PostgreSQL deparser.
func deparseNode(node *pg_query.Node) (string, error) {
	tree := &pg_query.ParseResult{
		Stmts: []*pg_query.RawStmt{{Stmt: node}},
	}
	out, err := pg_query.Deparse(tree)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}
