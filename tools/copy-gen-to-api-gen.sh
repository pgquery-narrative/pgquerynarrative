#!/bin/sh
# Sync goa's output from gen/ (ephemeral) into api/gen/ (the single committed
# tree) and rewrite every internal import so nothing in api/gen/ — or in the
# app — points back at gen/. goa always writes to ./gen/; the app never imports
# it, so `go build ./...` works on a clean clone straight from api/gen/.
set -e
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

[ -d gen ] || exit 0

# Service packages: one directory per goa Service, plus the shared http tree.
for d in gen/*/; do
	name=$(basename "$d")
	[ "$name" = "http" ] && continue
	mkdir -p "api/gen/$name"
	cp "$d"*.go "api/gen/$name/"
done
mkdir -p api/gen/http
cp -r gen/http/* api/gen/http/

# gen/<pkg> -> api/gen/<pkg> in every copied file.
find api/gen -name '*.go' -type f -exec sed -i.bak \
	's|github.com/pgquerynarrative/pgquerynarrative/gen/|github.com/pgquerynarrative/pgquerynarrative/api/gen/|g' {} +
find api/gen -name '*.bak' -type f -delete

echo "Synced gen/ -> api/gen/ (all services, imports rewritten)"
