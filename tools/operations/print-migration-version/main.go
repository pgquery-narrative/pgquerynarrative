// Command print-migration-version prints internal/db.RequiredMigrationVersion, so scripts like
// pilot_acceptance.sh can read it through the compiler instead of grep+sed-scraping the source
// file (which breaks silently on a gofmt change, an added comment, or a rename, with no error).
package main

import (
	"fmt"

	"github.com/pgquerynarrative/pgquerynarrative/internal/db"
)

func main() {
	fmt.Println(db.RequiredMigrationVersion)
}
