package pqncli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

// Verdicts. They match pqn_api.prove.
const (
	VerdictProven        = "Proven"
	VerdictNotFaster     = "NotFaster"
	VerdictDifferent     = "Different"
	VerdictUnverified    = "Unverified"
	minProvenSpeedup     = 1.2
	defaultMaxCandidates = 5
	defaultMaxProofs     = 3
)

var placeholderRe = regexp.MustCompile(`\$[0-9]+`)

// InvestigateOptions describes one investigation.
type InvestigateOptions struct {
	SQL           string
	QueryID       *int64
	Binds         []string
	Title         string
	MaxCandidates int
	MaxProofs     int
	NoRecord      bool
}

// CandidateResult is one proposal and, when it could be measured, its proof.
type CandidateResult struct {
	Kind        string   `json:"kind"` // sql_rewrite or index_ddl
	SQL         string   `json:"sql,omitempty"`
	DDL         string   `json:"ddl,omitempty"`
	Category    string   `json:"category"`
	Confidence  string   `json:"confidence"`
	Rationale   string   `json:"rationale"`
	CostBefore  float64  `json:"cost_before"`
	CostAfter   float64  `json:"cost_after"`
	Improved    []string `json:"improved,omitempty"`
	Verdict     string   `json:"verdict"`
	Reason      string   `json:"reason"`
	Rows        int64    `json:"rows,omitempty"`
	BeforeMs    float64  `json:"before_ms,omitempty"`
	AfterMs     float64  `json:"after_ms,omitempty"`
	Speedup     float64  `json:"speedup,omitempty"`
	ProofSource string   `json:"proof_source,omitempty"`
	Error       string   `json:"error,omitempty"`
}

// FindingLine is a plan finding trimmed to what a person reads.
type FindingLine struct {
	Category   string   `json:"category"`
	Confidence string   `json:"confidence"`
	Relation   string   `json:"relation,omitempty"`
	Message    string   `json:"message"`
	Evidence   []string `json:"evidence,omitempty"`
}

// Report is the result of an investigation.
type Report struct {
	InvestigationID int64             `json:"investigation_id,omitempty"`
	SQL             string            `json:"sql"`
	ExecutedSQL     string            `json:"executed_sql,omitempty"`
	GenericPlan     bool              `json:"generic_plan"`
	TotalCost       float64           `json:"total_cost"`
	Findings        []FindingLine     `json:"findings"`
	Candidates      []CandidateResult `json:"candidates"`
	Proven          int               `json:"proven"`
	Notes           []string          `json:"notes,omitempty"`
}

// ExitCode is 0 when at least one proposal was proven, 2 when there is nothing proven to ship.
func (r *Report) ExitCode() int {
	if r.Proven > 0 {
		return 0
	}
	return 2
}

// verdictFromMeasurement mirrors pqn_api.prove, for the replica path where the tool records the
// result itself.
func verdictFromMeasurement(m *Measurement) (string, string) {
	switch {
	case !m.Equal:
		return VerdictDifferent, "the two statements returned different rows"
	case m.BeforeRows == 0 && m.AfterRows == 0:
		// Two empty results are equal whatever the statements do. Nothing was compared.
		return VerdictUnverified, "both statements returned no rows, so nothing was compared; try values that return rows"
	case m.Speedup >= minProvenSpeedup:
		return VerdictProven, fmt.Sprintf("same rows, %.2fx faster", m.Speedup)
	default:
		return VerdictNotFaster, fmt.Sprintf("same rows, but only %.2fx (proof needs at least %.1fx)", m.Speedup, minProvenSpeedup)
	}
}

func tidySQL(s string) string {
	s = strings.TrimSpace(s)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	return s
}

// Investigate is the whole pitch in one call: read the plan, name what is wrong, propose from the
// plan, and prove each proposal by comparing rows and time. Nothing is changed in the database.
func Investigate(ctx context.Context, be Backend, val *queryrunner.Validator, opt InvestigateOptions) (*Report, error) {
	if opt.MaxCandidates <= 0 {
		opt.MaxCandidates = defaultMaxCandidates
	}
	if opt.MaxProofs <= 0 {
		opt.MaxProofs = defaultMaxProofs
	}

	sql := tidySQL(opt.SQL)
	var queryID *int64
	if sql == "" && opt.QueryID != nil {
		rows, err := be.Top(ctx, 500)
		if err != nil {
			return nil, fmt.Errorf("look up query %d: %w", *opt.QueryID, err)
		}
		for _, r := range rows {
			if r.QueryID == *opt.QueryID {
				sql = tidySQL(r.Query)
				break
			}
		}
		if sql == "" {
			return nil, fmt.Errorf("query %d is not among the top 500 statements", *opt.QueryID)
		}
	}
	queryID = opt.QueryID
	if sql == "" {
		return nil, errors.New("give a statement, or --queryid from `pqn top`")
	}
	if val != nil {
		if err := val.Validate(sql); err != nil {
			return nil, fmt.Errorf("the statement is not accepted: %w", err)
		}
	}

	report := &Report{SQL: sql}
	execSQL := sql
	hasParams := placeholderRe.MatchString(sql)
	if hasParams && len(opt.Binds) > 0 {
		sub, err := queryrunner.SubstituteParams(sql, opt.Binds)
		if err != nil {
			return nil, fmt.Errorf("substitute --bind values: %w", err)
		}
		execSQL = sub
		report.ExecutedSQL = sub
	}
	canExecute := !placeholderRe.MatchString(execSQL)
	report.GenericPlan = !canExecute
	if !canExecute {
		report.Notes = append(report.Notes,
			"The statement has $n placeholders, so it was planned generically and cannot be executed. Pass --bind for each placeholder to measure a proposal.")
	}

	planJSON, err := be.Plan(ctx, execSQL)
	if err != nil {
		return nil, fmt.Errorf("plan the statement: %w", err)
	}
	analysis, err := be.Analyze(ctx, planJSON)
	if err != nil {
		return nil, err
	}
	report.TotalCost = analysis.TotalCost
	report.Findings = []FindingLine{}
	report.Candidates = []CandidateResult{} // JSON gets [], not null, when nothing is proposed

	// The ledger: one investigation, one piece of evidence at a time. A failure to record must not
	// hide the analysis, so it becomes a note.
	record := !opt.NoRecord
	if record {
		id, err := be.RecordInvestigation(ctx, sql, opt.Title, queryID)
		if err != nil {
			report.Notes = append(report.Notes, "Not recorded in the ledger: "+trimErr(err))
			record = false
		} else {
			report.InvestigationID = id
			if e := be.RecordEvidence(ctx, id, "plan", json.RawMessage(planJSON)); e != nil {
				report.Notes = append(report.Notes, "Plan not recorded: "+trimErr(e))
			}
		}
	}

	// Propose from the plan.
	rewrites := queryrunner.SuggestRewrites(execSQL, analysis.Findings)
	var scored []queryrunner.ScoredCandidate
	byKey := map[string]*CandidateResult{}
	usedByRewrites := map[string]bool{}
	for _, c := range rewrites {
		if len(scored) >= opt.MaxCandidates {
			break
		}
		cand := &CandidateResult{Kind: queryrunner.CandidateKindSQLRewrite, SQL: c.SQL, Category: c.Category,
			Confidence: c.Confidence, Rationale: c.Rationale, CostBefore: analysis.TotalCost, Verdict: VerdictUnverified}
		byKey[c.SQL] = cand
		if val != nil {
			if err := val.Validate(c.SQL); err != nil {
				cand.Error = "rejected by the validator: " + trimErr(err)
				cand.Reason = cand.Error
				report.Candidates = append(report.Candidates, *cand)
				continue
			}
		}
		candPlan, err := be.Plan(ctx, c.SQL)
		if err != nil {
			cand.Error = "could not be planned: " + trimErr(err)
			cand.Reason = cand.Error
			report.Candidates = append(report.Candidates, *cand)
			continue
		}
		for _, name := range indexNamesInPlan(candPlan) {
			usedByRewrites[name] = true
		}
		after, err := queryrunner.MetricsFromPlan(candPlan)
		if err != nil {
			cand.Error = trimErr(err)
			cand.Reason = cand.Error
			report.Candidates = append(report.Candidates, *cand)
			continue
		}
		var improved []string
		if cmp, err := queryrunner.ComparePlans(planJSON, candPlan); err == nil {
			improved = cmp.Diff.Improved
		}
		sc := queryrunner.ScoreSQLRewrite(c, analysis.Metrics, after, improved)
		scored = append(scored, sc)
		cand.CostAfter = after.TotalCost
		cand.Improved = improved
	}
	ranked := queryrunner.RankScoredCandidates(scored)

	// A finding that says an index is unused, when a proposed rewrite uses it, would send a person
	// to drop the very index the fix needs. It looks unused only because this statement cannot use it.
	findings, withheld := withoutUsedIndexHealth(analysis.Findings, usedByRewrites)
	for _, name := range withheld {
		report.Notes = append(report.Notes, fmt.Sprintf(
			"Index %s looks unused only because this statement cannot use it (its filter wraps the column). A proposed rewrite does use it, so the finding and its DROP INDEX suggestion were withheld.", name))
	}
	ordered := append([]queryrunner.PlanFinding(nil), findings...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return findingPriority(ordered[i].Category) < findingPriority(ordered[j].Category)
	})
	for _, f := range ordered {
		report.Findings = append(report.Findings, FindingLine{
			Category: f.Category, Confidence: f.Confidence, Relation: f.Relation, Message: f.Message, Evidence: f.Evidence,
		})
	}
	if record {
		if e := be.RecordEvidence(ctx, report.InvestigationID, "findings", report.Findings); e != nil {
			report.Notes = append(report.Notes, "Findings not recorded: "+trimErr(e))
		}
	}

	// Prove the best proposals. A proposal that returns different rows is reported as such and
	// never as an improvement.
	proofs := 0
	for _, sc := range ranked {
		cand := byKey[sc.SQL]
		if cand == nil {
			continue
		}
		if !canExecute {
			cand.Reason = "not measured: the statement has $n placeholders (pass --bind)"
		} else if proofs >= opt.MaxProofs {
			cand.Reason = "not measured: only the best proposals are measured (--max-proofs)"
		} else {
			proofs++
			proveCandidate(ctx, be, report, cand, execSQL, record)
		}
	}
	for _, sc := range ranked {
		if cand := byKey[sc.SQL]; cand != nil {
			report.Candidates = append(report.Candidates, *cand)
		}
	}

	// Index proposals are review-only: nothing here creates an index, so nothing can be measured.
	for _, ic := range queryrunner.CollectIndexDDLCandidates(findings) {
		report.Candidates = append(report.Candidates, CandidateResult{
			Kind: queryrunner.CandidateKindIndexDDL, DDL: ic.DDL, Category: ic.Category, Confidence: ic.Confidence,
			Rationale: ic.Rationale, CostBefore: analysis.TotalCost, Verdict: VerdictUnverified,
			Reason: "review only: pqn never creates an index. Create it on a copy, then run `pqn investigate` again",
		})
	}

	for _, c := range report.Candidates {
		if c.Verdict == VerdictProven {
			report.Proven++
		}
	}
	if len(report.Candidates) == 0 {
		report.Notes = append(report.Notes, "No proposal from the plan. The statement may already be fine, or the shape is not one the engine rewrites.")
	}
	return report, nil
}

// proveCandidate measures one proposal. With no replica the database does it in one call, so the
// verdict is computed and recorded by the database. With a replica the heavy work runs there and
// the tool records the result on the primary, which makes it attributable but not tamper-proof.
func proveCandidate(ctx context.Context, be Backend, report *Report, cand *CandidateResult, execSQL string, record bool) {
	note := cand.Category
	if record && !be.HasReplica() {
		p, err := be.Prove(ctx, report.InvestigationID, execSQL, cand.SQL, note)
		if err != nil {
			cand.Verdict, cand.Reason, cand.Error = VerdictUnverified, "proof failed: "+trimErr(err), trimErr(err)
			return
		}
		cand.Verdict, cand.Reason, cand.CostAfter = p.Verdict, p.Reason, p.CostAfter
		cand.ProofSource = "recorded by the database"
		if p.Measured != nil {
			cand.Rows, cand.BeforeMs, cand.AfterMs, cand.Speedup = p.Measured.AfterRows, p.Measured.BeforeMs, p.Measured.AfterMs, p.Measured.Speedup
		}
		return
	}
	m, err := be.MeasurePair(ctx, execSQL, cand.SQL)
	if err != nil {
		cand.Verdict, cand.Reason, cand.Error = VerdictUnverified, "measurement failed: "+trimErr(err), trimErr(err)
		return
	}
	cand.Verdict, cand.Reason = verdictFromMeasurement(m)
	cand.Rows, cand.BeforeMs, cand.AfterMs, cand.Speedup = m.AfterRows, m.BeforeMs, m.AfterMs, m.Speedup
	cand.ProofSource = "measured on the primary, not recorded"
	if be.HasReplica() {
		cand.ProofSource = "measured on the replica"
	}
	if record {
		payload := map[string]any{
			"verdict": cand.Verdict, "reason": cand.Reason, "before_sql": execSQL, "after_sql": cand.SQL,
			"cost_before": cand.CostBefore, "cost_after": cand.CostAfter, "note": note,
			"measurement": m, "source": "pqn tool, measured on the replica and recorded on the primary",
		}
		if err := be.RecordEvidence(ctx, report.InvestigationID, "proof", payload); err != nil {
			report.Notes = append(report.Notes, "Proof not recorded: "+trimErr(err))
		} else {
			cand.ProofSource += ", recorded on the primary"
		}
	}
}

// trimErr keeps the first line of a database error, which is the part a person needs.
func trimErr(err error) string {
	s := err.Error()
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// findingPriority orders findings so the cause comes before the symptoms.
func findingPriority(category string) int {
	switch category {
	case queryrunner.CategorySeqScan:
		return 0
	case queryrunner.CategoryIndexCandidate:
		return 1
	case queryrunner.CategorySortSpill, queryrunner.CategoryHashBatches, queryrunner.CategoryLoopInflation:
		return 2
	case queryrunner.CategoryCardinality, queryrunner.CategorySelectivity, queryrunner.CategoryStaleStats:
		return 3
	case queryrunner.CategoryHighCost:
		return 8
	case queryrunner.CategoryIndexHealth:
		return 9
	}
	return 5
}

var (
	indexNameRe = regexp.MustCompile(`"Index Name"\s*:\s*"([^"]+)"`)
	dropIndexRe = regexp.MustCompile(`(?im)^\s*DROP INDEX(?: CONCURRENTLY)?(?: IF EXISTS)?\s+(?:[a-z_0-9"]+\.)?"?([A-Za-z0-9_]+)"?`)
)

// indexNamesInPlan lists the indexes a plan scans.
func indexNamesInPlan(plan json.RawMessage) []string {
	var out []string
	for _, m := range indexNameRe.FindAllSubmatch(plan, -1) {
		out = append(out, string(m[1]))
	}
	return out
}

// droppedIndexName returns the index a DROP INDEX statement names, or "".
func droppedIndexName(ddl string) string {
	if m := dropIndexRe.FindStringSubmatch(ddl); m != nil {
		return m[1]
	}
	return ""
}

// withoutUsedIndexHealth drops the index_health findings that recommend dropping an index the
// proposed rewrites use. It returns the findings that remain and the names it withheld.
func withoutUsedIndexHealth(findings []queryrunner.PlanFinding, used map[string]bool) ([]queryrunner.PlanFinding, []string) {
	var kept []queryrunner.PlanFinding
	var withheld []string
	seen := map[string]bool{}
	for _, f := range findings {
		if f.Category == queryrunner.CategoryIndexHealth && f.IndexAdvice != nil {
			if name := droppedIndexName(f.IndexAdvice.CandidateDDL); name != "" && used[name] {
				if !seen[name] {
					seen[name] = true
					withheld = append(withheld, name)
				}
				continue
			}
		}
		kept = append(kept, f)
	}
	return kept, withheld
}
