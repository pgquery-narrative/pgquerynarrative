package pqncli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/pgquerynarrative/pgquerynarrative/app/queryrunner"
)

const seqPlan = `[{"Plan": {"Node Type": "Seq Scan", "Relation Name": "orders", "Schema": "shop", "Alias": "orders",
 "Startup Cost": 0.0, "Total Cost": 24373.0, "Plan Rows": 7500, "Plan Width": 60,
 "Filter": "(date_trunc('day'::text, orders.created_at) = '2025-03-01 00:00:00+00'::timestamp with time zone)"}}]`

const idxPlan = `[{"Plan": {"Node Type": "Index Scan", "Relation Name": "orders", "Schema": "shop", "Alias": "orders",
 "Index Name": "orders_created_idx", "Startup Cost": 0.4, "Total Cost": 9516.0, "Plan Rows": 4110, "Plan Width": 60,
 "Index Cond": "((created_at >= '2025-03-01'::timestamptz) AND (created_at < '2025-03-02'::timestamptz))"}}]`

const dayQuery = "SELECT * FROM shop.orders WHERE date_trunc('day', created_at) = '2025-03-01'"

// fakeBackend records what was asked of it and answers from fields.
type fakeBackend struct {
	replica    bool
	measure    *Measurement
	measureErr error
	recordErr  error
	proof      *Proof
	planCalls  []string
	evidence   []string
	measured   int
	proved     int
	extra      []queryrunner.PlanFinding // findings added to whatever the plan yields
}

func (f *fakeBackend) HasReplica() bool { return f.replica }
func (f *fakeBackend) Top(context.Context, int) ([]TopRow, error) {
	return []TopRow{{QueryID: 42, Query: "SELECT * FROM shop.orders WHERE date_trunc('day', created_at) = $1", Calls: 9, MeanMs: 150}}, nil
}
func (f *fakeBackend) Plan(_ context.Context, sql string) (json.RawMessage, error) {
	f.planCalls = append(f.planCalls, sql)
	if strings.Contains(sql, "date_trunc") {
		return json.RawMessage(seqPlan), nil
	}
	return json.RawMessage(idxPlan), nil
}
func (f *fakeBackend) Analyze(ctx context.Context, plan json.RawMessage) (*queryrunner.PlanAnalysis, error) {
	a, err := queryrunner.AnalyzePlanJSON(ctx, nil, plan)
	if err == nil {
		a.Findings = append(a.Findings, f.extra...)
	}
	return a, err
}
func (f *fakeBackend) MeasurePair(context.Context, string, string) (*Measurement, error) {
	f.measured++
	return f.measure, f.measureErr
}
func (f *fakeBackend) Run(context.Context, string, int) (*RunResult, error) {
	return &RunResult{Columns: []string{"n"}, Rows: []map[string]any{{"n": float64(3)}}}, nil
}
func (f *fakeBackend) RecordInvestigation(context.Context, string, string, *int64) (int64, error) {
	if f.recordErr != nil {
		return 0, f.recordErr
	}
	return 7, nil
}
func (f *fakeBackend) RecordEvidence(_ context.Context, _ int64, kind string, _ any) error {
	f.evidence = append(f.evidence, kind)
	return nil
}
func (f *fakeBackend) Prove(context.Context, int64, string, string, string) (*Proof, error) {
	f.proved++
	return f.proof, nil
}
func (f *fakeBackend) Investigations(context.Context, int) ([]Investigation, error) { return nil, nil }
func (f *fakeBackend) Evidence(context.Context, int64) ([]EvidenceRow, error)       { return nil, nil }
func (f *fakeBackend) Doctor(context.Context) ([]DoctorRow, error)                  { return nil, nil }
func (f *fakeBackend) Close()                                                       {}

func validator() *queryrunner.Validator { return queryrunner.NewValidator(nil, 100000) }

func TestInvestigateProvesAProposalWithoutAReplica(t *testing.T) {
	be := &fakeBackend{proof: &Proof{Verdict: VerdictProven, Reason: "same rows, 21.28x faster", CostBefore: 24373, CostAfter: 9516,
		Measured: &Measurement{Equal: true, AfterRows: 4110, BeforeMs: 152, AfterMs: 7.1, Speedup: 21.28}}}
	rep, err := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery})
	if err != nil {
		t.Fatal(err)
	}
	if rep.InvestigationID != 7 {
		t.Errorf("investigation id = %d, want 7", rep.InvestigationID)
	}
	if rep.Proven != 1 || rep.ExitCode() != 0 {
		t.Fatalf("proven = %d exit = %d, want 1 and 0; report %+v", rep.Proven, rep.ExitCode(), rep.Candidates)
	}
	if be.proved != 1 || be.measured != 0 {
		t.Errorf("without a replica the database proves it: proved=%d measured=%d", be.proved, be.measured)
	}
	if strings.Join(be.evidence, ",") != "plan,findings" {
		t.Errorf("evidence recorded by the tool = %v, want plan,findings (the proof is recorded by the database)", be.evidence)
	}
	var found bool
	for _, f := range rep.Findings {
		if f.Category == queryrunner.CategorySeqScan {
			found = true
		}
	}
	if !found {
		t.Errorf("the sequential scan should be a finding: %+v", rep.Findings)
	}
}

func TestNoProposalsIsAnEmptyJSONArray(t *testing.T) {
	rep, err := Investigate(context.Background(), &fakeBackend{}, validator(), InvestigateOptions{SQL: "SELECT id FROM shop.orders WHERE id = 1"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(rep)
	if !strings.Contains(string(raw), `"candidates":[]`) || !strings.Contains(string(raw), `"findings":[`) {
		t.Errorf("candidates and findings must be arrays, never null: %s", raw)
	}
}

func TestInvestigateOnAReplicaMeasuresThereAndRecordsOnThePrimary(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: true, AfterRows: 10, BeforeMs: 100, AfterMs: 10, Speedup: 10}}
	rep, err := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery})
	if err != nil {
		t.Fatal(err)
	}
	if be.measured == 0 || be.proved != 0 {
		t.Errorf("a replica should measure and the tool should record: measured=%d proved=%d", be.measured, be.proved)
	}
	if rep.Proven != 1 {
		t.Errorf("proven = %d, want 1", rep.Proven)
	}
	if !strings.Contains(strings.Join(be.evidence, ","), "proof") {
		t.Errorf("the tool should record the proof on the primary: %v", be.evidence)
	}
}

func TestDifferentRowsAreNeverAnImprovement(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: false, BeforeMs: 100, AfterMs: 1, Speedup: 100}}
	rep, err := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Proven != 0 || rep.ExitCode() != 2 {
		t.Fatalf("a wrong answer must not be proven: proven=%d exit=%d", rep.Proven, rep.ExitCode())
	}
	var out bytes.Buffer
	rep.Render(&out)
	if !strings.Contains(out.String(), "different rows and must not be used") {
		t.Errorf("the report should say so:\n%s", out.String())
	}
}

func TestSameRowsButNotFasterIsNotProven(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: true, BeforeMs: 100, AfterMs: 95, Speedup: 1.05}}
	rep, _ := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery})
	if rep.Proven != 0 {
		t.Fatalf("1.05x is not a proof, got %d proven", rep.Proven)
	}
	if rep.Candidates[0].Verdict != VerdictNotFaster {
		t.Errorf("verdict = %s, want NotFaster", rep.Candidates[0].Verdict)
	}
}

func TestPlaceholdersAreNotExecutedWithoutBinds(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: true, Speedup: 50}}
	rep, err := Investigate(context.Background(), be, validator(),
		InvestigateOptions{SQL: "SELECT * FROM shop.orders WHERE date_trunc('day', created_at) = $1"})
	if err != nil {
		t.Fatal(err)
	}
	if be.measured != 0 {
		t.Fatalf("a statement with $1 must not be executed, measured %d time(s)", be.measured)
	}
	if !rep.GenericPlan || rep.Proven != 0 {
		t.Errorf("generic=%v proven=%d", rep.GenericPlan, rep.Proven)
	}
}

func TestBindsMakeAPlaceholderStatementMeasurable(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: true, BeforeMs: 100, AfterMs: 5, Speedup: 20}}
	rep, err := Investigate(context.Background(), be, validator(), InvestigateOptions{
		SQL: "SELECT * FROM shop.orders WHERE date_trunc('day', created_at) = $1", Binds: []string{"2025-03-01"}})
	if err != nil {
		t.Fatal(err)
	}
	if rep.ExecutedSQL == "" || strings.Contains(rep.ExecutedSQL, "$1") {
		t.Fatalf("binds should be substituted: %q", rep.ExecutedSQL)
	}
	if be.measured == 0 {
		t.Errorf("with binds the statement can be measured")
	}
}

func TestQueryIDIsLookedUpInTop(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: true, Speedup: 5}}
	id := int64(42)
	rep, err := Investigate(context.Background(), be, validator(), InvestigateOptions{QueryID: &id})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rep.SQL, "$1") {
		t.Errorf("the statement should come from top: %q", rep.SQL)
	}
	missing := int64(1)
	if _, err := Investigate(context.Background(), be, validator(), InvestigateOptions{QueryID: &missing}); err == nil {
		t.Errorf("an unknown queryid should fail")
	}
}

func TestALedgerFailureDoesNotHideTheAnalysis(t *testing.T) {
	be := &fakeBackend{replica: true, recordErr: errors.New("permission denied for function record_investigation"),
		measure: &Measurement{Equal: true, BeforeMs: 100, AfterMs: 10, Speedup: 10}}
	rep, err := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery})
	if err != nil {
		t.Fatal(err)
	}
	if rep.InvestigationID != 0 || rep.Proven != 1 {
		t.Errorf("analysis should survive: id=%d proven=%d", rep.InvestigationID, rep.Proven)
	}
	if len(rep.Notes) == 0 || !strings.Contains(strings.Join(rep.Notes, " "), "Not recorded") {
		t.Errorf("the failure should be a note: %v", rep.Notes)
	}
}

func TestNoRecordWritesNothing(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: true, BeforeMs: 100, AfterMs: 10, Speedup: 10}}
	rep, _ := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery, NoRecord: true})
	if len(be.evidence) != 0 || rep.InvestigationID != 0 {
		t.Errorf("--no-record must not write: evidence=%v id=%d", be.evidence, rep.InvestigationID)
	}
}

func TestTheValidatorStopsAWriteBeforeAnythingIsSent(t *testing.T) {
	be := &fakeBackend{}
	for _, sql := range []string{"DELETE FROM shop.orders", "SELECT 1; SELECT 2", "SELECT pg_read_file('/etc/passwd')", ""} {
		if _, err := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: sql}); err == nil {
			t.Errorf("%q should be refused", sql)
		}
	}
	if len(be.planCalls) != 0 {
		t.Errorf("nothing should reach the database: %v", be.planCalls)
	}
}

func TestReportRendersEverySection(t *testing.T) {
	be := &fakeBackend{proof: &Proof{Verdict: VerdictProven, Reason: "same rows, 21.28x faster", CostAfter: 9516,
		Measured: &Measurement{Equal: true, AfterRows: 4110, BeforeMs: 152, AfterMs: 7.1, Speedup: 21.28}}}
	rep, _ := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery, Title: "slow day"})
	var out bytes.Buffer
	rep.Render(&out)
	for _, want := range []string{"investigation #7", "What the plan shows", "Proposed from the plan", "Proof (same rows, then faster)", "Verdict", "pqn evidence 7", "Proven"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("report is missing %q:\n%s", want, out.String())
		}
	}
}

func TestMainCommands(t *testing.T) {
	be := &fakeBackend{replica: true, measure: &Measurement{Equal: true, BeforeMs: 100, AfterMs: 10, Speedup: 10}}
	connect := func(context.Context, string, string) (Backend, error) { return be, nil }
	env := func(string) string { return "" }
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		code := Main(args, &out, &errb, env, connect)
		return code, out.String(), errb.String()
	}
	if code, out, _ := run("help"); code != 0 || !strings.Contains(out, "investigate") {
		t.Errorf("help: %d %q", code, out)
	}
	if code, out, _ := run("version"); code != 0 || !strings.Contains(out, "pqn") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, _, errs := run("frobnicate"); code != 1 || !strings.Contains(errs, "unknown command") {
		t.Errorf("unknown command: %d %q", code, errs)
	}
	if code, out, _ := run("top"); code != 0 || !strings.Contains(out, "pqn investigate --queryid 42") {
		t.Errorf("top: %d %q", code, out)
	}
	if code, out, _ := run("run", "--sql", "SELECT count(*) AS n FROM orders"); code != 0 || !strings.Contains(out, "(1 row(s))") {
		t.Errorf("run: %d %q", code, out)
	}
	if code, out, _ := run("plan", "--sql", dayQuery); code != 0 || !strings.Contains(out, "seq_scan") {
		t.Errorf("plan: %d %q", code, out)
	}
	if code, _, _ := run("investigate", "--sql", dayQuery); code != 0 {
		t.Errorf("investigate exit = %d, want 0 when proven", code)
	}
	if code, _, errs := run("run", "--sql", "DROP TABLE x"); code != 1 || !strings.Contains(errs, "not accepted") {
		t.Errorf("a write must be refused by the tool: %d %q", code, errs)
	}
	if code, out, _ := run("prove", "--before", dayQuery, "--after",
		"SELECT * FROM shop.orders WHERE created_at >= '2025-03-01' AND created_at < '2025-03-02'"); code != 0 || !strings.Contains(out, "Verdict: Proven") {
		t.Errorf("prove: %d %q", code, out)
	}
	if code, out, _ := run("evidence", "7", "--json"); code != 0 || strings.Contains(out, "No evidence") {
		t.Errorf("a flag after the id must still apply: %d %q", code, out)
	}
	if code, _, errs := run("prove", "--before", dayQuery); code != 1 || !strings.Contains(errs, "--after") {
		t.Errorf("prove without --after: %d %q", code, errs)
	}
}

func TestDroppedIndexNameAndIndexesInPlan(t *testing.T) {
	cases := map[string]string{
		"-- CANDIDATE for review\nDROP INDEX CONCURRENTLY IF EXISTS \"orders_created_idx\";": "orders_created_idx",
		"DROP INDEX orders_status_idx;":                        "orders_status_idx",
		"DROP INDEX CONCURRENTLY \"shop\".\"orders_pkey_dup\"": "orders_pkey_dup",
		"CREATE INDEX CONCURRENTLY ON t (a)":                   "",
	}
	for ddl, want := range cases {
		if got := droppedIndexName(ddl); got != want {
			t.Errorf("droppedIndexName(%q) = %q, want %q", ddl, got, want)
		}
	}
	names := indexNamesInPlan(json.RawMessage(idxPlan))
	if len(names) != 1 || names[0] != "orders_created_idx" {
		t.Errorf("indexNamesInPlan = %v, want [orders_created_idx]", names)
	}
	if len(indexNamesInPlan(json.RawMessage(seqPlan))) != 0 {
		t.Errorf("a sequential scan uses no index")
	}
}

func TestFindingsAreOrderedCauseFirst(t *testing.T) {
	order := []string{queryrunner.CategorySeqScan, queryrunner.CategoryIndexCandidate, queryrunner.CategorySortSpill,
		queryrunner.CategoryCardinality, queryrunner.CategoryHighCost, queryrunner.CategoryIndexHealth}
	for i := 1; i < len(order); i++ {
		if findingPriority(order[i-1]) >= findingPriority(order[i]) {
			t.Errorf("%s should come before %s", order[i-1], order[i])
		}
	}
}

func TestTableAlignsAndClips(t *testing.T) {
	var out bytes.Buffer
	Table(&out, []string{"a", "bb"}, [][]string{{"1", "a very long cell that gets clipped"}}, 10)
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) != 3 || !strings.HasSuffix(lines[2], "…") {
		t.Errorf("unexpected table:\n%s", out.String())
	}
}

func TestNotesWrapWithAHangingIndent(t *testing.T) {
	r := &Report{SQL: "SELECT 1", Notes: []string{strings.Repeat("a long note about an index ", 8)}}
	var out bytes.Buffer
	r.Render(&out)
	if n := strings.Count(out.String(), "note:"); n != 1 {
		t.Errorf("a wrapped note should start with \"note:\" once, got %d:\n%s", n, out.String())
	}
}

func TestAnIndexTheRewriteUsesIsNeverSuggestedForRemoval(t *testing.T) {
	drop := queryrunner.PlanFinding{
		Category: queryrunner.CategoryIndexHealth, Confidence: "medium", Relation: "orders",
		Message:  "Index orders_created_idx has 0 recorded scans",
		Evidence: []string{"index=orders_created_idx"},
		IndexAdvice: &queryrunner.IndexAdvice{
			CandidateDDL: "-- CANDIDATE\nDROP INDEX CONCURRENTLY IF EXISTS \"orders_created_idx\";", Issues: []string{"low_use"}},
	}
	other := queryrunner.PlanFinding{
		Category: queryrunner.CategoryIndexHealth, Confidence: "medium", Relation: "orders",
		Message:     "Index orders_dup_idx duplicates orders_pkey",
		IndexAdvice: &queryrunner.IndexAdvice{CandidateDDL: "DROP INDEX CONCURRENTLY IF EXISTS \"orders_dup_idx\";", Issues: []string{"duplicate_prefix"}},
	}
	be := &fakeBackend{replica: true, extra: []queryrunner.PlanFinding{drop, other},
		measure: &Measurement{Equal: true, BeforeMs: 100, AfterMs: 10, Speedup: 10}}
	rep, err := Investigate(context.Background(), be, validator(), InvestigateOptions{SQL: dayQuery, NoRecord: true})
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	rep.Render(&text)
	out := text.String()
	if strings.Contains(out, "orders_created_idx\";") || strings.Contains(out, "Index orders_created_idx has 0 recorded scans") {
		t.Errorf("the index the rewrite uses must not be suggested for removal:\n%s", out)
	}
	if !strings.Contains(out, "orders_dup_idx") {
		t.Errorf("an unrelated removal suggestion must survive:\n%s", out)
	}
	if !strings.Contains(strings.Join(rep.Notes, " "), "orders_created_idx looks unused only because") {
		t.Errorf("the withholding should be explained: %v", rep.Notes)
	}
}

func TestJSONFieldsAreSnakeCase(t *testing.T) {
	for name, v := range map[string]any{
		"top": TopRow{}, "doctor": DoctorRow{}, "run": RunResult{}, "investigation": Investigation{},
		"evidence": EvidenceRow{}, "measurement": Measurement{}, "report": Report{}, "candidate": CandidateResult{},
	} {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		for k := range m {
			if k != strings.ToLower(k) {
				t.Errorf("%s: JSON field %q is not lower case: a Go field name leaked into the output", name, k)
			}
		}
	}
}
