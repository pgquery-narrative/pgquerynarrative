package queryrunner

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type tableCatalogStats struct {
	EstimatedRows   int64
	TotalBytes      int64
	IndexCount      int
	IndexNames      []string
	LastAnalyze     *time.Time
	LastAutoanalyze *time.Time
}

// smallTableRowThreshold is the row count below which index recommendations
// are downgraded: sequential scans over small tables are usually optimal.
const smallTableRowThreshold = 1000

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
		indexRows, err := pool.Query(ctx, `
			SELECT indexname
			FROM pg_indexes
			WHERE schemaname = $1 AND tablename = $2
			ORDER BY indexname
			LIMIT 5
		`, k.schema, k.name)
		if err == nil {
			for indexRows.Next() {
				var name string
				if scanErr := indexRows.Scan(&name); scanErr == nil && name != "" {
					row.IndexNames = append(row.IndexNames, name)
				}
			}
			indexRows.Close()
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
	}
	return out
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
