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
	Attribute("execution_time_ms", Int64, "Time to run EXPLAIN and parse the plan")
	Required("sql", "total_cost", "plan", "findings", "execution_time_ms")
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
	Required("node_type", "is_seq_scan", "message")
})
