package queryrunner

import (
	"encoding/json"
	"strings"
	"testing"
)

// loadPlanRoot decodes a testdata/explain/*.json fixture into the raw plan
// node map, the same shape walkPlanNode/detectPlanSignals operate on.
func loadPlanRoot(t *testing.T, fixture string) map[string]interface{} {
	t.Helper()
	data := loadExplainFixture(t, fixture)
	var roots explainRoot
	if err := json.Unmarshal(data, &roots); err != nil {
		t.Fatalf("failed to unmarshal fixture %s: %v", fixture, err)
	}
	if len(roots) == 0 || roots[0].Plan == nil {
		t.Fatalf("fixture %s has no plan", fixture)
	}
	return roots[0].Plan
}

func TestExtractFilterColumns(t *testing.T) {
	tests := []struct {
		name   string
		filter string
		want   []string
	}{
		{name: "empty", filter: "", want: nil},
		{name: "simple equality with cast", filter: "(region = 'North'::text)", want: []string{"region"}},
		{name: "integer literal", filter: "(passenger_count = 8)", want: []string{"passenger_count"}},
		{name: "qualified column with cast", filter: "((a.status)::text = 'active'::text)", want: []string{"status"}},
		{name: "range predicate", filter: "(created_at >= '2024-01-01'::date)", want: []string{"created_at"}},
		{name: "conjunction", filter: "((price > 100) AND (qty < 5))", want: []string{"price", "qty"}},
		{name: "is not null", filter: "(email IS NOT NULL)", want: []string{"email"}},
		{name: "no comparison", filter: "(true)", want: nil},
		// Multi-word type names must be stripped whole. Leaving the tail behind
		// makes the trailing word ("zone", "precision", "varying") look like the
		// column being compared, which produced index advice for columns that do
		// not exist.
		{
			name:   "date_trunc wrap with timestamptz cast",
			filter: "(date_trunc('month'::text, (date)::timestamp with time zone) = '2025-01-01'::date)",
			want:   []string{"date"},
		},
		{
			name:   "timestamp without time zone",
			filter: "(created_at >= '2024-01-01'::timestamp without time zone)",
			want:   []string{"created_at"},
		},
		{
			name:   "timestamptz with length modifier",
			filter: "((ts)::timestamp(3) with time zone = '2024-01-01'::timestamp with time zone)",
			want:   []string{"ts"},
		},
		{name: "double precision", filter: "(lat > '40.7'::double precision)", want: []string{"lat"}},
		{
			name:   "character varying with length",
			filter: "((a.code)::character varying(8) = 'ABC'::character varying)",
			want:   []string{"code"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractFilterColumns(tt.filter)
			if !sameColumnSet(got, tt.want) || len(got) != len(tt.want) {
				t.Fatalf("extractFilterColumns(%q) = %v, want %v", tt.filter, got, tt.want)
			}
		})
	}
}

func TestExtractFilterColumns_RealFixtures(t *testing.T) {
	seqScan := loadPlanRoot(t, "seq_scan.json")
	child := seqScan["Plans"].([]interface{})[0].(map[string]interface{})
	filter, _ := child["Filter"].(string)
	got := extractFilterColumns(filter)
	if len(got) != 1 || got[0] != "region" {
		t.Fatalf("seq_scan.json filter columns = %v, want [region]", got)
	}

	rowsRemoved := loadPlanRoot(t, "rows_removed.json")
	filter, _ = rowsRemoved["Filter"].(string)
	got = extractFilterColumns(filter)
	if len(got) != 1 || got[0] != "passenger_count" {
		t.Fatalf("rows_removed.json filter columns = %v, want [passenger_count]", got)
	}
}

func TestExtractSortColumns_RealFixture(t *testing.T) {
	sortSpill := loadPlanRoot(t, "sort_spill.json")
	got := extractSortColumns(sortSpill)
	if len(got) != 1 || got[0] != "sold_at" {
		t.Fatalf("sort_spill.json sort columns = %v, want [sold_at]", got)
	}
}

func TestExtractJoinColumns_RealFixture(t *testing.T) {
	hashJoin := loadPlanRoot(t, "hash_join_cond.json")
	got := extractJoinColumns(hashJoin)
	if !sameColumnSet(got, []string{"zone_id", "id"}) {
		t.Fatalf("hash_join_cond.json join columns = %v, want [zone_id id]", got)
	}
}

func TestNormalizeColumnName(t *testing.T) {
	tests := map[string]string{
		"region":                  "region",
		"sales.sold_at":           "sold_at",
		"sold_at DESC":            "sold_at",
		"sold_at ASC":             "sold_at",
		"sold_at DESC NULLS LAST": "sold_at",
		"\"Region\"":              "region",
		"a.status":                "status",
		"(a + b)":                 "",
		"":                        "",
	}
	for in, want := range tests {
		if got := normalizeColumnName(in); got != want {
			t.Errorf("normalizeColumnName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsColumnPrefix(t *testing.T) {
	tests := []struct {
		name       string
		cols       []string
		keyColumns []string
		want       bool
	}{
		{"exact prefix single col", []string{"region"}, []string{"region", "sold_at"}, true},
		{"unordered set match", []string{"b", "a"}, []string{"a", "b", "c"}, true},
		{"too long", []string{"a", "b", "c"}, []string{"a", "b"}, false},
		{"not a prefix", []string{"sold_at"}, []string{"region", "sold_at"}, false},
		{"empty cols", nil, []string{"a"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isColumnPrefix(tt.cols, tt.keyColumns); got != tt.want {
				t.Errorf("isColumnPrefix(%v, %v) = %v, want %v", tt.cols, tt.keyColumns, got, tt.want)
			}
		})
	}
}

func TestIndexColumnCoverage(t *testing.T) {
	idx := IndexDefinition{KeyColumns: []string{"region", "sold_at"}, IncludeColumns: []string{"amount"}}
	if got := indexColumnCoverage(idx, []string{"region"}); got != coverageFull {
		t.Errorf("expected coverageFull, got %v", got)
	}
	if got := indexColumnCoverage(idx, []string{"region", "sold_at"}); got != coverageFull {
		t.Errorf("expected coverageFull for full prefix, got %v", got)
	}
	if got := indexColumnCoverage(idx, []string{"amount"}); got != coveragePartial {
		t.Errorf("expected coveragePartial via INCLUDE column, got %v", got)
	}
	if got := indexColumnCoverage(idx, []string{"customer_id"}); got != coverageNone {
		t.Errorf("expected coverageNone, got %v", got)
	}
	if got := indexColumnCoverage(idx, []string{"sold_at"}); got != coveragePartial {
		t.Errorf("expected coveragePartial (present but not leftmost), got %v", got)
	}
}

func TestPromoteIndexCandidateFinding(t *testing.T) {
	f := PlanFinding{
		Category:  CategorySeqScan,
		IsSeqScan: true,
		IndexAdvice: &IndexAdvice{
			Issues:       []string{"no_covering_index"},
			CandidateDDL: "CREATE INDEX ...",
		},
	}
	got := promoteIndexCandidateFinding(f)
	if got.Category != CategoryIndexCandidate {
		t.Fatalf("category = %q, want %q", got.Category, CategoryIndexCandidate)
	}
	// already_covered must not promote
	f.IndexAdvice.Issues = []string{"already_covered"}
	got = promoteIndexCandidateFinding(f)
	if got.Category != CategorySeqScan {
		t.Fatalf("already_covered must stay seq_scan, got %q", got.Category)
	}
}

// TestBuildIndexAdvice_NoCoveringIndex verifies that when no existing index
// covers a finding's implicated columns, advice proposes a candidate DDL
// clearly marked for expert review only, plus benefit/write/storage cost text.
func TestBuildIndexAdvice_NoCoveringIndex(t *testing.T) {
	f := PlanFinding{
		Schema:         "demo",
		Relation:       "sales",
		IsSeqScan:      true,
		RelatedColumns: []string{"region"},
	}
	st := tableCatalogStats{EstimatedRows: 500000, TotalBytes: 80 << 20}

	advice := buildIndexAdvice(f, st)
	if advice == nil {
		t.Fatal("expected advice, got nil")
	}
	if len(advice.Issues) != 1 || advice.Issues[0] != "no_covering_index" {
		t.Fatalf("expected issue no_covering_index, got %v", advice.Issues)
	}
	if advice.PotentialBenefit == "" {
		t.Error("expected potential benefit text")
	}
	if advice.WriteCost == "" || advice.StorageCost == "" {
		t.Error("expected write/storage cost text")
	}
	if !strings.Contains(advice.CandidateDDL, "CREATE INDEX CONCURRENTLY") {
		t.Errorf("expected candidate DDL, got %q", advice.CandidateDDL)
	}
	if !strings.Contains(strings.ToUpper(advice.CandidateDDL), "EXPERT REVIEW ONLY") {
		t.Errorf("candidate DDL must flag expert-review-only, got %q", advice.CandidateDDL)
	}
	if !strings.Contains(advice.CandidateDDL, `"demo"."sales"`) {
		t.Errorf("expected qualified table name in candidate DDL, got %q", advice.CandidateDDL)
	}
}

// TestBuildIndexAdvice_AlreadyCovered verifies that when an existing index
// already covers the columns as a leftmost prefix, no new-index DDL is drafted.
func TestBuildIndexAdvice_AlreadyCovered(t *testing.T) {
	f := PlanFinding{
		Schema:         "demo",
		Relation:       "sales",
		IsSeqScan:      true,
		RelatedColumns: []string{"region"},
	}
	st := tableCatalogStats{
		EstimatedRows: 500000,
		Indexes: []IndexDefinition{
			{Name: "idx_sales_region", KeyColumns: []string{"region", "sold_at"}, IsValid: true},
		},
	}

	advice := buildIndexAdvice(f, st)
	if advice == nil {
		t.Fatal("expected advice, got nil")
	}
	if len(advice.Issues) != 1 || advice.Issues[0] != "already_covered" {
		t.Fatalf("expected issue already_covered, got %v", advice.Issues)
	}
	if advice.CandidateDDL != "" {
		t.Errorf("expected no candidate DDL when already covered, got %q", advice.CandidateDDL)
	}
	if len(advice.RelatedIndexes) != 1 || advice.RelatedIndexes[0].Name != "idx_sales_region" {
		t.Fatalf("expected covering index attached, got %v", advice.RelatedIndexes)
	}
}

func TestBuildIndexAdvice_NoRelatedColumns(t *testing.T) {
	if got := buildIndexAdvice(PlanFinding{Schema: "demo", Relation: "sales"}, tableCatalogStats{}); got != nil {
		t.Fatalf("expected nil advice when no related columns, got %+v", got)
	}
}

func TestBuildIndexAdvice_SmallTableSkipsDDL(t *testing.T) {
	f := PlanFinding{
		Schema:         "demo",
		Relation:       "sales",
		IsSeqScan:      true,
		RelatedColumns: []string{"region"},
		Message:        "Sequential scan — filter: (region = 'North')",
	}
	st := tableCatalogStats{EstimatedRows: 50, TotalBytes: 8192}

	advice := buildIndexAdvice(f, st)
	if advice == nil {
		t.Fatal("expected advice")
	}
	if len(advice.Issues) != 1 || advice.Issues[0] != "small_table" {
		t.Fatalf("expected small_table issue, got %v", advice.Issues)
	}
	if advice.CandidateDDL != "" {
		t.Errorf("small table must not get candidate DDL, got %q", advice.CandidateDDL)
	}
}

func TestBuildIndexAdvice_SmallTableByBytesWhenRelTuplesZero(t *testing.T) {
	f := PlanFinding{
		Schema:         "demo",
		Relation:       "sales",
		IsSeqScan:      true,
		RelatedColumns: []string{"region"},
		Message:        "Sequential scan — filter: (region = 'North')",
	}
	// Partitioned parents often report reltuples=0 with a small total size.
	st := tableCatalogStats{EstimatedRows: 0, TotalBytes: 64 << 10}

	advice := buildIndexAdvice(f, st)
	if advice == nil {
		t.Fatal("expected advice")
	}
	if len(advice.Issues) != 1 || advice.Issues[0] != "small_table" {
		t.Fatalf("expected small_table issue, got %v", advice.Issues)
	}
	if advice.CandidateDDL != "" {
		t.Errorf("tiny relation must not get candidate DDL, got %q", advice.CandidateDDL)
	}
}

func TestBuildIndexAdvice_FunctionWrapBareColumn(t *testing.T) {
	f := PlanFinding{
		Schema:         "demo",
		Relation:       "sales",
		IsSeqScan:      true,
		RelatedColumns: []string{"date"},
		Message:        "Sequential scan — filter: (date_trunc('month'::text, date) = '2025-01-01'::date) — function-wrapped partition/index key blocks pruning",
	}
	st := tableCatalogStats{EstimatedRows: 500000, TotalBytes: 80 << 20}

	advice := buildIndexAdvice(f, st)
	if advice == nil {
		t.Fatal("expected advice")
	}
	if !strings.Contains(advice.PotentialBenefit, "bare column") {
		t.Errorf("expected bare-column guidance, got %q", advice.PotentialBenefit)
	}
	if !strings.Contains(advice.CandidateDDL, `"date"`) {
		t.Errorf("expected index on bare date column, got %q", advice.CandidateDDL)
	}
}

func TestIndexAdviceColumns_EqualityFirst(t *testing.T) {
	f := PlanFinding{
		RelatedColumns: []string{"sold_at", "region"},
		Message:        "Sequential scan — filter: ((region = 'North'::text) AND (sold_at >= '2025-01-01'::date))",
	}
	got := indexAdviceColumns(f)
	if len(got) != 2 || got[0] != "region" || got[1] != "sold_at" {
		t.Fatalf("expected equality column first, got %v", got)
	}
}

func TestPromoteIndexCandidateFinding_SmallTableSkipped(t *testing.T) {
	f := PlanFinding{
		Category:  CategorySeqScan,
		IsSeqScan: true,
		IndexAdvice: &IndexAdvice{
			Issues:       []string{"small_table"},
			CandidateDDL: "",
		},
	}
	got := promoteIndexCandidateFinding(f)
	if got.Category != CategorySeqScan {
		t.Fatalf("small_table must not promote, got %q", got.Category)
	}
}

// TestDetectIndexIssues_Invalid ensures invalid indexes are flagged with a
// DROP candidate for expert review, never an auto-applied statement.
func TestDetectIndexIssues_Invalid(t *testing.T) {
	indexes := []IndexDefinition{
		{Name: "idx_broken", KeyColumns: []string{"region"}, IsValid: false, SizeBytes: 1 << 20},
	}
	findings := detectIndexIssues("demo", "sales", indexes)
	if len(findings) != 1 {
		t.Fatalf("expected 1 finding, got %d: %+v", len(findings), findings)
	}
	f := findings[0]
	if f.Category != CategoryIndexHealth || f.Confidence != "high" {
		t.Fatalf("unexpected category/confidence: %+v", f)
	}
	if f.IndexAdvice == nil || f.IndexAdvice.Issues[0] != "invalid" {
		t.Fatalf("expected invalid issue, got %+v", f.IndexAdvice)
	}
	if !strings.Contains(f.IndexAdvice.CandidateDDL, "DROP INDEX CONCURRENTLY") {
		t.Errorf("expected DROP candidate DDL, got %q", f.IndexAdvice.CandidateDDL)
	}
	if !strings.Contains(strings.ToUpper(f.IndexAdvice.CandidateDDL), "EXPERT REVIEW ONLY") {
		t.Errorf("candidate DDL must flag expert-review-only, got %q", f.IndexAdvice.CandidateDDL)
	}
}

// TestDetectIndexIssues_LowUse covers the unique-vs-non-unique confidence split.
func TestDetectIndexIssues_LowUse(t *testing.T) {
	indexes := []IndexDefinition{
		{Name: "idx_unused", KeyColumns: []string{"customer_id"}, IsValid: true, IndexScans: 0, SizeBytes: 2 << 20},
		{Name: "uq_unused", KeyColumns: []string{"email"}, IsValid: true, IsUnique: true, IndexScans: 0, SizeBytes: 1 << 20},
		{Name: "idx_active", KeyColumns: []string{"sold_at"}, IsValid: true, IndexScans: 5000},
	}
	findings := detectIndexIssues("demo", "customers", indexes)

	byName := map[string]PlanFinding{}
	for _, f := range findings {
		if f.IndexAdvice != nil && len(f.IndexAdvice.RelatedIndexes) > 0 {
			byName[f.IndexAdvice.RelatedIndexes[0].Name] = f
		}
	}
	nonUnique, ok := byName["idx_unused"]
	if !ok {
		t.Fatal("expected low-use finding for idx_unused")
	}
	if nonUnique.Confidence != "medium" {
		t.Errorf("expected medium confidence for non-unique low-use index, got %q", nonUnique.Confidence)
	}
	unique, ok := byName["uq_unused"]
	if !ok {
		t.Fatal("expected low-use finding for uq_unused")
	}
	if unique.Confidence != "low" {
		t.Errorf("expected low confidence for unique low-use index (still enforces a constraint), got %q", unique.Confidence)
	}
	if _, ok := byName["idx_active"]; ok {
		t.Error("did not expect a low-use finding for an actively-scanned index")
	}
}

// TestDetectIndexIssues_DuplicatePrefix ensures a shorter index that is a
// leftmost prefix of a longer one is flagged as redundant.
func TestDetectIndexIssues_DuplicatePrefix(t *testing.T) {
	indexes := []IndexDefinition{
		{Name: "idx_region", KeyColumns: []string{"region"}, IsValid: true, SizeBytes: 1 << 20},
		{Name: "idx_region_sold_at", KeyColumns: []string{"region", "sold_at"}, IsValid: true, SizeBytes: 3 << 20},
	}
	findings := detectIndexIssues("demo", "sales", indexes)
	var dup *PlanFinding
	for i := range findings {
		if findings[i].IndexAdvice != nil && len(findings[i].IndexAdvice.Issues) > 0 && findings[i].IndexAdvice.Issues[0] == "duplicate_prefix" {
			dup = &findings[i]
		}
	}
	if dup == nil {
		t.Fatalf("expected a duplicate_prefix finding, got %+v", findings)
	}
	if !strings.Contains(dup.IndexAdvice.CandidateDDL, "idx_region") || !strings.Contains(dup.IndexAdvice.CandidateDDL, "DROP INDEX CONCURRENTLY") {
		t.Errorf("expected drop candidate for redundant prefix index, got %q", dup.IndexAdvice.CandidateDDL)
	}
}

// TestDetectIndexIssues_DuplicatePrefix_PreservesUnique ensures a unique index
// is never proposed for removal in favor of a non-unique superset index.
func TestDetectIndexIssues_DuplicatePrefix_PreservesUnique(t *testing.T) {
	indexes := []IndexDefinition{
		{Name: "uq_email", KeyColumns: []string{"email"}, IsUnique: true, IsValid: true},
		{Name: "idx_email_created", KeyColumns: []string{"email", "created_at"}, IsValid: true},
	}
	findings := detectIndexIssues("demo", "customers", indexes)
	for _, f := range findings {
		if f.IndexAdvice != nil {
			for _, issue := range f.IndexAdvice.Issues {
				if issue == "duplicate_prefix" {
					t.Fatalf("must not propose dropping a unique index for a non-unique superset: %+v", f)
				}
			}
		}
	}
}

// TestDetectIndexIssues_Overlapping covers same-column-set-different-order and
// shared-leading-column-diverging-tail overlap detection.
func TestDetectIndexIssues_Overlapping(t *testing.T) {
	indexes := []IndexDefinition{
		{Name: "idx_a_b", KeyColumns: []string{"a", "b"}, IsValid: true, SizeBytes: 1 << 20},
		{Name: "idx_b_a", KeyColumns: []string{"b", "a"}, IsValid: true, SizeBytes: 1 << 20},
		{Name: "idx_c_d", KeyColumns: []string{"c", "d"}, IsValid: true},
		{Name: "idx_c_e", KeyColumns: []string{"c", "e"}, IsValid: true},
	}
	findings := detectIndexIssues("demo", "widgets", indexes)

	var sawOverlap, sawLeadingCol bool
	for _, f := range findings {
		if f.IndexAdvice == nil {
			continue
		}
		for _, issue := range f.IndexAdvice.Issues {
			switch issue {
			case "overlapping":
				sawOverlap = true
			case "overlapping_leading_column":
				sawLeadingCol = true
			}
		}
	}
	if !sawOverlap {
		t.Error("expected an 'overlapping' finding for idx_a_b/idx_b_a")
	}
	if !sawLeadingCol {
		t.Error("expected an 'overlapping_leading_column' finding for idx_c_d/idx_c_e")
	}
}

func TestDetectIndexIssues_NoIssues(t *testing.T) {
	indexes := []IndexDefinition{
		{Name: "idx_healthy", KeyColumns: []string{"sold_at"}, IsValid: true, IndexScans: 1000},
	}
	if got := detectIndexIssues("demo", "sales", indexes); len(got) != 0 {
		t.Fatalf("expected no issues for a single healthy index, got %+v", got)
	}
}

// TestWalkPlanNode_AttachesRelatedColumns checks the full parseExplainJSON
// pipeline attaches RelatedColumns to a seq-scan finding from a real fixture.
func TestWalkPlanNode_AttachesRelatedColumns(t *testing.T) {
	_, findings, _, err := parseExplainJSON(loadExplainFixture(t, "seq_scan.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range findings {
		if f.IsSeqScan {
			if len(f.RelatedColumns) != 1 || f.RelatedColumns[0] != "region" {
				t.Fatalf("expected RelatedColumns=[region], got %v", f.RelatedColumns)
			}
			return
		}
	}
	t.Fatal("no seq scan finding produced")
}

// TestDetectPlanSignals_InfersRelationForSort checks that a Sort node with no
// "Relation Name" of its own is attributed to the single underlying table so
// sort-spill findings can be related to that table's indexes.
func TestDetectPlanSignals_InfersRelationForSort(t *testing.T) {
	_, findings, _, err := parseExplainJSON(loadExplainFixture(t, "sort_spill.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, f := range findings {
		if f.Category == CategorySortSpill {
			if f.Schema != "demo" || f.Relation != "sales" {
				t.Fatalf("expected inferred schema/relation demo.sales, got %s.%s", f.Schema, f.Relation)
			}
			if len(f.RelatedColumns) != 1 || f.RelatedColumns[0] != "sold_at" {
				t.Fatalf("expected RelatedColumns=[sold_at], got %v", f.RelatedColumns)
			}
			return
		}
	}
	t.Fatal("no sort spill finding produced")
}

func TestInferSingleTableRelation(t *testing.T) {
	single := loadPlanRoot(t, "sort_spill.json")
	schema, relation := inferSingleTableRelation(single)
	if schema != "demo" || relation != "sales" {
		t.Fatalf("expected demo.sales, got %s.%s", schema, relation)
	}

	multiTable := loadPlanRoot(t, "hash_join_cond.json")
	schema, relation = inferSingleTableRelation(multiTable)
	if schema != "" || relation != "" {
		t.Fatalf("expected no single-table attribution for a multi-table join, got %s.%s", schema, relation)
	}
}
