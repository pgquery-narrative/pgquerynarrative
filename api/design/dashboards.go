package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = Service("dashboards", func() {
	Description("Dashboard CRUD and widget resolution")

	Method("list", func() {
		Result(DashboardListResult)
		HTTP(func() {
			GET("/api/v1/dashboards")
			Response(StatusOK)
		})
	})

	Method("create", func() {
		Payload(func() {
			Attribute("name", String, func() { MinLength(1); MaxLength(200) })
			Required("name")
		})
		Result(Dashboard)
		HTTP(func() {
			POST("/api/v1/dashboards")
			Response(StatusOK)
		})
	})

	Method("get", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Required("id")
		})
		Result(Dashboard)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/dashboards/{id}")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("update", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Attribute("name", String, func() { MinLength(1); MaxLength(200) })
			Attribute("widgets", ArrayOf(DashboardWidgetInput))
			Required("id", "name")
		})
		Result(Dashboard)
		Error("not_found", NotFoundError)
		HTTP(func() {
			PUT("/api/v1/dashboards/{id}")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("delete", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Required("id")
		})
		Result(Empty)
		Error("not_found", NotFoundError)
		HTTP(func() {
			DELETE("/api/v1/dashboards/{id}")
			Response(StatusNoContent)
			Response(StatusNotFound, "not_found")
		})
	})

	Method("resolve", func() {
		Payload(func() {
			Attribute("id", String, func() { Format(FormatUUID) })
			Required("id")
		})
		Result(DashboardResolved)
		Error("not_found", NotFoundError)
		HTTP(func() {
			GET("/api/v1/dashboards/{id}/resolve")
			Response(StatusOK)
			Response(StatusNotFound, "not_found")
		})
	})
})

var Dashboard = Type("Dashboard", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("name", String)
	Attribute("widgets", ArrayOf(DashboardWidget))
	Attribute("created_at", String, func() { Format(FormatDateTime) })
	Attribute("updated_at", String, func() { Format(FormatDateTime) })
	Required("id", "name", "widgets", "created_at", "updated_at")
})

var DashboardListResult = Type("DashboardListResult", func() {
	Attribute("items", ArrayOf(Dashboard))
	Required("items")
})

var DashboardWidget = Type("DashboardWidget", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("widget_type", String)
	Attribute("title", String)
	Attribute("report_id", String, func() { Format(FormatUUID) })
	Attribute("saved_query_id", String, func() { Format(FormatUUID) })
	Attribute("refresh_seconds", Int32)
	Attribute("position", Int32)
	Required("id", "widget_type", "refresh_seconds", "position")
})

var DashboardWidgetInput = Type("DashboardWidgetInput", func() {
	Attribute("widget_type", String)
	Attribute("title", String)
	Attribute("report_id", String, func() { Format(FormatUUID) })
	Attribute("saved_query_id", String, func() { Format(FormatUUID) })
	Attribute("refresh_seconds", Int32)
	Attribute("position", Int32)
	Required("widget_type")
})

var DashboardResolved = Type("DashboardResolved", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("name", String)
	Attribute("widgets", ArrayOf(DashboardResolvedWidget))
	Required("id", "name", "widgets")
})

var DashboardResolvedWidget = Type("DashboardResolvedWidget", func() {
	Attribute("id", String, func() { Format(FormatUUID) })
	Attribute("widget_type", String)
	Attribute("title", String)
	Attribute("refresh_seconds", Int32)
	Attribute("position", Int32)
	Attribute("report", Report)
	Attribute("saved_query", SavedQuery)
	Required("id", "widget_type", "refresh_seconds", "position")
})
