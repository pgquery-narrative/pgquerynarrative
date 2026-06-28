package design

import (
	. "goa.design/goa/v3/dsl"
)

var _ = API("pgquerynarrative", func() {
	Title("PgQueryNarrative API")
	Description("Postgres-native query intelligence: secure read-only SQL, EXPLAIN plan analysis, and optional AI narratives")
	Version("v1")
	Server("pgquerynarrative", func() {
		Host("localhost", func() {
			URI("http://localhost:8080")
		})
	})
})
