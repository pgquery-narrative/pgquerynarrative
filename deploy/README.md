# Deployment

## One image, one model

PgQueryNarrative ships as a **single container image**: the Go server serves both the
JSON API and the built React SPA from `frontend/dist`. There is no separate frontend
container, no sidecar, and no reverse proxy required to serve the UI.

That image is defined by exactly one file — the repository-root [`Dockerfile`](../Dockerfile).
Every deployment path below consumes that same image:

| Path | Where | Notes |
|------|-------|-------|
| Release image | `ghcr.io/pgquery-narrative/pgquerynarrative:<version>` | Built and signed by [`release.yml`](../.github/workflows/release.yml) from the root `Dockerfile`. |
| Local / dev Compose | [`docker-compose.yml`](../docker-compose.yml) | `make start-docker`. Localhost-bound, dev defaults. |
| Production-shaped Compose | [`docker/docker-compose.yml`](docker/docker-compose.yml) | Builds the root `Dockerfile`; `APP_ENV=production` StrictMode, no published Postgres port. |
| Kubernetes | [`kubernetes/`](kubernetes/) | Plain manifests. Set `image:` to a published tag. |
| Helm | [`helm/pgquerynarrative/`](helm/pgquerynarrative/) | Preferred for real deployments. Set `image.repository` / `image.tag`. |

Build it directly with:

```bash
docker build -t pgquerynarrative:dev .
```

### Why this is written down

`deploy/docker/` previously carried a second, server-only `Dockerfile` that did **not**
build the SPA and pinned `goa@latest` instead of the repo's `goa@v3.24.1`. Nothing built
it — `deploy/docker/docker-compose.yml` already pointed at the root `Dockerfile` — but the
docs still advertised it as "the production image", so following them produced an image
whose UI routes served nothing and whose generated API code could drift from the committed
tree. It has been removed. If you need a server-only variant, add a build target to the root
`Dockerfile` rather than a second image definition.

## What the image contains

- `/app/bin/server` — the API + SPA server (`CGO_ENABLED=1`, needed by `pg_query_go`)
- `/app/bin/migrate` — golang-migrate CLI, used by the entrypoint
- `/app/frontend/dist` — the built SPA
- `/app/app/db/migrations` — migration files
- `/app/tools/db/seed.sql` — optional demo seed, applied only when `PGQUERYNARRATIVE_SEED=true`

The entrypoint ([`tools/docker/entrypoint.sh`](../tools/docker/entrypoint.sh)) waits for
Postgres, runs `migrate up`, optionally seeds, then execs the server. It runs as the
non-root `appuser` (uid 1000) and listens on `8080`.

## Related

- [Deployment reference](../docs/reference/deployment.md) — Compose, Kubernetes, and Helm walkthroughs
- [Operations](../docs/reference/operations.md) — upgrade, rollback, backup
- [Branch protection](../docs/ops/branch-protection.md) — required checks on `main`
- [RELEASING.md](../RELEASING.md) — the gate that must be green before tagging
