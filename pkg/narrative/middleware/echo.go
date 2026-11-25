package middleware

import (
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
)

// MountEcho mounts PgQueryNarrative HTTP handlers on the Echo instance or group under prefix.
// Routes: POST prefix/query/run, POST prefix/report/generate, GET prefix/schema,
// GET prefix/suggestions/queries. Prefix is normalized (no trailing slash).
// Client must not be nil.
func MountEcho(e *echo.Echo, client *narrative.Client, prefix string) {
	prefix = strings.TrimSuffix(prefix, "/")
	var g *echo.Group
	if prefix == "" {
		g = e.Group("")
	} else {
		g = e.Group(prefix)
	}
	g.POST("/query/run", wrapEcho(client, HandleRunQuery))
	g.POST("/report/generate", wrapEcho(client, HandleGenerateReport))
	g.GET("/schema", wrapEcho(client, HandleGetSchema))
	g.GET("/suggestions/queries", wrapEcho(client, HandleSuggestionsQueries))
}

func wrapEcho(client *narrative.Client, fn func(*narrative.Client, http.ResponseWriter, *http.Request)) echo.HandlerFunc {
	return func(c echo.Context) error {
		fn(client, c.Response().Writer, c.Request())
		return nil
	}
}
