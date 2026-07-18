package queryrunner

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type tableCatalogStats struct {
	EstimatedRows   int64
	TotalBytes      int64
	IndexCount      int
	IndexNames      []string
	Indexes         []IndexDefinition
	LastAnalyze     *time.Time
	LastAutoanalyze *time.Time
}

// smallTableRowThreshold is the row count below which index recommendations
// are downgraded: sequential scans over small tables are usually optimal.
const smallTableRowThreshold = 1000

// maxIndexesPerTable bounds how many index definitions are pulled per relation
// so a pathologically over-indexed table can't blow up analysis time.
const maxIndexesPerTable = 25

// lowUseIndexScanThreshold is the pg_stat_user_indexes.idx_scan count at or
// below which a non-unique, non-primary index is flagged as low-use.
const lowUseIndexScanThreshold = 0

// enrichExplainFindings adds table-size, index, and statistics-freshness
// context from the catalog, and suppresses low-value index recommendations.
func (r *Runner) enrichExplainFindings(ctx context.Context, findings []PlanFinding) []PlanFinding {
	if len(findings) == 0 || r.activePool() == nil {
		return findings
	}

	type relKey struct {
		schema string
		name   string
	}
	keys := make(map[relKey]struct{})
	for _, f := range findings {
		if f.Relation == "" {
			continue
		}
		keys[relKey{schema: f.Schema, name: f.Relation}] = struct{}{}
	}
	if len(keys) == 0 {
		return findings
	}

	stats := make(map[relKey]tableCatalogStats, len(keys))
	pool := r.activePool()
	if pool == nil {
		return findings
	}
	for k := range keys {
		var row tableCatalogStats
		err := pool.QueryRow(ctx, `
			SELECT COALESCE(c.reltuples::bigint, 0),
			       COALESCE(pg_total_relation_size(c.oid), 0),
			       (SELECT COUNT(*)::int FROM pg_index i WHERE i.indrelid = c.oid AND i.indisvalid),
			       s.last_analyze,
			       s.last_autoanalyze
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_stat_user_tables s ON s.relid = c.oid
			WHERE n.nspname = $1 AND c.relname = $2
		`, k.schema, k.name).Scan(&row.EstimatedRows, &row.TotalBytes, &row.IndexCount, &row.LastAnalyze, &row.LastAutoanalyze)
		if err != nil {
			continue
		}
		indexes, err := fetchIndexDefinitions(ctx, pool, k.schema, k.name)
		if err == nil {
			row.Indexes = indexes
			for _, idx := range indexes {
				row.IndexNames = append(row.IndexNames, idx.Name)
			}
		}
		stats[k] = row
	}

	out := make([]PlanFinding, len(findings))
	copy(out, findings)
	for i := range out {
		if out[i].Relation == "" {
			continue
		}
		st, ok := stats[relKey{schema: out[i].Schema, name: out[i].Relation}]
		if !ok {
			continue
		}
		out[i] = applyCatalogContext(out[i], st)
		out[i].IndexAdvice = buildIndexAdvice(out[i], st)
	}

	// Beyond per-node enrichment, surface health issues with the *existing*
	// indexes on every relation touched by this plan (duplicate prefixes,
	// overlapping coverage, invalid indexes, low-use indexes) — these are
	// evidence-backed but independent of any single plan node.
	reportedTables := make(map[relKey]bool, len(stats))
	for k, st := range stats {
		if reportedTables[k] || len(st.Indexes) == 0 {
			continue
		}
		reportedTables[k] = true
		out = append(out, detectIndexIssues(k.schema, k.name, st.Indexes)...)
	}
	return out
}

// fetchIndexDefinitions retrieves full index metadata for one relation: index
// name and definition, key/INCLUDE columns in position order (via
// pg_get_indexdef per column), the partial-index predicate (if any),
// uniqueness/primary/validity flags, on-disk size, and live usage counters
// from pg_stat_user_indexes. Returns at most maxIndexesPerTable rows.
func fetchIndexDefinitions(ctx context.Context, pool *pgxpool.Pool, schema, table string) ([]IndexDefinition, error) {
	if pool == nil {
		return nil, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT
			ic.relname,
			pg_get_indexdef(i.indexrelid),
			i.indisunique,
			i.indisprimary,
			i.indisvalid,
			COALESCE(pg_relation_size(i.indexrelid), 0),
			COALESCE(s.idx_scan, 0),
			COALESCE(s.idx_tup_read, 0),
			COALESCE(s.idx_tup_fetch, 0),
			COALESCE(pg_get_expr(i.indpred, i.indrelid), ''),
			ARRAY(
				SELECT pg_get_indexdef(i.indexrelid, gs, false)
				FROM generate_series(1, i.indnkeyatts::int) AS gs
			),
			ARRAY(
				SELECT pg_get_indexdef(i.indexrelid, gs, false)
				FROM generate_series(i.indnkeyatts::int + 1, i.indnatts::int) AS gs
			)
		FROM pg_index i
		JOIN pg_class ic ON ic.oid = i.indexrelid
		JOIN pg_class tc ON tc.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = tc.relnamespace
		LEFT JOIN pg_stat_user_indexes s ON s.indexrelid = i.indexrelid
		WHERE n.nspname = $1 AND tc.relname = $2
		ORDER BY ic.relname
		LIMIT $3
	`, schema, table, maxIndexesPerTable)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []IndexDefinition
	for rows.Next() {
		var d IndexDefinition
		if err := rows.Scan(
			&d.Name, &d.Definition, &d.IsUnique, &d.IsPrimary, &d.IsValid,
			&d.SizeBytes, &d.IndexScans, &d.TuplesRead, &d.TuplesFetched, &d.Predicate,
			&d.KeyColumns, &d.IncludeColumns,
		); err != nil {
			return nil, err
		}
		for i, c := range d.KeyColumns {
			d.KeyColumns[i] = normalizeColumnName(c)
		}
		for i, c := range d.IncludeColumns {
			d.IncludeColumns[i] = normalizeColumnName(c)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func applyCatalogContext(f PlanFinding, st tableCatalogStats) PlanFinding {
	parts := []string{f.Message}
	if st.EstimatedRows > 0 {
		parts = append(parts, fmt.Sprintf("catalog estimates ~%s rows (~%s on disk)", formatRowCount(st.EstimatedRows), formatBytes(st.TotalBytes)))
		f.Evidence = append(f.Evidence, fmt.Sprintf("catalog reltuples=%d", st.EstimatedRows), fmt.Sprintf("pg_total_relation_size=%d", st.TotalBytes))
	}

	// Small tables: sequential scans are usually optimal; suppress index advice.
	if f.IsSeqScan && st.EstimatedRows > 0 && st.EstimatedRows < smallTableRowThreshold {
		f.Confidence = "low"
		parts = append(parts, fmt.Sprintf("table is small (<%d rows); an index is unlikely to help", smallTableRowThreshold))
		f.Message = strings.Join(parts, " — ")
		return f
	}

	switch {
	case st.IndexCount == 0:
		parts = append(parts, "no indexes found on this relation")
		if f.IsSeqScan && f.Confidence != "high" {
			f.Confidence = "high"
		}
		if f.IsSeqScan && st.TotalBytes > 0 {
			parts = append(parts, fmt.Sprintf("note: a new btree index typically adds ~%s of storage and slows writes on this table", formatBytes(estimateIndexSizeBytes(st))))
		}
	case len(st.IndexNames) > 0:
		parts = append(parts, fmt.Sprintf("existing indexes: %s — verify they cover the filter columns before adding another", strings.Join(st.IndexNames, ", ")))
		if f.IsSeqScan && st.EstimatedRows > 1_000_000 && f.Confidence == "medium" {
			f.Confidence = "high"
		}
	default:
		parts = append(parts, fmt.Sprintf("%d indexes on relation", st.IndexCount))
	}

	// Stale statistics: cardinality misestimates on never/rarely analyzed tables.
	if f.Category == CategoryCardinality || f.IsSeqScan {
		if note := staleStatsNote(st); note != "" {
			parts = append(parts, note)
			f.Evidence = append(f.Evidence, "stats: "+note)
		}
	}

	f.Message = strings.Join(parts, " — ")
	return f
}

// staleStatsNote flags relations whose statistics were never collected or are old.
func staleStatsNote(st tableCatalogStats) string {
	last := st.LastAnalyze
	if st.LastAutoanalyze != nil && (last == nil || st.LastAutoanalyze.After(*last)) {
		last = st.LastAutoanalyze
	}
	if last == nil {
		if st.EstimatedRows <= 0 {
			return "statistics never collected (run ANALYZE)"
		}
		return ""
	}
	if age := time.Since(*last); age > 7*24*time.Hour {
		return fmt.Sprintf("statistics last collected %s ago (consider ANALYZE)", formatDurationDays(age))
	}
	return ""
}

// estimateIndexSizeBytes gives a rough single-column btree size estimate for
// storage-cost messaging only (never used for automated DDL).
func estimateIndexSizeBytes(st tableCatalogStats) int64 {
	const bytesPerEntry = 24 // key + tuple pointer + page overhead, rough average
	est := st.EstimatedRows * bytesPerEntry
	if st.TotalBytes > 0 && est > st.TotalBytes {
		est = st.TotalBytes
	}
	return est
}

func formatDurationDays(d time.Duration) string {
	days := int64(d.Hours() / 24)
	if days < 1 {
		return "under a day"
	}
	return fmt.Sprintf("%dd", days)
}

func formatBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f kB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func formatRowCount(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

type indexCoverage int

const (
	coverageNone indexCoverage = iota
	coveragePartial
	coverageFull
)

// indexColumnCoverage classifies how well idx covers cols: coverageFull means
// cols matches idx's leftmost key-column prefix (as a set); coveragePartial
// means some but not all of cols appear anywhere in idx's key or INCLUDE
// columns; coverageNone means no overlap at all.
func indexColumnCoverage(idx IndexDefinition, cols []string) indexCoverage {
	if len(cols) == 0 || len(idx.KeyColumns) == 0 {
		return coverageNone
	}
	if isColumnPrefix(cols, idx.KeyColumns) {
		return coverageFull
	}
	present := make(map[string]bool, len(idx.KeyColumns)+len(idx.IncludeColumns))
	for _, c := range idx.KeyColumns {
		present[c] = true
	}
	for _, c := range idx.IncludeColumns {
		present[c] = true
	}
	for _, c := range cols {
		if present[c] {
			return coveragePartial
		}
	}
	return coverageNone
}

// buildIndexAdvice relates a finding's implicated columns (RelatedColumns) to
// its relation's existing index definitions. When an index already covers the
// columns as a leftmost prefix, advice explains that adding another index is
// unlikely to help. When no index covers them, advice estimates the potential
// benefit and write/storage cost of a new index and drafts a candidate DDL
// statement FOR EXPERT REVIEW ONLY — this function never causes any DDL to
// execute, here or anywhere downstream.
func buildIndexAdvice(f PlanFinding, st tableCatalogStats) *IndexAdvice {
	if len(f.RelatedColumns) == 0 {
		return nil
	}
	advice := &IndexAdvice{RelatedColumns: append([]string(nil), f.RelatedColumns...)}

	var covering *IndexDefinition
	var partial []IndexDefinition
	for i := range st.Indexes {
		switch indexColumnCoverage(st.Indexes[i], f.RelatedColumns) {
		case coverageFull:
			if covering == nil {
				covering = &st.Indexes[i]
			}
		case coveragePartial:
			partial = append(partial, st.Indexes[i])
		}
	}

	switch {
	case covering != nil:
		advice.RelatedIndexes = []IndexDefinition{*covering}
		advice.Issues = []string{"already_covered"}
		advice.PotentialBenefit = fmt.Sprintf(
			"existing index %s already covers (%s) as a leftmost prefix — investigate stale statistics, low selectivity, or a type/collation mismatch before adding another index",
			covering.Name, strings.Join(f.RelatedColumns, ", "),
		)
		return advice
	case len(partial) > 0:
		names := make([]string, len(partial))
		for i, idx := range partial {
			names[i] = idx.Name
		}
		advice.RelatedIndexes = partial
		advice.Issues = []string{"partial_coverage"}
		advice.PotentialBenefit = fmt.Sprintf(
			"%s reference some of (%s) but not as a leftmost prefix — extending or reordering an existing index may avoid creating a new one",
			strings.Join(names, ", "), strings.Join(f.RelatedColumns, ", "),
		)
	default:
		advice.Issues = []string{"no_covering_index"}
		if st.EstimatedRows > 0 {
			advice.PotentialBenefit = fmt.Sprintf(
				"a new index on (%s) could let the planner use an index scan instead of scanning ~%s rows",
				strings.Join(f.RelatedColumns, ", "), formatRowCount(st.EstimatedRows),
			)
		} else {
			advice.PotentialBenefit = fmt.Sprintf(
				"a new index on (%s) could avoid scanning the full table for this predicate",
				strings.Join(f.RelatedColumns, ", "),
			)
		}
	}

	advice.WriteCost = "adds one index entry maintained on every INSERT/UPDATE/DELETE touching these columns"
	if st.EstimatedRows > 0 {
		advice.StorageCost = fmt.Sprintf("~%s", formatBytes(estimateIndexSizeBytes(st)))
	} else {
		advice.StorageCost = "unknown (no catalog row-count estimate available)"
	}
	advice.CandidateDDL = draftCandidateDDL(f.Schema, f.Relation, f.RelatedColumns)
	return advice
}

// hasExpressionColumn reports whether cols contains an entry that couldn't be
// normalized to a plain identifier (e.g. an expression/functional-index
// column), which makes prefix/overlap comparisons unreliable.
func hasExpressionColumn(cols []string) bool {
	for _, c := range cols {
		if c == "" {
			return true
		}
	}
	return false
}

// detectIndexIssues inspects one relation's existing index definitions and
// reports evidence-backed health findings: invalid indexes, low-use indexes,
// one index being a redundant leftmost prefix of another, and indexes that
// overlap (same column set, or same leading column with diverging tails)
// without being a clean prefix relationship. All CandidateDDL in the returned
// findings is FOR EXPERT REVIEW ONLY and is never executed by this codebase.
func detectIndexIssues(schema, relation string, indexes []IndexDefinition) []PlanFinding {
	var out []PlanFinding
	target := relationOrNode("", schema, relation)

	for _, idx := range indexes {
		if idx.IsValid {
			continue
		}
		out = append(out, PlanFinding{
			Schema:     schema,
			Relation:   relation,
			Category:   CategoryIndexHealth,
			Confidence: "high",
			Message: fmt.Sprintf(
				"Index %s on %s is INVALID (typically a failed CREATE INDEX CONCURRENTLY or REINDEX) — the planner never uses it, but it still costs storage and write overhead",
				idx.Name, target,
			),
			Evidence: []string{"indisvalid=false", "index=" + idx.Name, fmt.Sprintf("size_bytes=%d", idx.SizeBytes)},
			IndexAdvice: &IndexAdvice{
				RelatedIndexes:   []IndexDefinition{idx},
				Issues:           []string{"invalid"},
				PotentialBenefit: "removes dead storage and write overhead from an index the planner ignores",
				WriteCost:        "none — dropping an invalid index only reduces write cost",
				StorageCost:      fmt.Sprintf("frees ~%s", formatBytes(idx.SizeBytes)),
				CandidateDDL:     draftDropDDL(idx.Name, "invalid — never used by the planner; recreate with CREATE INDEX CONCURRENTLY if the index is still needed"),
			},
		})
	}

	for _, idx := range indexes {
		if !idx.IsValid || idx.IsPrimary || idx.IndexScans > lowUseIndexScanThreshold {
			continue
		}
		confidence := "medium"
		benefit := "removes write overhead and storage for an index the planner never used in this session's statistics window"
		if idx.IsUnique {
			// Still enforces a constraint; dropping changes correctness, not just performance.
			confidence = "low"
			benefit = "consider whether the uniqueness constraint is still required before dropping — this index has zero recorded scans but also enforces uniqueness"
		}
		out = append(out, PlanFinding{
			Schema:     schema,
			Relation:   relation,
			Category:   CategoryIndexHealth,
			Confidence: confidence,
			Message: fmt.Sprintf(
				"Index %s on %s has %d recorded scans (pg_stat_user_indexes.idx_scan) — a strong candidate for removal if this reflects steady-state traffic",
				idx.Name, target, idx.IndexScans,
			),
			Evidence: []string{fmt.Sprintf("idx_scan=%d", idx.IndexScans), "index=" + idx.Name, fmt.Sprintf("size_bytes=%d", idx.SizeBytes)},
			IndexAdvice: &IndexAdvice{
				RelatedIndexes:   []IndexDefinition{idx},
				Issues:           []string{"low_use"},
				PotentialBenefit: benefit,
				WriteCost:        "none — dropping an unused index only reduces write cost",
				StorageCost:      fmt.Sprintf("frees ~%s", formatBytes(idx.SizeBytes)),
				CandidateDDL:     draftDropDDL(idx.Name, "low use — confirm against a full traffic cycle (idx_scan resets on stats reset/restart) before dropping"),
			},
		})
	}

	for i := range indexes {
		for j := range indexes {
			if i == j {
				continue
			}
			a, b := indexes[i], indexes[j]
			if !a.IsValid || !b.IsValid || a.Predicate != b.Predicate {
				continue
			}
			if hasExpressionColumn(a.KeyColumns) || hasExpressionColumn(b.KeyColumns) {
				continue
			}
			switch {
			case len(a.KeyColumns) < len(b.KeyColumns) && isColumnPrefix(a.KeyColumns, b.KeyColumns):
				if a.IsUnique && !b.IsUnique {
					// Dropping a would lose a uniqueness guarantee b doesn't provide.
					continue
				}
				out = append(out, PlanFinding{
					Schema:     schema,
					Relation:   relation,
					Category:   CategoryIndexHealth,
					Confidence: "high",
					Message: fmt.Sprintf(
						"Index %s (%s) on %s is a redundant leftmost prefix of %s (%s) — every query %s can serve, %s can also serve",
						a.Name, strings.Join(a.KeyColumns, ", "), target, b.Name, strings.Join(b.KeyColumns, ", "), a.Name, b.Name,
					),
					Evidence: []string{"index=" + a.Name, "superseded_by=" + b.Name, fmt.Sprintf("size_bytes=%d", a.SizeBytes)},
					IndexAdvice: &IndexAdvice{
						RelatedIndexes:   []IndexDefinition{a, b},
						Issues:           []string{"duplicate_prefix"},
						PotentialBenefit: fmt.Sprintf("removes a redundant index; %s already serves every query %s could", b.Name, a.Name),
						WriteCost:        "none — dropping the redundant index only reduces write cost",
						StorageCost:      fmt.Sprintf("frees ~%s", formatBytes(a.SizeBytes)),
						CandidateDDL:     draftDropDDL(a.Name, fmt.Sprintf("redundant leftmost prefix of %s", b.Name)),
					},
				})
			case a.Name < b.Name && sameColumnSet(a.KeyColumns, b.KeyColumns):
				out = append(out, PlanFinding{
					Schema:     schema,
					Relation:   relation,
					Category:   CategoryIndexHealth,
					Confidence: "medium",
					Message: fmt.Sprintf(
						"Indexes %s and %s on %s cover the same columns (%s) in different order — likely redundant; keep whichever ordering matches real query sort/filter patterns",
						a.Name, b.Name, target, strings.Join(a.KeyColumns, ", "),
					),
					Evidence: []string{"index=" + a.Name, "overlaps_with=" + b.Name},
					IndexAdvice: &IndexAdvice{
						RelatedIndexes:   []IndexDefinition{a, b},
						Issues:           []string{"overlapping"},
						PotentialBenefit: "consolidating to one index reduces write amplification and storage without losing coverage",
						WriteCost:        "none — dropping one of the pair only reduces write cost",
						StorageCost:      fmt.Sprintf("frees ~%s (whichever is dropped)", formatBytes(minInt64(a.SizeBytes, b.SizeBytes))),
					},
				})
			case a.Name < b.Name && len(a.KeyColumns) > 0 && len(b.KeyColumns) > 0 && a.KeyColumns[0] == b.KeyColumns[0] &&
				!isColumnPrefix(a.KeyColumns, b.KeyColumns) && !isColumnPrefix(b.KeyColumns, a.KeyColumns):
				out = append(out, PlanFinding{
					Schema:     schema,
					Relation:   relation,
					Category:   CategoryIndexHealth,
					Confidence: "low",
					Message: fmt.Sprintf(
						"Indexes %s (%s) and %s (%s) on %s share leading column %q but diverge afterward — consider consolidating into one multi-column index if both access patterns are common",
						a.Name, strings.Join(a.KeyColumns, ", "), b.Name, strings.Join(b.KeyColumns, ", "), target, a.KeyColumns[0],
					),
					Evidence: []string{"index=" + a.Name, "shares_leading_column_with=" + b.Name},
					IndexAdvice: &IndexAdvice{
						RelatedIndexes:   []IndexDefinition{a, b},
						Issues:           []string{"overlapping_leading_column"},
						PotentialBenefit: "a single multi-column index covering both access patterns can reduce total index count and write cost",
					},
				})
			}
		}
	}
	return out
}

// draftDropDDL builds a DROP INDEX CONCURRENTLY statement for expert review
// only, annotated with the reason it was flagged. It is never executed by
// this codebase.
func draftDropDDL(indexName, reason string) string {
	return fmt.Sprintf(
		"-- CANDIDATE for expert review only — do not auto-apply. Reason: %s.\nDROP INDEX CONCURRENTLY IF EXISTS %s;",
		reason, quoteIdent(indexName),
	)
}

// draftCandidateDDL builds a CREATE INDEX CONCURRENTLY statement for expert
// review only, covering cols on schema.relation. It is never executed by this
// codebase — callers must treat it as a suggestion requiring human judgment
// about lock contention, replication lag, and available disk headroom.
func draftCandidateDDL(schema, relation string, cols []string) string {
	if relation == "" || hasExpressionColumn(cols) || len(cols) == 0 {
		return ""
	}
	quotedCols := make([]string, len(cols))
	for i, c := range cols {
		quotedCols[i] = quoteIdent(c)
	}
	table := quoteIdent(relation)
	if schema != "" {
		table = quoteIdent(schema) + "." + table
	}
	idxName := "idx_" + relation + "_" + strings.Join(cols, "_")
	return fmt.Sprintf(
		"-- CANDIDATE for expert review only — do not auto-apply. Verify on staging/a replica, check for lock contention, and confirm write/storage cost first.\nCREATE INDEX CONCURRENTLY %s ON %s (%s);",
		quoteIdent(idxName), table, strings.Join(quotedCols, ", "),
	)
}

// quoteIdent double-quotes a PostgreSQL identifier for safe inclusion in a
// candidate (never-executed) DDL statement.
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
