package queryrunner

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// IndexProjectionMethod identifies how an index cost was estimated.
const (
	IndexProjectionHypopg    = "hypopg"
	IndexProjectionHeuristic = "heuristic"
	IndexProjectionNone      = "unavailable"
)

// IndexProjection is a projected plan cost if CandidateDDL were applied.
// Method documents honesty: hypopg (planner-backed) vs heuristic (rough).
type IndexProjection struct {
	Method          string
	BaselineCost    float64
	ProjectedCost   float64
	CostDelta       float64
	Available       bool
	Rationale       string
	HypotheticalOID uint32
	// FailureReason explains why hypopg could not be used (privilege, missing
	// extension, read-only transaction, bad DDL). Empty when Method=hypopg.
	FailureReason string
}

var createIndexStmtRe = regexp.MustCompile(`(?is)(CREATE\s+(?:UNIQUE\s+)?INDEX\s+(?:CONCURRENTLY\s+)?(?:IF\s+NOT\s+EXISTS\s+)?[^;]+)`)

// ExtractCreateIndexSQL pulls the CREATE INDEX statement from advisor CandidateDDL
// (which may include leading comment lines).
func ExtractCreateIndexSQL(candidateDDL string) string {
	m := createIndexStmtRe.FindStringSubmatch(candidateDDL)
	if len(m) < 2 {
		return ""
	}
	stmt := strings.TrimSpace(m[1])
	// hypopg does not support CONCURRENTLY.
	stmt = regexp.MustCompile(`(?i)\s+CONCURRENTLY\b`).ReplaceAllString(stmt, "")
	return strings.TrimSpace(stmt)
}

// HypopgAvailable reports whether the hypopg extension is installed.
func (r *Runner) HypopgAvailable(ctx context.Context) bool {
	pool := r.activePool(ctx)
	if pool == nil {
		return false
	}
	var ok bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_extension WHERE extname = 'hypopg')`).Scan(&ok)
	return err == nil && ok
}

// hypopgSchema returns the schema holding the hypopg functions. The analytical
// role runs with search_path pinned to the allowed data schemas, so hypopg
// (installed in public by default) is not reachable unqualified.
func (r *Runner) hypopgSchema(ctx context.Context) (string, error) {
	pool := r.activePool(ctx)
	if pool == nil {
		return "", fmt.Errorf("no pool")
	}
	var schema string
	err := pool.QueryRow(ctx, `
		SELECT n.nspname
		FROM pg_extension e
		JOIN pg_namespace n ON n.oid = e.extnamespace
		WHERE e.extname = 'hypopg'
	`).Scan(&schema)
	if err != nil {
		return "", fmt.Errorf("hypopg schema lookup: %w", err)
	}
	return schema, nil
}

// ProjectIndexCost estimates plan cost if candidateDDL were applied.
// Prefers hypopg when available; otherwise returns an honest heuristic projection
// labeled as non-planner. Never executes real DDL.
func (r *Runner) ProjectIndexCost(ctx context.Context, sql, candidateDDL string, baselineCost float64) IndexProjection {
	out := IndexProjection{
		Method:        IndexProjectionNone,
		BaselineCost:  baselineCost,
		ProjectedCost: baselineCost,
		Available:     false,
		Rationale:     "index cost projection unavailable",
	}
	createSQL := ExtractCreateIndexSQL(candidateDDL)
	if createSQL == "" {
		out.Rationale = "candidate DDL is not a CREATE INDEX statement"
		out.FailureReason = "not_create_index"
		return out
	}
	if r.activePool(ctx) == nil {
		out.FailureReason = "no_pool"
		out.Rationale = "no database pool for hypopg; using labeled heuristic — not planner-backed"
		if baselineCost <= 0 {
			return out
		}
		projected := baselineCost * 0.3
		out.Method = IndexProjectionHeuristic
		out.Available = true
		out.ProjectedCost = projected
		out.CostDelta = projected - baselineCost
		return out
	}

	if r.HypopgAvailable(ctx) {
		proj, err := r.projectWithHypopg(ctx, sql, createSQL, baselineCost)
		if err == nil {
			return proj
		}
		out.FailureReason = classifyHypopgFailure(err)
		out.Rationale = fmt.Sprintf(
			"hypopg present but projection failed (%s); using labeled heuristic — not planner-backed",
			out.FailureReason,
		)
	} else {
		out.FailureReason = "extension_missing"
		out.Rationale = "hypopg extension not installed; using labeled heuristic selectivity projection — not planner-backed"
	}

	// Honest heuristic: assume index avoids ~70% of seq-scan cost when advice exists.
	// Callers MUST surface Method=heuristic distinctly from hypopg (never identical UI treatment).
	if baselineCost <= 0 {
		out.Rationale = strings.TrimSpace(out.Rationale + "; baseline cost unavailable so heuristic skipped")
		return out
	}
	projected := baselineCost * 0.3
	out.Method = IndexProjectionHeuristic
	out.Available = true
	out.ProjectedCost = projected
	out.CostDelta = projected - baselineCost
	if !strings.Contains(strings.ToLower(out.Rationale), "heuristic") {
		out.Rationale = "HEURISTIC (~70% cost reduction assumed) — install hypopg + grants for planner-backed estimates"
	} else if !strings.HasPrefix(strings.ToUpper(out.Rationale), "HEURISTIC") && !strings.Contains(out.Rationale, "labeled heuristic") {
		out.Rationale = "HEURISTIC: " + out.Rationale
	}
	return out
}

func classifyHypopgFailure(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "permission denied") || strings.Contains(msg, "must be owner") || strings.Contains(msg, "insufficient"):
		return "privilege_denied"
	case strings.Contains(msg, "read-only") || strings.Contains(msg, "read only") || strings.Contains(msg, "cannot execute"):
		return "read_only_transaction"
	case strings.Contains(msg, "hypopg") && strings.Contains(msg, "does not exist"):
		return "function_missing"
	case strings.Contains(msg, "syntax") || strings.Contains(msg, "invalid"):
		return "invalid_ddl"
	case strings.Contains(msg, "no pool"):
		return "no_pool"
	default:
		return "hypopg_error"
	}
}

func (r *Runner) projectWithHypopg(ctx context.Context, sql, createIndexSQL string, baselineCost float64) (IndexProjection, error) {
	pool := r.activePool(ctx)
	if pool == nil {
		return IndexProjection{}, fmt.Errorf("no pool")
	}
	innerSQL, _, err := ExtractReadOnlySQL(sql)
	if err != nil {
		return IndexProjection{}, err
	}
	schema, err := r.hypopgSchema(ctx)
	if err != nil {
		return IndexProjection{}, err
	}
	qualified := pgx.Identifier{schema}.Sanitize()
	resetSQL := `SELECT ` + qualified + `.hypopg_reset()`
	createSQL := `SELECT indexrelid, indexname FROM ` + qualified + `.hypopg_create_index($1)`

	// hypopg mutates backend-local state. The analytical role defaults to
	// transaction_read_only=on; lift it for this short transaction only.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadWrite})
	if err != nil {
		return IndexProjection{}, fmt.Errorf("begin read-write tx for hypopg: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SET LOCAL transaction_read_only = off`); err != nil {
		return IndexProjection{}, fmt.Errorf("read-only transaction: cannot disable for hypopg: %w", err)
	}

	// A failed reset aborts the transaction, which would mask the real cause
	// behind "current transaction is aborted" on every later statement.
	if _, err := tx.Exec(ctx, resetSQL); err != nil {
		return IndexProjection{}, err
	}
	var indexName string
	var oid uint32
	// hypopg_create_index returns (indexrelid, indexname) — not index_name.
	if err := tx.QueryRow(ctx, createSQL, createIndexSQL).Scan(&oid, &indexName); err != nil {
		return IndexProjection{}, err
	}

	explainSQL := buildExplainSQL(innerSQL, ExplainOptions{Analyze: false, Buffers: false})
	var planText string
	if err := tx.QueryRow(ctx, explainSQL).Scan(&planText); err != nil {
		_, _ = tx.Exec(ctx, resetSQL)
		return IndexProjection{}, err
	}
	totalCost, _, _, err := parseExplainJSON([]byte(planText))
	_, _ = tx.Exec(ctx, resetSQL)
	if err != nil {
		return IndexProjection{}, err
	}
	// Keep hypothetical indexes out of the session even if commit fails.
	_ = tx.Commit(ctx)
	if baselineCost <= 0 {
		baselineCost = totalCost
	}
	return IndexProjection{
		Method:          IndexProjectionHypopg,
		BaselineCost:    baselineCost,
		ProjectedCost:   totalCost,
		CostDelta:       totalCost - baselineCost,
		Available:       true,
		Rationale:       fmt.Sprintf("hypopg projected plan cost with hypothetical index %s", indexName),
		HypotheticalOID: oid,
	}, nil
}

// ErrHypopgUnavailable is returned by helpers when the extension cannot run.
var ErrHypopgUnavailable = errors.New("hypopg unavailable")
