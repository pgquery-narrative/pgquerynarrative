// Chi integration example: embed PgQueryNarrative in a Chi router using middleware.
//
// Build from repo root: go build -o bin/example-chi ./examples/chi-integration
// Run: set DATABASE_* and LLM_* then ./bin/example-chi
// Endpoints (prefix /api): POST /api/query/run, POST /api/report/generate,
// GET /api/schema, GET /api/suggestions/queries
package main

import (
	"context"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/pgquerynarrative/pgquerynarrative/app/config"
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

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	narrativemw.MountChi(r, client, "/api")

	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	_ = srv.ListenAndServe()
}
