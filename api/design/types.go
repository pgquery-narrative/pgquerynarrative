package design

import (
	. "goa.design/goa/v3/dsl"
)

var RunQueryPayload = Type("RunQueryPayload", func() {
	Attribute("sql", String, "SQL query to execute", func() {
		MinLength(1)
		MaxLength(10000)
		Pattern("^[^;]+$")
	})
	Attribute("limit", Int32, "Maximum number of rows to return", func() {
		Default(100)
		Minimum(1)
		Maximum(1000)
	})
	Attribute("connection_id", String, "Optional connection ID; defaults to server default connection")
	Required("sql")
})

var RunQueryResult = Type("RunQueryResult", func() {
	Attribute("columns", ArrayOf(ColumnInfo))
	Attribute("rows", ArrayOf(ArrayOf(Any)))
	Attribute("row_count", Int32)
	Attribute("execution_time_ms", Int64)
	Attribute("limit", Int32)
	Attribute("chart_suggestions", ArrayOf(ChartSuggestion), "Suggested chart types based on result shape")
	Attribute("period_comparison", ArrayOf(PeriodComparisonItem), "Period-over-period comparison when result has time + measure columns")
	Attribute("period_current_label", String, "Label for current period when period_comparison is present (e.g. date or month)")
	Attribute("period_previous_label", String, "Label for previous period")
	Required("columns", "rows", "row_count", "execution_time_ms", "limit")
})

// PeriodComparisonItem is one measure's current vs previous period (e.g. this month vs last month).
var PeriodComparisonItem = Type("PeriodComparisonItem", func() {
	Attribute("measure", String, "Measure column name")
	Attribute("current", Float64, "Current period value")
	Attribute("previous", Float64, "Previous period value")
	Attribute("change", Float64, "Absolute change (current - previous)")
	Attribute("change_percentage", Float64, "Percent change vs previous")
	Attribute("trend", String, "up, down, or flat")
	Required("measure", "current", "trend")
})

// ChartSuggestion describes a chart type suggested from the query result shape.
var ChartSuggestion = Type("ChartSuggestion", func() {
	Attribute("chart_type", String, "Chart type identifier: bar, line, pie, area, table")
	Attribute("label", String, "Human-readable label")
	Attribute("reason", String, "Why this chart fits the data")
	Required("chart_type", "label", "reason")
})

var ColumnInfo = Type("ColumnInfo", func() {
	Attribute("name", String)
	Attribute("type", String)
	Required("name", "type")
})

var ValidationError = Type("ValidationError", func() {
	Attribute("name", String)
	Attribute("message", String)
	Attribute("code", String)
	Required("name", "message")
})

var NotFoundError = Type("NotFoundError", func() {
	Attribute("name", String)
	Attribute("message", String)
	Attribute("code", String)
	Required("name", "message")
})

var SaveQueryPayload = Type("SaveQueryPayload", func() {
	Attribute("name", String, func() {
		MinLength(1)
		MaxLength(200)
	})
	Attribute("sql", String, func() {
		MinLength(1)
		MaxLength(10000)
	})
	Attribute("description", String, func() {
		MaxLength(500)
	})
	Attribute("tags", ArrayOf(String))
	Attribute("connection_id", String, "Optional connection ID; defaults to server default connection")
	Required("name", "sql")
})

var SavedQuery = Type("SavedQuery", func() {
	Attribute("id", String, func() {
		Format(FormatUUID)
	})
	Attribute("name", String)
	Attribute("sql", String)
	Attribute("description", String)
	Attribute("tags", ArrayOf(String))
	Attribute("connection_id", String)
	Attribute("created_at", String, func() {
		Format(FormatDateTime)
	})
	Attribute("updated_at", String, func() {
		Format(FormatDateTime)
	})
	Required("id", "name", "sql", "connection_id", "created_at", "updated_at")
})

var SavedQueryList = Type("SavedQueryList", func() {
	Attribute("items", ArrayOf(SavedQuery))
	Attribute("limit", Int32)
	Attribute("offset", Int32)
	Required("items", "limit", "offset")
})

var ExplainQueryPayload = Type("ExplainQueryPayload", func() {
	Attribute("sql", String, "Read-only SQL to explain (SELECT or WITH)", func() {
		MinLength(1)
		MaxLength(10000)
		Pattern("^[^;]+$")
	})
	Attribute("analyze", Boolean, "When true, run EXPLAIN (ANALYZE, FORMAT JSON) instead of estimate-only", func() {
		Default(false)
	})
	Attribute("connection_id", String, "Optional connection ID; defaults to server default connection")
	Required("sql")
})

var ExplainQueryResult = Type("ExplainQueryResult", func() {
	Attribute("sql", String, "The inner read-only SQL that was explained")
	Attribute("total_cost", Float64, "Estimated total cost from the root plan node")
	Attribute("plan", Any, "Raw EXPLAIN (FORMAT JSON) output")
	Attribute("findings", ArrayOf(PlanFinding), "Notable plan nodes (seq scans, high-cost operators)")
	Attribute("diagnosis", PlanDiagnosis, "Verdict-first rollup of findings into ranked causes")
	Attribute("generic_plan", Boolean, "True when the plan came from EXPLAIN (GENERIC_PLAN) because the query is parameterized ($1, $2, ...)")
	Attribute("execution_time_ms", Int64, "Time to run EXPLAIN and parse the plan")
	Required("sql", "total_cost", "plan", "findings", "execution_time_ms")
})

// PlanDiagnosisCause is one distinct, deduplicated reason a query is slow.
var PlanDiagnosisCause = Type("PlanDiagnosisCause", func() {
	Attribute("category", String, "Finding category, e.g. partition_pruning, sort_spill")
	Attribute("title", String, "Deduplicated, human headline for this cause")
	Attribute("detail", String, "One-line explanation")
	Attribute("fix", String, "Short imperative fix; empty when there is no generic fix")
	Attribute("severity", String, "blocker | contributing")
	Attribute("cost_share", Float64, "0..1 share of plan cost attributable to this cause")
	Attribute("occurrences", Int, "Raw findings that rolled into this cause")
	Attribute("node_types", ArrayOf(String), "Distinct plan node types involved")
	Attribute("evidence", ArrayOf(String), "Up to five sample raw finding messages")
	Required("category", "title", "severity")
})

// PlanDiagnosisIncidental is the collapsed line for schema-hygiene noise.
var PlanDiagnosisIncidental = Type("PlanDiagnosisIncidental", func() {
	Attribute("count", Int, "Raw findings rolled up")
	Attribute("summary", String, "One-line explanation of why these are set aside")
	Attribute("categories", ArrayOf(String), "Categories represented")
	Required("count", "summary")
})

// PlanDiagnosis collapses hundreds of repetitive findings into the small set of
// distinct, ranked causes a human would name.
var PlanDiagnosis = Type("PlanDiagnosis", func() {
	Attribute("headline", String, "Short root-cause label")
	Attribute("summary", String, "Two-to-three sentence verdict with concrete numbers")
	Attribute("root_cause", PlanDiagnosisCause, "The single highest-leverage cause")
	Attribute("causes", ArrayOf(PlanDiagnosisCause), "Ranked causes, root first (excludes incidental)")
	Attribute("incidental", PlanDiagnosisIncidental, "Rolled-up schema-hygiene noise")
	Attribute("raw_count", Int, "Number of findings before rollup")
	Required("raw_count")
})

// StatStatementRow is one entry from pg_stat_statements.
var StatStatementRow = Type("StatStatementRow", func() {
	Attribute("queryid", String, "Normalized query identifier when available")
	Attribute("query", String, "Query text (truncated)")
	Attribute("calls", Int64, "Number of times executed")
	Attribute("total_time_ms", Float64, "Total execution time in milliseconds")
	Attribute("mean_time_ms", Float64, "Mean execution time per call in milliseconds")
	Attribute("rows", Int64, "Total rows retrieved or affected")
	Required("query", "calls", "total_time_ms", "mean_time_ms", "rows")
})

// StatStatementsResult is a ranked list from pg_stat_statements.
var StatStatementsResult = Type("StatStatementsResult", func() {
	Attribute("items", ArrayOf(StatStatementRow))
	Attribute("order_by", String, "Sort key used: total_time, mean_time, or calls")
	Attribute("limit", Int32)
	Required("items", "order_by", "limit")
})

// IndexDefinition describes an existing index considered by the advisor.
var IndexDefinition = Type("IndexDefinition", func() {
	Attribute("name", String, "Index name")
	Attribute("definition", String, "pg_get_indexdef text")
	Attribute("key_columns", ArrayOf(String), "Key columns in position order")
	Attribute("include_columns", ArrayOf(String), "INCLUDE columns")
	Attribute("predicate", String, "Partial-index predicate when present")
	Attribute("is_unique", Boolean)
	Attribute("is_primary", Boolean)
	Attribute("is_valid", Boolean)
	Attribute("size_bytes", Int64)
	Attribute("index_scans", Int64)
	Attribute("tuples_read", Int64)
	Attribute("tuples_fetched", Int64)
	Required("name", "definition", "is_unique", "is_primary", "is_valid")
})

// IndexAdvice is structured index-recommendation evidence for a plan finding.
// CandidateDDL is for expert review only and is never auto-applied.
var IndexAdvice = Type("IndexAdvice", func() {
	Attribute("related_columns", ArrayOf(String), "Columns implicated by the finding")
	Attribute("related_indexes", ArrayOf(IndexDefinition), "Existing indexes evaluated")
	Attribute("issues", ArrayOf(String), "Issue codes (e.g. no_covering_index, already_covered)")
	Attribute("potential_benefit", String, "Plain-language benefit if advice is followed")
	Attribute("write_cost", String, "Write-amplification cost of the recommended change")
	Attribute("storage_cost", String, "On-disk storage cost of the recommended change")
	Attribute("candidate_ddl", String, "Draft DDL for expert review only; never auto-applied")
})

// PlanFinding highlights a notable node in an EXPLAIN plan.
var PlanFinding = Type("PlanFinding", func() {
	Attribute("node_type", String, "PostgreSQL plan node type (e.g. Seq Scan)")
	Attribute("schema", String, "Schema name when the node scans a relation")
	Attribute("relation", String, "Relation name when applicable")
	Attribute("estimated_cost", Float64, "Planner cost for this node")
	Attribute("is_seq_scan", Boolean, "True when the node is a sequential scan")
	Attribute("category", String, "Finding category (e.g. seq_scan, cardinality_misestimate, sort_spill)")
	Attribute("confidence", String, "Triage confidence: low, medium, or high")
	Attribute("message", String, "Human-readable summary and optional index hint")
	Attribute("evidence", ArrayOf(String), "Raw plan metrics backing this finding (e.g. Plan Rows=8000)")
	Attribute("related_columns", ArrayOf(String), "Filter/join/sort columns implicated by this finding")
	Attribute("index_advice", IndexAdvice, "Structured index advice when catalog enrichment produced it")
	Required("node_type", "is_seq_scan", "message")
})

// RewriteCandidate is a system-generated SQL rewrite suggestion.
var RewriteCandidate = Type("RewriteCandidate", func() {
	Attribute("sql", String, "Candidate rewrite SQL")
	Attribute("rationale", String, "Short explanation of the rewrite")
	Attribute("category", String, "Rewrite category (e.g. function_wrap)")
	Attribute("confidence", String, "Suggestion confidence: low, medium, or high")
	Required("sql", "rationale")
})

// RewriteSuggestionList is the result of suggest_rewrite.
var RewriteSuggestionList = Type("RewriteSuggestionList", func() {
	Attribute("candidates", ArrayOf(RewriteCandidate))
	Required("candidates")
})

// RankedCandidateBaseline is the original investigation plan metrics.
var RankedCandidateBaseline = Type("RankedCandidateBaseline", func() {
	Attribute("total_cost", Float64, "Baseline total cost")
	Attribute("partitions_scanned", Float64, "Baseline partitions scanned when known")
	Attribute("execution_time_ms", Float64, "Baseline execution time when ANALYZE was used")
})

// RankedCandidate is one scored rewrite or index-DDL suggestion (DDL may carry
// hypopg/heuristic projected cost when available).
var RankedCandidate = Type("RankedCandidate", func() {
	Attribute("kind", String, "sql_rewrite or index_ddl")
	Attribute("rank", Int32, "1-based rank among rankable candidates; 0 when not rankable")
	Attribute("rankable", Boolean, "True when dry-EXPLAIN or projected index metrics are available for ranking")
	Attribute("sql", String, "Candidate rewrite SQL (sql_rewrite)")
	Attribute("ddl", String, "Candidate DDL for expert review (index_ddl); never auto-applied")
	Attribute("rationale", String, "Short explanation")
	Attribute("category", String)
	Attribute("confidence", String)
	Attribute("total_cost", Float64, "After-plan or projected total cost")
	Attribute("cost_delta", Float64, "after/projected - baseline total cost (negative is better)")
	Attribute("partitions_scanned", Float64)
	Attribute("partitions_delta", Float64, "after - baseline partitions (negative is better)")
	Attribute("execution_time_ms", Float64, "After-plan timing when ANALYZE was used")
	Attribute("improved", ArrayOf(String), "Structural improvements vs baseline")
	Attribute("projection_method", String, "For index_ddl: hypopg, heuristic, or unavailable")
	Required("kind", "rankable", "rationale")
})

// RankedCandidateList is the result of rank_candidates.
var RankedCandidateList = Type("RankedCandidateList", func() {
	Attribute("baseline", RankedCandidateBaseline)
	Attribute("candidates", ArrayOf(RankedCandidate))
	Required("candidates")
})

// ComparePlansPayload compares before and after EXPLAIN plans.
var ComparePlansPayload = Type("ComparePlansPayload", func() {
	Attribute("before_sql", String, "Original SQL to explain", func() {
		MinLength(1)
		MaxLength(10000)
		Pattern("^[^;]+$")
	})
	Attribute("after_sql", String, "Candidate SQL to explain", func() {
		MinLength(1)
		MaxLength(10000)
		Pattern("^[^;]+$")
	})
	Attribute("analyze", Boolean, "Run EXPLAIN ANALYZE when enabled server-side", func() {
		Default(false)
	})
	Attribute("connection_id", String, "Optional connection ID")
	Attribute("binds", ArrayOf(String), "Sample bind values for parameterized before/after SQL ($1, $2, ...); substituted for the compare/equivalence run")
	Required("before_sql", "after_sql")
})

// PlanComparisonMetric is one row in a before/after comparison table.
var PlanComparisonMetric = Type("PlanComparisonMetric", func() {
	Attribute("evidence", String, "Metric name (e.g. Execution time)")
	Attribute("before", String, "Before value")
	Attribute("after", String, "After value")
	Attribute("change", String, "Change summary (e.g. −96.3%)")
	Required("evidence", "before", "after", "change")
})

// PlanComparisonDiff summarizes structural plan changes.
var PlanComparisonDiff = Type("PlanComparisonDiff", func() {
	Attribute("removed", ArrayOf(String), "Plan nodes removed")
	Attribute("added", ArrayOf(String), "Plan nodes added")
	Attribute("improved", ArrayOf(String), "Improvements detected")
})

// ComparePlansResult is the outcome of comparing two execution plans.
var ComparePlansResult = Type("ComparePlansResult", func() {
	Attribute("before", ExplainQueryResult)
	Attribute("after", ExplainQueryResult)
	Attribute("metrics", ArrayOf(PlanComparisonMetric))
	Attribute("diff", PlanComparisonDiff)
	Attribute("result_checksum_equal", Boolean, "True when results match; false when they differ; omitted/null when unverified")
	Attribute("result_equivalence_status", String, "Equal | Different | Unverified")
	Attribute("result_equivalence_notes", String, "Human-readable equivalence caveats (COUNT(*), sample size, failures)")
	Attribute("result_before_row_count", Int64, "COUNT(*) of before SQL when computable")
	Attribute("result_after_row_count", Int64, "COUNT(*) of after SQL when computable")
	Attribute("result_sample_rows", Int32, "Rows compared in the multiset sample")
	Required("before", "after", "metrics", "diff")
})

// StatSnapshot captures pg_stat_statements context for an investigation.
var StatSnapshot = Type("StatSnapshot", func() {
	Attribute("queryid", String)
	Attribute("calls", Int64)
	Attribute("mean_time_ms", Float64)
	Attribute("total_time_ms", Float64)
	Attribute("rows", Int64)
})

// Investigation is a Query Investigation workflow artifact.
var Investigation = Type("Investigation", func() {
	Attribute("id", String, func() {
		Format(FormatUUID)
	})
	Attribute("title", String)
	Attribute("status", String, "open, analyzing, comparing, or complete")
	Attribute("sql", String)
	Attribute("connection_id", String)
	Attribute("query_fingerprint", String)
	Attribute("stat_snapshot", StatSnapshot)
	Attribute("explain", ExplainQueryResult)
	Attribute("candidate_sql", String)
	Attribute("candidate_explain", ExplainQueryResult)
	Attribute("comparison", ComparePlansResult)
	Attribute("report_id", String)
	Attribute("created_at", String, func() {
		Format(FormatDateTime)
	})
	Attribute("updated_at", String, func() {
		Format(FormatDateTime)
	})
	Required("id", "title", "status", "sql", "connection_id", "created_at", "updated_at")
})

var InvestigationList = Type("InvestigationList", func() {
	Attribute("items", ArrayOf(Investigation))
	Attribute("limit", Int32)
	Attribute("offset", Int32)
	Required("items", "limit", "offset")
})

var CreateInvestigationPayload = Type("CreateInvestigationPayload", func() {
	Attribute("title", String, func() {
		MinLength(1)
		MaxLength(200)
	})
	Attribute("sql", String, func() {
		MinLength(1)
		MaxLength(10000)
		Pattern("^[^;]+$")
	})
	Attribute("connection_id", String)
	Attribute("queryid", String, "Optional pg_stat_statements queryid for context")
	Attribute("calls", Int64)
	Attribute("mean_time_ms", Float64)
	Attribute("total_time_ms", Float64)
	Attribute("rows", Int64)
	Required("title", "sql")
})

var AddCandidatePayload = Type("AddCandidatePayload", func() {
	Attribute("id", String, func() {
		Format(FormatUUID)
	})
	Attribute("candidate_sql", String, func() {
		MinLength(1)
		MaxLength(10000)
		Pattern("^[^;]+$")
	})
	Attribute("analyze", Boolean, func() {
		Default(false)
	})
	Attribute("binds", ArrayOf(String), "Sample bind values for a parameterized candidate ($1, $2, ...); used only for the compare/equivalence run, not stored")
	Required("id", "candidate_sql")
})

// WorkspaceOverview is PostgreSQL evidence for the landing dashboard.
var WorkspaceOverview = Type("WorkspaceOverview", func() {
	Attribute("queries_observed", Int64)
	Attribute("database_time_hours", Float64)
	Attribute("queries_attention", Int32)
	Attribute("largest_regression_pct", Float64)
	Attribute("temp_data_written_gb", Float64)
	Attribute("sequential_scans_detected", Int32)
	Attribute("investigations_open", Int32)
	Attribute("reports_generated", Int32)
	Required("queries_observed", "database_time_hours", "queries_attention",
		"largest_regression_pct", "temp_data_written_gb", "sequential_scans_detected",
		"investigations_open", "reports_generated")
})

// RegressionAlert is one entry in the regression inbox.
var RegressionAlert = Type("RegressionAlert", func() {
	Attribute("id", String, func() {
		Format(FormatUUID)
	})
	Attribute("title", String)
	Attribute("query", String)
	Attribute("change_type", String)
	Attribute("change_summary", String)
	Attribute("impact", String, "critical, high, medium, or low")
	Attribute("first_detected_at", String, func() {
		Format(FormatDateTime)
	})
	Attribute("acknowledged", Boolean)
	Attribute("connection_id", String)
	Attribute("queryid", String, "pg_stat_statements queryid when known")
	Attribute("source", String, "poller or demo")
	Attribute("investigation_id", String, "Linked investigation when opened from inbox", func() {
		Format(FormatUUID)
	})
	Attribute("calls", Int64)
	Attribute("mean_time_ms", Float64)
	Attribute("total_time_ms", Float64)
	Attribute("rows", Int64)
	Required("id", "title", "query", "change_type", "change_summary", "impact", "first_detected_at", "acknowledged", "connection_id")
})

var RegressionInbox = Type("RegressionInbox", func() {
	Attribute("items", ArrayOf(RegressionAlert))
	Required("items")
})

// DemoScenario is a guided investigation scenario.
var DemoScenario = Type("DemoScenario", func() {
	Attribute("id", String)
	Attribute("title", String)
	Attribute("problem", String, "Short business problem")
	Attribute("sql", String, "Reproducible query")
	Attribute("candidate_sql", String, "Verified improvement query")
	Attribute("expected_improvement", String, "e.g. 8.4s → 310ms")
	Attribute("category", String, "e.g. function_wrap, partition_pruning, cardinality")
	Required("id", "title", "problem", "sql", "expected_improvement", "category")
})

var DemoScenarioList = Type("DemoScenarioList", func() {
	Attribute("items", ArrayOf(DemoScenario))
	Required("items")
})

// SecurityTrust documents the security posture.
var SecurityTrust = Type("SecurityTrust", func() {
	Attribute("authentication", String)
	Attribute("connection_mode", String)
	Attribute("allowed_schemas", ArrayOf(String))
	Attribute("tenant_isolation", String)
	Attribute("tls", String)
	Attribute("audit_mode", String)
	Attribute("query_timeout_seconds", Int32)
	Attribute("result_limit", Int32)
	Attribute("explain_analyze", String)
	Attribute("external_llm_data", String)
	Required("authentication", "connection_mode", "allowed_schemas", "tenant_isolation",
		"tls", "audit_mode", "query_timeout_seconds", "result_limit",
		"explain_analyze", "external_llm_data")
})
