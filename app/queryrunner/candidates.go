package queryrunner

import (
	"sort"
	"strings"
)

// Candidate kinds returned by ranked-candidate collection.
const (
	CandidateKindSQLRewrite = "sql_rewrite"
	CandidateKindIndexDDL   = "index_ddl"
)

// ScoredCandidate is a rewrite or DDL suggestion with optional dry-EXPLAIN metrics
// relative to a baseline plan. Index DDL candidates are never rankable (cannot be
// EXPLAINed as SELECT under a read-only policy).
type ScoredCandidate struct {
	Kind              string
	Rankable          bool
	Rank              int
	SQL               string
	DDL               string
	Rationale         string
	Category          string
	Confidence        string
	TotalCost         float64
	CostDelta         float64
	PartitionsScanned float64
	PartitionsDelta   float64
	ExecutionTimeMs   float64
	HasTiming         bool
	Improved          []string
}

// CollectIndexDDLCandidates returns unique CREATE/DROP index DDL suggestions
// from plan findings. These are review-only and not dry-EXPLAIN ranked.
func CollectIndexDDLCandidates(findings []PlanFinding) []ScoredCandidate {
	seen := map[string]struct{}{}
	var out []ScoredCandidate
	for _, f := range findings {
		if f.IndexAdvice == nil {
			continue
		}
		ddl := strings.TrimSpace(f.IndexAdvice.CandidateDDL)
		if ddl == "" {
			continue
		}
		// Prefer actionable "add index" advice; still surface other DDL once.
		key := ddl
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}

		rationale := f.IndexAdvice.PotentialBenefit
		if rationale == "" {
			rationale = "Index advice from catalog enrichment (expert review only; never auto-applied)"
		}
		issues := strings.Join(f.IndexAdvice.Issues, ", ")
		if issues != "" {
			rationale = rationale + " [" + issues + "]"
		}
		category := CategoryIndexCandidate
		if f.Category == CategoryIndexHealth {
			category = CategoryIndexHealth
		}
		out = append(out, ScoredCandidate{
			Kind:       CandidateKindIndexDDL,
			Rankable:   false,
			DDL:        ddl,
			Rationale:  rationale,
			Category:   category,
			Confidence: f.Confidence,
		})
	}
	return out
}

// ScoreSQLRewrite attaches dry-EXPLAIN metrics for a rewrite relative to baseline.
func ScoreSQLRewrite(c RewriteCandidate, baseline, after PlanMetrics, improved []string) ScoredCandidate {
	partBefore := partitionCountForRanking(baseline)
	partAfter := partitionCountForRanking(after)
	sc := ScoredCandidate{
		Kind:              CandidateKindSQLRewrite,
		Rankable:          true,
		SQL:               c.SQL,
		Rationale:         c.Rationale,
		Category:          c.Category,
		Confidence:        c.Confidence,
		TotalCost:         after.TotalCost,
		CostDelta:         after.TotalCost - baseline.TotalCost,
		PartitionsScanned: partAfter,
		PartitionsDelta:   partAfter - partBefore,
		Improved:          append([]string(nil), improved...),
	}
	if after.HasActualTiming {
		sc.ExecutionTimeMs = after.ExecutionTimeMs
		sc.HasTiming = true
	}
	return sc
}

// RankScoredCandidates sorts rankable candidates by improvement (cost delta,
// then partitions delta, then execution time when present) and assigns Rank
// starting at 1. Non-rankable candidates keep Rank 0 and are appended after.
func RankScoredCandidates(cands []ScoredCandidate) []ScoredCandidate {
	if len(cands) == 0 {
		return nil
	}
	rankable := make([]ScoredCandidate, 0, len(cands))
	other := make([]ScoredCandidate, 0, len(cands))
	for _, c := range cands {
		if c.Rankable {
			rankable = append(rankable, c)
		} else {
			other = append(other, c)
		}
	}
	sort.SliceStable(rankable, func(i, j int) bool {
		a, b := rankable[i], rankable[j]
		if a.CostDelta != b.CostDelta {
			return a.CostDelta < b.CostDelta
		}
		if a.PartitionsDelta != b.PartitionsDelta {
			return a.PartitionsDelta < b.PartitionsDelta
		}
		if a.HasTiming && b.HasTiming && a.ExecutionTimeMs != b.ExecutionTimeMs {
			return a.ExecutionTimeMs < b.ExecutionTimeMs
		}
		return a.SQL < b.SQL
	})
	for i := range rankable {
		rankable[i].Rank = i + 1
	}
	return append(rankable, other...)
}

func partitionCountForRanking(m PlanMetrics) float64 {
	if m.HasPartitionAppend {
		return m.PartitionsScanned
	}
	// Fully pruned plans often drop Append; treat as a single partition scanned.
	if m.PartitionsScanned > 0 {
		return m.PartitionsScanned
	}
	return 1
}
