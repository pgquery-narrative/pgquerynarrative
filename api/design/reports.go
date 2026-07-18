package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = Service("reports", func() {
	Description("Report generation and management")

	Method("generate", func() {
		Description("Generate a narrative report from a SQL query")
		Payload(GenerateReportPayload)
		Result(Report)
		Error("validation_error", ValidationError)
		Error("llm_error", LLMError)
		HTTP(func() {
			POST("/api/v1/reports/generate")
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
			Response(StatusInternalServerError, "llm_error")
		})
	})

	Method("get", func() {
		Description("Get a report by ID")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Result(Report)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/reports/{id}")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("list", func() {
		Description("List generated reports")
		Payload(func() {
			Attribute("saved_query_id", String, func() {
				Format(FormatUUID)
			})
			Attribute("connection_id", String)
			Attribute("limit", Int32, func() {
				Default(50)
				Minimum(1)
				Maximum(100)
			})
			Attribute("offset", Int32, func() {
				Default(0)
				Minimum(0)
			})
		})
		Result(ReportList)
		HTTP(func() {
			GET("/api/v1/reports")
			Params(func() {
				Param("saved_query_id")
				Param("connection_id")
				Param("limit")
				Param("offset")
			})
		})
	})

	Method("similar", func() {
		Description("Find reports semantically similar to input text")
		Payload(func() {
			Attribute("text", String, func() {
				MinLength(1)
				MaxLength(2000)
			})
			Attribute("connection_id", String)
			Attribute("limit", Int32, func() {
				Default(5)
				Minimum(1)
				Maximum(20)
			})
			Required("text")
		})
		Result(ReportSimilarResult)
		HTTP(func() {
			GET("/api/v1/reports/similar")
			Params(func() {
				Param("text")
				Param("connection_id")
				Param("limit")
			})
			Response(StatusOK)
		})
	})

	Method("rewrite", func() {
		Description("Rewrite an existing report narrative using a user instruction.")
		Payload(func() {
			Attribute("report_id", String, "Report ID to rewrite", func() {
				Format(FormatUUID)
			})
			Attribute("instruction", String, "How to rewrite the narrative (e.g. concise, executive summary, Spanish)", func() {
				MinLength(1)
				MaxLength(1000)
			})
			Required("report_id", "instruction")
		})
		Result(NarrativeContent)
		Error("validation_error", ValidationError)
		Error("llm_error", LLMError)
		Error("not_found", NotFoundError)
		HTTP(func() {
			POST("/api/v1/reports/rewrite")
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
			Response(StatusInternalServerError, "llm_error")
			Response(StatusNotFound, "not_found")
		})
	})

	Method("create_share", func() {
		Description("Create or refresh a shareable read-only token link for a report.")
		Payload(func() {
			Attribute("report_id", String, func() {
				Format(FormatUUID)
			})
			Attribute("expires_in_hours", Int32, "Optional expiry in hours (1-720). Omit for no expiry.", func() {
				Minimum(1)
				Maximum(720)
			})
			Required("report_id")
		})
		Result(ReportShareLink)
		Error("not_found", NotFoundError)
		HTTP(func() {
			POST("/api/v1/reports/share")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("get_shared", func() {
		Description("Fetch a report by valid share token.")
		Payload(func() {
			Attribute("token", String, func() {
				MinLength(12)
			})
			Required("token")
		})
		Result(Report)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/reports/shared/{token}")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("list_shares", func() {
		Description("List active and revoked share links for a report. Never returns the raw token, only non-secret metadata.")
		Payload(func() {
			Attribute("report_id", String, func() {
				Format(FormatUUID)
			})
			Required("report_id")
		})
		Result(ReportShareList)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/reports/{report_id}/shares")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("revoke_share", func() {
		Description("Revoke a share link so it can no longer be used to fetch its report.")
		Payload(func() {
			Attribute("id", String, "Share link ID (not the token)", func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Error("not_found", NotFoundError)
		HTTP(func() {
			POST("/api/v1/reports/shares/{id}/revoke")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})
})

var GenerateReportPayload = Type("GenerateReportPayload", func() {
	Attribute("sql", String, func() {
		MinLength(1)
		MaxLength(10000)
		Pattern("^[^;]+$")
	})
	Attribute("saved_query_id", String, func() {
		Format(FormatUUID)
	})
	Attribute("connection_id", String)
	Required("sql")
})

var Report = Type("Report", func() {
	Attribute("id", String, func() {
		Format(FormatUUID)
	})
	Attribute("saved_query_id", String, func() {
		Format(FormatUUID)
	})
	Attribute("sql", String)
	Attribute("connection_id", String)
	Attribute("narrative", NarrativeContent)
	Attribute("metrics", MetricsData)
	Attribute("chart_suggestions", ArrayOf(ChartSuggestion), "Suggested chart types based on result shape")
	Attribute("created_at", String, func() {
		Format(FormatDateTime)
	})
	Attribute("llm_model", String)
	Attribute("llm_provider", String)
	Required("id", "sql", "connection_id", "narrative", "metrics", "created_at", "llm_model", "llm_provider")
})

var NarrativeContent = Type("NarrativeContent", func() {
	Attribute("headline", String)
	Attribute("takeaways", ArrayOf(String))
	Attribute("drivers", ArrayOf(String))
	Attribute("limitations", ArrayOf(String))
	Attribute("recommendations", ArrayOf(String))
	Required("headline", "takeaways")
})

var CorrelationPairData = Type("CorrelationPairData", func() {
	Attribute("column_a", String)
	Attribute("column_b", String)
	Attribute("pearson", Float64, "Pearson correlation -1 to 1")
	Attribute("spearman", Float64, "Spearman rank correlation -1 to 1")
	Required("column_a", "column_b", "pearson", "spearman")
})

var CohortPeriodPointData = Type("CohortPeriodPointData", func() {
	Attribute("period_label", String)
	Attribute("value", Float64)
	Required("period_label", "value")
})

var CohortMetricData = Type("CohortMetricData", func() {
	Attribute("cohort_label", String)
	Attribute("periods", ArrayOf(CohortPeriodPointData))
	Attribute("retention_pct", Float64)
	Required("cohort_label")
})

var MetricsData = Type("MetricsData", func() {
	Attribute("aggregates", MapOf(String, AggregateData))
	Attribute("top_categories", MapOf(String, ArrayOf(TopCategoryData)))
	Attribute("time_series", MapOf(String, TimeSeriesData))
	Attribute("correlations", ArrayOf(CorrelationPairData), "Pairwise Pearson and Spearman (when ≥2 numeric measures, enough rows)")
	Attribute("cohorts", ArrayOf(CohortMetricData), "Cohort analysis when cohort dimension present (inputs: cohort key, time grain)")
	Attribute("period_current_label", String, "Label for current period when time_series is present")
	Attribute("period_previous_label", String, "Label for previous period")
	Attribute("data_quality", MapOf(String, ColumnQualityData), "Per-column data quality (nulls, distinct)")
	Attribute("perf_suggestions", ArrayOf(String), "Performance suggestions from execution time/row count")
})

var AggregateData = Type("AggregateData", func() {
	Attribute("sum", Float64)
	Attribute("avg", Float64)
	Attribute("min", Float64)
	Attribute("max", Float64)
	Attribute("count", Int32)
	Attribute("std_dev", Float64, "Standard deviation (stats)")
})

var ColumnQualityData = Type("ColumnQualityData", func() {
	Attribute("null_count", Int32)
	Attribute("distinct_count", Int32)
	Attribute("total_rows", Int32)
	Attribute("null_pct", Float64, "Null percentage 0–100")
	Required("null_count", "distinct_count", "total_rows", "null_pct")
})

var TopCategoryData = Type("TopCategoryData", func() {
	Attribute("category", String)
	Attribute("value", Float64)
	Attribute("percentage", Float64)
	Required("category", "value", "percentage")
})

var TimeSeriesData = Type("TimeSeriesData", func() {
	Attribute("current_period", Float64)
	Attribute("previous_period", Float64)
	Attribute("change", Float64)
	Attribute("change_percentage", Float64)
	Attribute("trend", String)
	Attribute("periods", ArrayOf(PeriodPointData), "Last N period labels and values (newest last)")
	Attribute("moving_average", Float64, "Simple moving average for latest period (e.g. 3-period SMA)")
	Attribute("anomalies", ArrayOf(AnomalyPointData), "Periods flagged as statistical anomalies (e.g. z-score)")
	Attribute("trend_summary", TrendSummaryData, "Trend over multiple periods (direction, slope, summary)")
	Attribute("next_period_forecast", Float64, "Simple predictive: last value + trend slope")
	Attribute("forecast_ci_lower", Float64, "Lower bound of confidence interval for next-period forecast")
	Attribute("forecast_ci_upper", Float64, "Upper bound of confidence interval for next-period forecast")
	Attribute("predictive_summary", String, "Human-readable predictive sentence for the narrative")
	Attribute("exponential_smooth_forecast", Float64, "One-step-ahead forecast from simple exponential smoothing")
	Attribute("holt_forecast", Float64, "One-step-ahead forecast from Holt linear trend")
	Attribute("seasonal_period", Int32, "Detected seasonal period (0=none, e.g. 4=quarterly, 12=monthly)")
	Attribute("seasonally_adjusted_forecast", Float64, "Next-period forecast with seasonal component")
	Required("current_period", "trend")
})

var PeriodPointData = Type("PeriodPointData", func() {
	Attribute("label", String)
	Attribute("value", Float64)
	Required("label", "value")
})

var AnomalyPointData = Type("AnomalyPointData", func() {
	Attribute("period_label", String)
	Attribute("value", Float64)
	Attribute("reason", String)
	Attribute("explanation", String, "One-sentence explanation for why this period may be anomalous")
	Required("period_label", "value", "reason")
})

var TrendSummaryData = Type("TrendSummaryData", func() {
	Attribute("direction", String, "increasing, decreasing, or stable")
	Attribute("slope", Float64, "Change per period from linear regression")
	Attribute("periods_used", Int32)
	Attribute("summary", String, "Human-readable trend description")
	Attribute("explanation", String, "One-sentence explanation of what the trend suggests")
	Required("direction", "summary")
})

var ReportList = Type("ReportList", func() {
	Attribute("items", ArrayOf(Report))
	Attribute("limit", Int32)
	Attribute("offset", Int32)
	Required("items", "limit", "offset")
})

var SimilarReportItem = Type("SimilarReportItem", func() {
	Attribute("id", String, func() {
		Format(FormatUUID)
	})
	Attribute("headline", String)
	Attribute("sql", String)
	Attribute("connection_id", String)
	Attribute("created_at", String, func() {
		Format(FormatDateTime)
	})
	Attribute("similarity", Float64)
	Required("id", "headline", "sql", "connection_id", "created_at", "similarity")
})

var ReportSimilarResult = Type("ReportSimilarResult", func() {
	Attribute("items", ArrayOf(SimilarReportItem))
	Required("items")
})

var ReportShareLink = Type("ReportShareLink", func() {
	Attribute("token", String)
	Attribute("url", String)
	Attribute("expires_at", String, func() { Format(FormatDateTime) })
	Required("token", "url")
})

// ReportShareInfo is non-secret metadata about a share link: it never carries the raw
// token or its hash, only enough information to let an owner audit and revoke links.
var ReportShareInfo = Type("ReportShareInfo", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("created_at", String, func() { Format(FormatDateTime) })
	Attribute("expires_at", String, func() { Format(FormatDateTime) })
	Attribute("revoked_at", String, func() { Format(FormatDateTime) })
	Attribute("access_count", Int32)
	Attribute("last_accessed_at", String, func() { Format(FormatDateTime) })
	Required("id", "created_at", "access_count")
})

var ReportShareList = Type("ReportShareList", func() {
	Attribute("items", ArrayOf(ReportShareInfo))
	Required("items")
})

var LLMError = Type("LLMError", func() {
	Attribute("name", String)
	Attribute("message", String)
	Attribute("code", String)
	Required("name", "message")
})
