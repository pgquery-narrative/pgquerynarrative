package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = Service("investigations", func() {
	Description("Query Investigation workflow: analyze expensive queries with evidence-backed reports")

	Method("create", func() {
		Description("Start a new query investigation from SQL or pg_stat_statements context")
		Payload(CreateInvestigationPayload)
		Result(Investigation)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/investigations")
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("create_from_regression", func() {
		Description("Open an investigation from a regression alert (EXPLAIN + link alert to investigation)")
		Payload(func() {
			Attribute("regression_alert_id", String, func() {
				Format(FormatUUID)
			})
			Required("regression_alert_id")
		})
		Result(Investigation)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/investigations/from-regression")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("list", func() {
		Description("List query investigations for the current organization")
		Payload(func() {
			Attribute("limit", Int32, func() {
				Default(20)
				Minimum(1)
				Maximum(100)
			})
			Attribute("offset", Int32, func() {
				Default(0)
				Minimum(0)
			})
		})
		Result(InvestigationList)
		HTTP(func() {
			GET("/api/v1/investigations")
			Params(func() {
				Param("limit")
				Param("offset")
			})
		})
	})

	Method("get", func() {
		Description("Get a query investigation with evidence")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Result(Investigation)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/investigations/{id}")
			Response(StatusNotFound, "not_found")
		})
	})

	Method("add_candidate", func() {
		Description("Add a candidate rewrite and compare plans")
		Payload(AddCandidatePayload)
		Result(Investigation)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/investigations/{id}/candidate")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("update_fix", func() {
		Description("Advance the fix lifecycle (proposed -> verified -> applied) and record a PR/ticket link. Marking 'applied' snapshots the query's current latency so the poller can confirm the fix.")
		Payload(UpdateFixPayload)
		Result(Investigation)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/investigations/{id}/fix")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("suggest_rewrite", func() {
		Description("Suggest candidate SQL rewrites from the investigation SQL and plan findings (AST-based; no demo scenarios required)")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Result(RewriteSuggestionList)
		Error("not_found", NotFoundError)
		HTTP(func() {
			POST("/api/v1/investigations/{id}/suggest-rewrite")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("rank_candidates", func() {
		Description("Generate rewrite and index-DDL candidates, dry-EXPLAIN rewrites, project index cost (hypopg or honest heuristic), and rank by cost/partitions")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Attribute("analyze", Boolean, "When true, dry-EXPLAIN uses ANALYZE for timing (slower)", func() {
				Default(false)
			})
			Required("id")
		})
		Result(RankedCandidateList)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/investigations/{id}/rank-candidates")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("generate_report", func() {
		Description("Generate an engineering investigation report")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			// SampleMatch is the *fallback* taken when full-result fingerprinting
			// could not run. It is supporting evidence, not verification, so
			// shipping a report on it takes a deliberate human acknowledgement
			// rather than passing silently as if it were VerifiedEqual.
			Attribute("accept_sample_match", Boolean,
				"Acknowledge that result equivalence rests on a bounded sample, not full-result verification. Required to generate a report when equivalence status is SampleMatch; the resulting report is marked results_sampled in its provenance.")
			Required("id")
		})
		Result(Investigation)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/investigations/{id}/report")
			// Carried as a query parameter, not a body attribute: this endpoint
			// took no request body before, and giving it one would make goa
			// reject every existing body-less POST with MissingPayloadError.
			Params(func() {
				Param("accept_sample_match")
			})
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
			Response(StatusBadRequest, "validation_error")
		})
	})
})

var _ = Service("workspace", func() {
	Description("Workspace overview, regression inbox, demo scenarios, and security trust")

	Method("overview", func() {
		Description("PostgreSQL evidence summary for the landing dashboard")
		Result(WorkspaceOverview)
		HTTP(func() {
			GET("/api/v1/workspace/overview")
		})
	})

	Method("regressions", func() {
		Description("Regression inbox: queries requiring attention")
		Payload(func() {
			Attribute("limit", Int32, func() {
				Default(10)
				Minimum(1)
				Maximum(50)
			})
			Attribute("include_acknowledged", Boolean, func() {
				Default(false)
			})
		})
		Result(RegressionInbox)
		HTTP(func() {
			GET("/api/v1/workspace/regressions")
			Params(func() {
				Param("limit")
				Param("include_acknowledged")
			})
		})
	})

	Method("acknowledge_regression", func() {
		Description("Acknowledge a regression alert")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Error("not_found", NotFoundError)
		HTTP(func() {
			POST("/api/v1/workspace/regressions/{id}/acknowledge")
			Response(StatusNoContent)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("demo_scenarios", func() {
		Description("Guided demo scenarios for Query Investigation")
		Result(DemoScenarioList)
		HTTP(func() {
			GET("/api/v1/demo/scenarios")
		})
	})

	Method("security_trust", func() {
		Description("Security and trust posture for the Security & Trust page")
		Payload(func() {
			Attribute("connection_id", String, "Optional connection ID; defaults to server default connection")
		})
		Result(SecurityTrust)
		Error("validation_error", ValidationError)
		HTTP(func() {
			GET("/api/v1/trust")
			Params(func() {
				Param("connection_id")
			})
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
		})
	})
})
