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

	Method("generate_report", func() {
		Description("Generate an engineering investigation report")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Result(Investigation)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/investigations/{id}/report")
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
		Result(SecurityTrust)
		HTTP(func() {
			GET("/api/v1/trust")
		})
	})
})
