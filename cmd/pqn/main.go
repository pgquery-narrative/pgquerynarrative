// Command pqn is the terminal tool for the "pqn" PostgreSQL extension: it investigates a slow
// query from its plan, proposes a fix, and proves the fix returns the same rows faster. It logs in
// to PostgreSQL as you, so there is no server to run and no API key to keep.
package main

import (
	"os"

	"github.com/pgquerynarrative/pgquerynarrative/internal/pqncli"
)

// version is set with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	pqncli.Version = version
	os.Exit(pqncli.Main(os.Args[1:], os.Stdout, os.Stderr, os.Getenv, pqncli.Connect))
}
