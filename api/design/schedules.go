package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = Service("schedules", func() {
	Description("Scheduled report generation and delivery")

	Method("list", func() {
		Result(ScheduleListResult)
		HTTP(func() {
			GET("/api/v1/schedules")
			Response(StatusOK)
		})
	})

	Method("create", func() {
		Payload(ScheduleInput)
		Result(Schedule)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/schedules")
			Response(StatusOK)
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("update", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Extend(ScheduleInput)
			Required("id")
		})
		Result(Schedule)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			PUT("/api/v1/schedules/{id}")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("delete", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Required("id")
		})
		Result(Empty)
		HTTP(func() {
			DELETE("/api/v1/schedules/{id}")
			Response(StatusNoContent)
		})
	})

	Method("run_now", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Required("id")
		})
		Result(ScheduleRunResult)
		Error("not_found", NotFoundError)
		HTTP(func() {
			POST("/api/v1/schedules/{id}/run")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("list_runs", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Required("id")
		})
		Result(ScheduleRunListResult)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/schedules/{id}/runs")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("retry_run", func() {
		Payload(func() {
			Attribute("run_id", String, func() { Format(FormatUUID) })
			Required("run_id")
		})
		Result(ScheduleRunRecord)
		Error("not_found", NotFoundError)
		Error("validation_error", ValidationError)
		HTTP(func() {
			POST("/api/v1/schedule-runs/{run_id}/retry")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
			Response(StatusBadRequest, "validation_error")
		})
	})

	Method("list_deliveries", func() {
		Result(WebhookDeliveryListResult)
		HTTP(func() {
			GET("/api/v1/webhook-deliveries")
			Response(StatusOK)
		})
	})
})

var ScheduleInput = Type("ScheduleInput", func() {
	Attribute("name", String, func() { MinLength(1); MaxLength(200) })
	Attribute("saved_query_id", String, func() { Format(FormatUUID) })
	Attribute("sql", String, func() { MaxLength(10000) })
	Attribute("connection_id", String)
	Attribute("interval_expr", String, "Use @every <duration> format (e.g. @every 6h). Formerly named cron_expr.")
	Attribute("timezone", String, "IANA time zone for scheduling (default UTC)")
	Attribute("destination_type", String, "webhook|log")
	Attribute("destination_target", String, "Webhook URL (required for webhook); optional for log")
	Attribute("enabled", Boolean)
	Required("name", "interval_expr", "destination_type")
})

var Schedule = Type("Schedule", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("name", String)
	Attribute("saved_query_id", String, func() { Format(FormatUUID) })
	Attribute("sql", String)
	Attribute("connection_id", String)
	Attribute("interval_expr", String)
	Attribute("timezone", String)
	Attribute("destination_type", String)
	Attribute("destination_target", String)
	Attribute("enabled", Boolean)
	Attribute("last_run_at", String, func() { Format(FormatDateTime) })
	Attribute("last_status", String)
	Attribute("last_error", String)
	Attribute("next_run_at", String, func() { Format(FormatDateTime) })
	Attribute("created_at", String, func() { Format(FormatDateTime) })
	Attribute("updated_at", String, func() { Format(FormatDateTime) })
	Required("id", "name", "connection_id", "interval_expr", "destination_type", "enabled", "created_at", "updated_at")
})

var ScheduleListResult = Type("ScheduleListResult", func() {
	Attribute("items", ArrayOf(Schedule))
	Required("items")
})

var ScheduleRunResult = Type("ScheduleRunResult", func() {
	Attribute("schedule", Schedule)
	Attribute("report_id", String, func() { Format(FormatUUID) })
	Attribute("delivered", Boolean)
	Required("schedule", "delivered")
})

var ScheduleRunRecord = Type("ScheduleRunRecord", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("schedule_id", String, func() { Format(FormatUUID) })
	Attribute("status", String)
	Attribute("attempt_count", Int)
	Attribute("scheduled_for", String, func() { Format(FormatDateTime) })
	Attribute("started_at", String, func() { Format(FormatDateTime) })
	Attribute("completed_at", String, func() { Format(FormatDateTime) })
	Attribute("report_id", String, func() { Format(FormatUUID) })
	Attribute("failure_code", String)
	Attribute("failure_message", String)
	Required("id", "schedule_id", "status", "attempt_count", "scheduled_for")
})

var ScheduleRunListResult = Type("ScheduleRunListResult", func() {
	Attribute("items", ArrayOf(ScheduleRunRecord))
	Required("items")
})

var WebhookDeliveryRecord = Type("WebhookDeliveryRecord", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("schedule_id", String, func() { Format(FormatUUID) })
	Attribute("destination_url", String)
	Attribute("status", String)
	Attribute("attempt_count", Int)
	Attribute("http_status", Int)
	Attribute("error_message", String)
	Attribute("created_at", String, func() { Format(FormatDateTime) })
	Attribute("completed_at", String, func() { Format(FormatDateTime) })
	Required("id", "destination_url", "status", "attempt_count", "created_at")
})

var WebhookDeliveryListResult = Type("WebhookDeliveryListResult", func() {
	Attribute("items", ArrayOf(WebhookDeliveryRecord))
	Required("items")
})
