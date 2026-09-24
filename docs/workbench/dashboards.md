# Dashboards

Simple widget dashboards built from saved queries and reports: CRUD plus a
resolve step that fetches each widget's live data in one call.

| Method | Path | Purpose |
|---|---|---|
| GET | `/dashboards` | List dashboards |
| POST | `/dashboards` | Create (`name`) |
| GET | `/dashboards/{id}` | Get a dashboard and its widget definitions |
| PUT | `/dashboards/{id}` | Replace `name` and `widgets[]` |
| DELETE | `/dashboards/{id}` | Delete |
| GET | `/dashboards/{id}/resolve` | Get the dashboard **with each widget's data resolved** |

A widget (`widget_type`, `title`, `position`, `refresh_seconds`) points at either a
`report_id` or a `saved_query_id`. `resolve` runs each widget's underlying query or
loads its report and returns the data inline, so the UI can render a dashboard in one
round trip instead of one request per widget.

There is no scheduled dashboard refresh on the server: `refresh_seconds` is a UI hint
for how often the client should re-call `resolve`, not a background job.

## See also

[REST API](../integrations/rest-api.md) · [Reports and sharing](reports.md)
