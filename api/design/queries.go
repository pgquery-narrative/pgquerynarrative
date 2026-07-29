package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = Service("queries", func() {
	Description("Query execution and management")

	Method("run", func() {
		Description("Execute a SQL query and return results")
		Payload(RunQueryPayload)
		Result(RunQueryResult)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/queries/run")
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("stat_statements", func() {
		Description("List top queries from pg_stat_statements (read-only observability)")
		Payload(func() {
			Attribute("order_by", String, "Sort key: total_time, mean_time, or calls", func() {
				Enum("total_time", "mean_time", "calls")
				Default("total_time")
			})
			Attribute("limit", Int32, func() {
				Default(20)
				Minimum(1)
				Maximum(100)
			})
			Attribute("connection_id", String, "Optional connection ID; defaults to server default connection")
		})
		Result(StatStatementsResult)
		Error("validation_error", ValidationError)
		HTTP(func() {
			GET("/api/v1/queries/stats")
			Params(func() {
				Param("order_by")
				Param("limit")
				Param("connection_id")
			})
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("explain_plan", func() {
		Description("Run EXPLAIN (FORMAT JSON) on a read-only query and return plan analysis")
		Payload(ExplainQueryPayload)
		Result(ExplainQueryResult)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/queries/explain")
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("compare_plans", func() {
		Description("Compare before and after execution plans for a query rewrite or index change")
		Payload(ComparePlansPayload)
		Result(ComparePlansResult)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/queries/explain/compare")
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("list_saved", func() {
		Description("List saved queries")
		Payload(func() {
			Attribute("tags", ArrayOf(String))
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
		Result(SavedQueryList)
		HTTP(func() {
			GET("/api/v1/queries/saved")
			Params(func() {
				Param("tags")
				Param("connection_id")
				Param("limit")
				Param("offset")
			})
		})
	})

	Method("save", func() {
		Description("Save a query")
		Payload(SaveQueryPayload)
		Result(SavedQuery)
		HTTP(func() {
			POST("/api/v1/queries/saved")
			Response(StatusOK)
		})
	})

	Method("get_saved", func() {
		Description("Get saved query by ID")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Result(SavedQuery)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/queries/saved/{id}")
			Response(StatusNotFound, "not_found")
		})
	})

	Method("delete_saved", func() {
		Description("Delete a saved query")
		Payload(func() {
			Attribute("id", String, func() {
				Format(FormatUUID)
			})
			Required("id")
		})
		Error("not_found", NotFoundError)
		HTTP(func() {
			DELETE("/api/v1/queries/saved/{id}")
			Response(StatusNoContent)
			Response(StatusNotFound, "not_found")
		})
	})
})
