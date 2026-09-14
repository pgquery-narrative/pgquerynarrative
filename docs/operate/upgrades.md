# Migrations, upgrades, backup

## Migrations

Migrations use [golang-migrate](https://github.com/golang-migrate/migrate) against
`app/db/migrations/`. The server enforces a **minimum schema version**
(`db.RequiredMigrationVersion`, currently **57**) at readiness time — not at process
start:

- `GET /ready` returns **503** with `schema migration version N < required 57: run
  database migrations` (or `dirty at version N: resolve with migrate force before
  starting`) if the database is behind or the last migration failed partway.
- The server **process itself still starts and accepts connections** — only
  readiness fails, so a load balancer correctly stops routing to it, but a health
  check that only looks at `/health` will not catch this. Watch `/ready`, not
  `/health`, during a rollout.

| Command | Effect |
|---|---|
| `make migrate` | Apply pending migrations (uses `DB_URL`, or `LOCAL_DB_URL` for local host Postgres) |
| `make migrate-force VERSION=N` | Force the tracked version without running SQL — for clearing a dirty state after a manual fix |
| `make migrate-docker` | Apply migrations inside Docker, as `postgres`, against the Compose stack |
| `make migrate-cycle-docker` | Up, then `down -all`, then up — proves migrations are reversible (CI job `Migration up/down/up`) |
| `make db-security-verify-docker` | Runs `tools/db/verify_security.sh` against the Compose stack |

A dirty migration (a prior `up` that failed midway) must be resolved by hand:
inspect what partially applied, fix it, then `migrate force <version>` to clear the
dirty flag before running `up` again.

## Upgrading

1. Read the release notes for the target version — a bump to
   `RequiredMigrationVersion` is called out there, with the version range.
2. Run migrations for the new version **before** starting the new binary
   (`make migrate` / `make migrate-docker`, or your own migration Job).
3. Deploy the new image (see [Deployment](deployment.md)).
4. Confirm `GET /ready` is `200` on the new version.

Upgrading from a `2.0.x` install specifically requires running migrations through
schema version 57 first — the server on a database behind that version will report
`/ready` as unhealthy rather than boot-loop, but it will not serve traffic correctly
either.

## Rollback

1. **Compose:** revert the image tag, `docker compose ... up -d`.
2. **Kubernetes:** `kubectl rollout undo deployment/pgquerynarrative -n pgquerynarrative`,
   or revert `deployment.yaml` and re-apply.
3. **Helm:** `helm rollback pgqn -n pgquerynarrative`, or upgrade with the previous
   `image.tag`.
4. Confirm `GET /ready` is `200` on the rolled-back version.

**Rolling back the binary does not roll back the schema.** Migrations here are
forward-only; if the new version's migrations are incompatible with the old binary,
rolling back the image alone leaves the old binary pointed at a schema it doesn't
understand. Plan schema changes to be backward-compatible for at least one release,
or restore from backup instead of rolling back in place.

## Backup and restore

```bash
# Logical backup: app + demo + opendata schemas, plus schema_migrations
tools/ops/backup.sh [output-file]
DATABASE_HOST=... DATABASE_USER=postgres DATABASE_PASSWORD=... tools/ops/backup.sh /tmp/pgqn.dump

# Restore into an existing (usually empty) database
tools/ops/restore.sh backup.sql.gz
DATABASE_NAME=pgqn_drill tools/ops/restore.sh /tmp/pgqn.dump
```

`backup.sh` prefers the host's `pg_dump`, falling back to `docker compose exec
postgres pg_dump` when Compose is running; it uses `--no-owner --no-acl`, so a
restore **does not** recreate roles or grants — apply
`infra/postgres-init/00-init.sql` (or your own role provisioning) first, then
restore the data.

**Verify a restore before trusting it**: restore into a scratch database
(`DATABASE_NAME=pgqn_drill`), run `tools/db/verify_security.sh` against it, and spot
-check row counts on `app.investigations` and `demo.sales` before treating the
backup as good.

## See also

[Deployment](deployment.md) · [Health and monitoring](monitoring.md) ·
[Troubleshooting and runbooks](troubleshooting.md) ·
[Releases and versioning](../project/releases.md)
