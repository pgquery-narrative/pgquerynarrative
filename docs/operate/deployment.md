# Deployment

Build and deploy PgQueryNarrative with Docker, Kubernetes, or Helm. First run:
[Quick start](../getting-started/quickstart.md) or [Installation](../getting-started/installation.md).
Production hardening: [Production configuration](production.md).

## One image, one model

PgQueryNarrative ships as a **single container image**: the Go server serves both
the JSON API and the built React SPA from `frontend/dist`. There is no separate
frontend container, sidecar, or reverse proxy needed to serve the UI. That image is
defined by exactly one file, the repository-root
[`Dockerfile`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/Dockerfile),
and every path below consumes the same image:

| Path | Where | Notes |
|---|---|---|
| Release image | `ghcr.io/pgquery-narrative/pgquerynarrative:<version>` | Built and signed by [`release.yml`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/workflows/release.yml) from the root `Dockerfile` |
| Local / dev Compose | [`docker-compose.yml`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/docker-compose.yml) | `make start-docker`. Localhost-bound, dev defaults |
| Production-shaped Compose | [`deploy/docker/docker-compose.yml`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/deploy/docker/docker-compose.yml) | Builds the root `Dockerfile`; `APP_ENV=production` StrictMode, no published Postgres port |
| Kubernetes | [`deploy/kubernetes/`](https://github.com/pgquery-narrative/pgquerynarrative/tree/main/deploy/kubernetes) | Plain manifests. Set `image:` to a published tag |
| Helm | [`deploy/helm/pgquerynarrative/`](https://github.com/pgquery-narrative/pgquerynarrative/tree/main/deploy/helm/pgquerynarrative) | Parameterized install; chart defaults are StrictMode-aligned |

```bash
docker build -t pgquerynarrative:dev .
```

## What the image contains

- `/app/bin/server`: the API + SPA server (`CGO_ENABLED=1`, needed by `pg_query_go`)
- `/app/bin/migrate`: the golang-migrate CLI, used by the entrypoint
- `/app/frontend/dist`: the built SPA
- `/app/app/db/migrations`: migration files
- `/app/tools/db/seed.sql`: optional demo seed, applied only when `PGQUERYNARRATIVE_SEED=true`

The entrypoint ([`tools/docker/entrypoint.sh`](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/tools/docker/entrypoint.sh))
waits for Postgres, runs `migrate up`, optionally seeds, then execs the server. It
runs as the non-root `appuser` (uid 1000) on port `8080`.

## Migration identity: read this before a production deploy {#migration-identity}

Migrations create extensions and run `ALTER ROLE`; the runtime role deliberately
cannot do either, because it also executes user-supplied SQL. On a **fresh**
database the entrypoint needs a separate, privileged migration credential:

```bash
DATABASE_MIGRATION_USER=postgres
DATABASE_MIGRATION_PASSWORD=...
# or: DATABASE_MIGRATION_URL=postgres://...
```

**Under production StrictMode (`APP_ENV=production`), if no migration credential is
set, the container refuses to start** rather than run migrations as the ordinary app
user and risk a half-applied schema. The bundled Helm chart and Kubernetes manifests
run with `APP_ENV=production` but do **not** set a migration credential or
`PGQUERYNARRATIVE_SKIP_MIGRATIONS`, plan for one of:

- Add `DATABASE_MIGRATION_USER`/`_PASSWORD` (or `_URL`) as a Secret value the
  Deployment consumes, or
- Run migrations as a separate Job/init container with the migration identity, and
  start the main container with `PGQUERYNARRATIVE_SKIP_MIGRATIONS=true`

Against an already-migrated database, an unset migration credential outside
StrictMode is harmless: `migrate up` is a no-op. Full identity model:
[Database roles](../security/database-roles.md).

## Docker {#docker}

### Docker Compose

```bash
docker compose -f deploy/docker/docker-compose.yml up -d
# or build first: ... up -d --build
```

Set env or use `.env`. Required: `DATABASE_*`, and `LLM_*` if you want narratives
([Configuration](../reference/configuration.md)). Optional:
`PGQUERYNARRATIVE_SEED=true` for the demo seed. The app waits for Postgres, runs
migrations, then starts. API and health endpoints:
[http://localhost:8080](../operate/monitoring.md#health-and-readiness).

Pre-built image:

```yaml
services:
  app:
    image: ghcr.io/pgquery-narrative/pgquerynarrative:<version>
```

Replace `<version>` with a released tag, see
[Releases and versioning](../project/releases.md).

## Kubernetes

Manifests: `deploy/kubernetes/`. PostgreSQL is external; the app connects via
`DATABASE_HOST` and credentials from a Secret.

1. Create the database and roles, and run migrations once if the database is empty
   (see [Installation](../getting-started/installation.md) and the migration
   identity note above).
2. Apply in order:

   ```bash
   kubectl apply -f deploy/kubernetes/namespace.yaml
   kubectl apply -f deploy/kubernetes/secret.yaml      # edit with real credentials first, never commit them
   kubectl apply -f deploy/kubernetes/configmap.yaml   # confirm DATABASE_HOST
   kubectl apply -f deploy/kubernetes/deployment.yaml  # set image: to your tag
   kubectl apply -f deploy/kubernetes/service.yaml
   kubectl apply -f deploy/kubernetes/ingress.yaml     # optional
   ```

Probes: `livenessProbe` → `GET /health`, `readinessProbe` → `GET /ready`, see
[Health and monitoring](monitoring.md#health-and-readiness).

**Access:** no Ingress by default: `kubectl port-forward -n pgquerynarrative svc/pgquerynarrative 8080:8080`
→ http://localhost:8080. With Ingress, configure the controller and DNS for the
host in `ingress.yaml`.

## Helm

Chart: `deploy/helm/pgquerynarrative/`. Chart defaults are **StrictMode-aligned**
(`appEnv: production`): install fails on placeholder secrets by design.

```bash
helm install pgqn ./deploy/helm/pgquerynarrative -n pgquerynarrative --create-namespace \
  --set image.repository=ghcr.io/pgquery-narrative/pgquerynarrative \
  --set image.tag=<version> \
  --set database.host=... \
  --set secret.databasePassword=... \
  --set secret.databaseReadonlyPassword=... \
  --set secret.apiKeyHash=... \
  --set secret.sessionSecret=... \
  --set secret.dataEncryptionKey=...
```

Or `-f my-values.yaml`, see `values-production.example.yaml` for a full override
checklist, and `deploy/helm/pgquerynarrative/values.yaml` for every key (image,
database, secret, oidc, webhook, security, llm, resources, security contexts,
networkPolicy, ingress, service). `ci-values.yaml` is the smoke-test overlay used by
the `Helm StrictMode gates` CI job, not a deployment template.

```bash
helm upgrade pgqn ./deploy/helm/pgquerynarrative -n pgquerynarrative
helm uninstall pgqn -n pgquerynarrative
```

The chart sets pod and container security contexts (`runAsNonRoot`, dropped
capabilities, read-only root filesystem), a `NetworkPolicy` restricting ingress to
the ingress controller and same-namespace pods, and probes at `/health`/`/ready`. It
does **not** template a migration Job; apply the migration-identity guidance above
before installing against a fresh database.

## Summary

| Method | Path | Use case |
|---|---|---|
| Docker Compose | `deploy/docker/` | Single host, staging, or small production |
| Kubernetes | `deploy/kubernetes/` | Raw manifests |
| Helm | `deploy/helm/pgquerynarrative/` | Parameterized install, StrictMode-aligned defaults |

## See also

[Production configuration](production.md) · [Health and monitoring](monitoring.md) ·
[Migrations, upgrades, backup](upgrades.md) · [Configuration reference](../reference/configuration.md) ·
[Installation](../getting-started/installation.md)
