// Gin integration example: embed PgQueryNarrative in a Gin server using middleware.
//
// Build from repo root: go build -o bin/example-gin ./examples/gin-integration
// Run: set DATABASE_* and LLM_* then ./bin/example-gin
// Endpoints (prefix /api): POST /api/query/run, POST /api/report/generate,
// GET /api/schema, GET /api/suggestions/queries
package main

import (
	"context"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/pgquerynarrative/pgquerynarrative/internal/config"
	"github.com/pgquerynarrative/pgquerynarrative/pkg/narrative"
	narrativemw "github.com/pgquerynarrative/pgquerynarrative/pkg/narrative/middleware"
)

func main() {
	ctx := context.Background()
	cfg := narrative.FromAppConfig(config.Load())
	client, err := narrative.NewClient(ctx, cfg)
	if err != nil {
		panic(err)
	}
	defer client.Close()

	r := gin.Default()
	narrativemw.MountGin(r, client, "/api")

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	_ = r.Run(addr)
}
