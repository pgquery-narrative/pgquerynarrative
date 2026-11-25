// Echo integration example: embed PgQueryNarrative in an Echo server using middleware.
//
// Build from repo root: go build -o bin/example-echo ./examples/echo-integration
// Run: set DATABASE_* and LLM_* then ./bin/example-echo
// Endpoints (prefix /api): POST /api/query/run, POST /api/report/generate,
// GET /api/schema, GET /api/suggestions/queries
package main

import (
	"context"
	"os"

	"github.com/labstack/echo/v4"
	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
	narrativemw "github.com/pgquerynarrative/pgquerynarrative/pkg/narrative/middleware"
)

func main() {
	ctx := context.Background()
	cfg := narrative.LoadConfig()
	client, err := narrative.NewClient(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	e := echo.New()
	narrativemw.MountEcho(e, client, "/api")

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	_ = e.Start(addr)
}
