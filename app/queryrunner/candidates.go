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

// ScoredCandidate is a rewrite or DDL suggestion with optional dry-EXPLAIN or
// projected-index metrics relative to a baseline plan.
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
	ProjectionMethod  string // hypopg | heuristic | unavailable (index_ddl)
}

// CollectIndexDDLCandidates returns unique CREATE/DROP index DDL suggestions
// from plan findings. Without a cost projection they remain review-only.
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
			Kind:             CandidateKindIndexDDL,
			Rankable:         false,
			DDL:              ddl,
			Rationale:        rationale,
			Category:         category,
			Confidence:       f.Confidence,
			ProjectionMethod: IndexProjectionNone,
		})
	}
	return out
}

// ScoreIndexProjection attaches hypopg/heuristic projected cost so an index DDL
// candidate can participate in ranking. Heuristic projections are never treated
// as planner-backed: they stay review-only (Rankable=false) so they cannot look
// identical to hypopg in ranked lists. hypopg projections are rankable.
func ScoreIndexProjection(base ScoredCandidate, proj IndexProjection) ScoredCandidate {
	sc := base
	sc.ProjectionMethod = proj.Method
	rationale := base.Rationale
	if proj.Rationale != "" {
		rationale = strings.TrimSpace(rationale + " — " + proj.Rationale)
	}
	if proj.FailureReason != "" && proj.Method != IndexProjectionHypopg {
		rationale = strings.TrimSpace(rationale + " [hypopg_failure=" + proj.FailureReason + "]")
	}
	sc.Rationale = rationale
	if !proj.Available {
		sc.Rankable = false
		if sc.ProjectionMethod == "" {
			sc.ProjectionMethod = IndexProjectionNone
		}
		return sc
	}
	sc.TotalCost = proj.ProjectedCost
	sc.CostDelta = proj.CostDelta
	// Only planner-backed hypopg estimates participate in numeric ranking.
	if proj.Method == IndexProjectionHypopg {
		sc.Rankable = true
	} else {
		sc.Rankable = false
	}
	return sc
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

// candidateImproves reports whether c measurably beats the baseline plan on a
// tracked metric — lower total cost, fewer partitions scanned, or a detected
// structural improvement. A candidate that only ties the baseline is not an
// improvement.
func (c ScoredCandidate) candidateImproves() bool {
	return c.CostDelta < 0 || c.PartitionsDelta < 0 || len(c.Improved) > 0
}

// RankScoredCandidates assigns Rank 1..n to the planner-backed candidates that
// actually improve on the baseline, best first (cost delta, then partitions
// delta, then execution time). A rankable candidate that does not beat the
// baseline keeps Rank 0, is appended after the ranked ones with a
// "not recommended" note, and never displaces a real improvement. Review-only
// (non-rankable) candidates keep Rank 0 and come last, unchanged.
func RankScoredCandidates(cands []ScoredCandidate) []ScoredCandidate {
	if len(cands) == 0 {
		return nil
	}
	improving := make([]ScoredCandidate, 0, len(cands))
	notImproving := make([]ScoredCandidate, 0, len(cands))
	other := make([]ScoredCandidate, 0, len(cands))
	for _, c := range cands {
		switch {
		case c.Rankable && c.candidateImproves():
			improving = append(improving, c)
		case c.Rankable:
			c.Rationale = strings.TrimSpace(c.Rationale + " — not recommended: no measured improvement over the baseline plan")
			notImproving = append(notImproving, c)
		default:
			other = append(other, c)
		}
	}
	sort.SliceStable(improving, func(i, j int) bool {
		a, b := improving[i], improving[j]
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
	for i := range improving {
		improving[i].Rank = i + 1
	}
	out := make([]ScoredCandidate, 0, len(cands))
	out = append(out, improving...)
	out = append(out, notImproving...)
	out = append(out, other...)
	return out
}

// RankingRecommendation is a one-line verdict for a ranked candidate list: empty
// when there is a recommended (Rank 1) candidate, otherwise an explanation.
func RankingRecommendation(ranked []ScoredCandidate) string {
	for _, c := range ranked {
		if c.Rank == 1 {
			return ""
		}
	}
	testedRewrite := false
	for _, c := range ranked {
		if c.Kind == CandidateKindSQLRewrite {
			testedRewrite = true
			break
		}
	}
	if testedRewrite {
		return "No improving candidate found — every tested rewrite scored equal to or worse than the baseline plan. Not recommended."
	}
	return "No planner-backed candidate to rank — the suggestions are review-only."
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
