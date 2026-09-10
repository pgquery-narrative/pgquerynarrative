#!/bin/sh
set -e

CMD="${1:-up}"
DB_URL="${2:-}"

# Pinned to the version go.mod depends on, so this CLI and the library the tests
# link against cannot drift. `@latest` broke every migration job the day v4.20.1
# shipped requiring a newer Go than the pinned container image provided.
MIGRATE_VERSION="${MIGRATE_VERSION:-v4.19.1}"
MIGRATE_PKG="github.com/golang-migrate/migrate/v4/cmd/migrate@${MIGRATE_VERSION}"

case "$CMD" in
  up)
    if [ -z "$DB_URL" ]; then echo "Usage: ./tools/db/migrate.sh up <database_url>"; exit 1; fi
    go run -tags 'postgres' "$MIGRATE_PKG" -path ./app/db/migrations -database "$DB_URL" up
    ;;
  down)
    if [ -z "$DB_URL" ]; then echo "Usage: ./tools/db/migrate.sh down <database_url>"; exit 1; fi
    go run -tags 'postgres' "$MIGRATE_PKG" -path ./app/db/migrations -database "$DB_URL" down
    ;;
  version)
    if [ -z "$DB_URL" ]; then echo "Usage: ./tools/db/migrate.sh version <database_url>"; exit 1; fi
    go run -tags 'postgres' "$MIGRATE_PKG" -path ./app/db/migrations -database "$DB_URL" version
    ;;
  force)
    VERSION="${2:-}"
    DB_URL="${3:-}"
    if [ -z "$VERSION" ] || [ -z "$DB_URL" ]; then
      echo "Usage: ./tools/db/migrate.sh force <version> <database_url>"
      echo "  Use after a failed migration to set schema version (e.g. force 6 then run up again)."
      exit 1
    fi
    go run -tags 'postgres' "$MIGRATE_PKG" -path ./app/db/migrations -database "$DB_URL" force "$VERSION"
    ;;
  *)
    echo "Unknown command: $CMD"
    echo "Usage: ./tools/db/migrate.sh up|down|version|force <database_url>"
    echo "  force: ./tools/db/migrate.sh force <version> <database_url>"
    exit 1
    ;;
esac
