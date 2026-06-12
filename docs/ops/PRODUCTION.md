# Production operations

Operational outline for running PgQueryNarrative against PostgreSQL in production.
This repo ships a **Docker Compose dev stack** (`postgres:16-alpine`, volume
`pgquerynarrative_data`, roles `pgquerynarrative_app` / `pgquerynarrative_readonly`,
schema `demo` with partitioned `demo.sales`). The sections below map directly to
those artifacts — adapt hostnames, credentials, and retention to your environment.

**Related:** [Demo dataset](../DATASET.md) · [Query optimization case study](../case-studies/01-query-optimization.md) · [Migrations](../../app/db/migrations/)

---

## 1. Backup and restore

### What to protect

| Asset | Location in this repo | Notes |
|---|---|---|
| PostgreSQL data directory | Docker volume `pgquerynarrative_data` → `/var/lib/postgresql/data` | All roles, schemas, migrations state, seeded `demo.sales` |
| Migration history | `schema_migrations` table (golang-migrate) | Version must match `app/db/migrations/` |
| App metadata | `app.saved_queries`, `app.reports`, etc. | Owned by `pgquerynarrative_app`; not in `demo` |
| Demo analytics data | `demo.sales` (+ 49 monthly partitions) | ~1.7 GB at 10M rows ([DATASET.md](../DATASET.md)); reproducible via `make seed-large-docker` |

Logical backups are sufficient for this app's scale; physical backups and PITR
become important once downtime RPO/RTO requirements tighten or the dataset grows
beyond what a `pg_dump` window can tolerate.

### Logical backup (`pg_dump`)

**Full cluster** (roles, tablespaces — rarely needed for a single-app DB):

```bash
docker compose exec -T postgres pg_dumpall -U postgres --clean --if-exists \
  > pgquerynarrative-$(date +%Y%m%d).sql
```

**Database-only** (typical for this app):

```bash
docker compose exec -T postgres pg_dump -U postgres -Fc \
  -d pgquerynarrative -f /tmp/pgquerynarrative.dump

docker compose cp postgres:/tmp/pgquerynarrative.dump ./backups/
```

`-Fc` (custom format) supports parallel restore and selective object restore.
For a quick human-readable dump, omit `-Fc`.

**Schema-scoped** (app tables only, no 10M-row demo):

```bash
docker compose exec -T postgres pg_dump -U postgres -Fc \
  -d pgquerynarrative -n app -n public \
  > pgquerynarrative-app-$(date +%Y%m%d).dump
```

The `demo` schema can be re-seeded; backing it up separately is optional unless
you rely on custom data beyond `tools/db/seed-large.sql`.

### Restore

From custom-format dump:

```bash
docker compose cp ./backups/pgquerynarrative.dump postgres:/tmp/
docker compose exec -T postgres pg_restore -U postgres \
  --clean --if-exists -d pgquerynarrative /tmp/pgquerynarrative.dump
```

From plain SQL (`pg_dumpall` output):

```bash
docker compose exec -T postgres psql -U postgres -d pgquerynarrative \
  < pgquerynarrative-YYYYMMDD.sql
```

After restore, confirm migration version:

```bash
DB_URL=postgres://postgres:postgres@localhost:5432/pgquerynarrative?sslmode=disable \
  sh ./tools/db/migrate.sh version "$DB_URL"
```

Re-apply the readonly session default if restoring onto a fresh cluster
(migration `000011` also sets this when run as superuser):

```sql
ALTER ROLE pgquerynarrative_readonly SET default_transaction_read_only = on;
```

### PITR sketch (not configured in this repo)

Point-in-time recovery requires **continuous WAL archiving** and **base backups**.
The Compose file does not enable archiving; this is the production upgrade path:

1. **Base backup** — nightly `pg_basebackup` or filesystem snapshot of the data
   directory while Postgres is running (or use a managed provider's automated
   backups).
2. **WAL archive** — set in `postgresql.conf` (or via ConfigMap / RDS parameter
   group):
   ```
   wal_level = replica
   archive_mode = on
   archive_command = 'test ! -f /wal_archive/%f && cp %p /wal_archive/%f'
   ```
   Ship `/wal_archive` to durable object storage (S3, GCS).
3. **Recovery** — restore the latest base backup, create `recovery.signal` (PG 12+)
   or `standby.signal` + `restore_command`, set `recovery_target_time`, start
   Postgres.

For PgQueryNarrative specifically, **RPO trade-off**: losing `app.saved_queries`
/ `app.reports` hurts more than losing re-seedable `demo.sales`. Tier backups:
frequent logical dumps of `app` + `public`, less frequent full DB, WAL only when
you need sub-hour RPO.

### Docker volume snapshot (dev / single-node)

```bash
docker compose stop postgres
docker run --rm -v pgquerynarrative_pgquerynarrative_data:/data \
  -v "$(pwd)/backups":/backup alpine \
  tar czf /backup/pgdata-$(date +%Y%m%d).tar.gz -C /data .
docker compose start postgres
```

Not a substitute for `pg_dump` across Postgres major versions.

---

## 2. Migration strategy (expand/contract + golang-migrate)

### How this repo runs migrations

| Path | Command | DB user | When |
|---|---|---|---|
| Host (Go installed) | `make migrate` | `pgquerynarrative_app` via `DB_URL`, or `postgres` for admin | Local dev |
| Docker-only | `make migrate-docker` | `postgres` superuser (`DOCKER_MIGRATE_DB_URL`) | CI, machines without Go |
| Dirty state recovery | `make migrate-force VERSION=N` then `make migrate` | same as above | After failed migration |

Migrations live in `app/db/migrations/` as numbered pairs
(`000NNN_description.up.sql` / `.down.sql`). The wrapper `tools/db/migrate.sh`
invokes `github.com/golang-migrate/migrate/v4` via `go run`.

**Rules for safe deploys:**

1. Migrations run **before** rolling out app code that depends on them (or use
   expand/contract so old code keeps working during transition).
2. Run as a user that can `CREATE`/`ALTER` in `app` and `demo` — in Docker this
   is `postgres`; production should use a dedicated migration role, not the app
   runtime user.
3. Never edit a migration that has already been applied in any shared environment;
   add `000019_…` instead.
4. Test against a copy with production-like volume: partition migration `000018`
   and `make seed-large-docker` on a 2G+ Postgres instance.

### Expand/contract pattern

Avoid breaking changes in a single deploy. Standard three-phase flow:

| Phase | Database | Application |
|---|---|---|
| **Expand** | Add new column/table/index; keep old | Deploy code that writes both / reads new with fallback |
| **Migrate** | Backfill; dual-write if needed | Monitor; fix stragglers |
| **Contract** | Drop old column/table; remove redundant indexes | Deploy code that only uses new shape |

**Example — adding a column to `app.saved_queries`:**

```sql
-- 000019_expand_saved_queries_foo.up.sql
ALTER TABLE app.saved_queries ADD COLUMN IF NOT EXISTS foo TEXT;

-- 000020_contract_saved_queries_foo.up.sql  (deploy only after backfill + app release)
-- ALTER TABLE app.saved_queries ALTER COLUMN foo SET NOT NULL;
-- (separate migration once backfill complete)
```

**Example — index change on `demo.sales` (see case study):**

Expand: `CREATE INDEX CONCURRENTLY` on parent `demo.sales` (propagates to
partitions). Contract: `DROP INDEX` old index only after query plans use the new
one. The case study's covering index added **426 MB** — that is an expand-phase
cost; dropping `idx_sales_region` would be contract only if nothing else needs it.

### What `000018` did (partition conversion)

Migration `000018_partition_demo_sales` is a **controlled table swap**, not a
classic online expand/contract:

1. Drop dependent view `demo.sales_summary`.
2. `ALTER TABLE demo.sales RENAME TO sales_legacy`.
3. Create partitioned `demo.sales` + monthly child partitions.
4. `INSERT INTO demo.sales SELECT … FROM sales_legacy`.
5. Recreate indexes, re-grant `SELECT` to `pgquerynarrative_readonly`, recreate view.
6. Drop `sales_legacy`.

Acceptable for demo/dev refresh; **in production** on a large live table you would:

- Create new partitioned table alongside (`sales_v2`).
- Backfill in batches with replication slot or trigger-based sync.
- Swap views/API pointers, then contract `sales_legacy`.

Always ship a tested `.down.sql`, but treat partition conversions as **one-way**
in production — restore from backup rather than `migrate down` across data motion.

### Rollback

- **App rollback:** deploy previous container image; DB must still match schema
  the old code expects.
- **Migration rollback:** `sh ./tools/db/migrate.sh down "$DB_URL"` — only safe for
  reversible, non-destructive downs. After `000018` or data backfills, prefer
  restore from backup.

---

## 3. Monitoring and alerts

### Health endpoints

| Check | Command / path | Alert if |
|---|---|---|
| Postgres ready | `pg_isready -U postgres` (Compose healthcheck) | Unready > 1 min |
| App ready | `GET http://localhost:8080/ready` (Compose healthcheck) | Non-200 > 2 min |

### `pg_stat_activity` — long-running queries

User queries run on the **read-only pool** (`pgquerynarrative_readonly`) with a
**30s default timeout** (`QUERY_TIMEOUT`, set in `docker-compose.yml` via app env).
Alerts still matter: runaway sessions, migration jobs, or direct `psql` bypass the app.

```sql
SELECT pid,
       usename,
       application_name,
       state,
       now() - query_start AS duration,
       wait_event_type,
       left(query, 120) AS query_preview
FROM pg_stat_activity
WHERE state = 'active'
  AND pid <> pg_backend_pid()
  AND now() - query_start > interval '30 seconds'
ORDER BY duration DESC;
```

**Suggested alerts:**

- Any query active **> 60s** (warning) or **> 5 min** (critical) on production.
- **> 3** concurrent active queries from `pgquerynarrative_readonly` lasting **> 25s**
  (approaching app timeout — indicates need for indexes or stricter limits).
- Blocking: `wait_event_type = 'Lock'` with duration **> 10s**.

Cancel vs terminate:

```sql
SELECT pg_cancel_backend(<pid>);   -- polite
SELECT pg_terminate_backend(<pid>);  -- force
```

The case study query (region rollup on 10M rows) ran **~1.1s before indexing** and
**~145ms after** — sustained multi-second analytics on `demo.sales` without a date
filter is a performance smell ([case study](../case-studies/01-query-optimization.md)).

### Replication lag (when you add a replica)

Not used in the default Compose stack. If you add a read replica for analytics:

```sql
-- On primary: pg_stat_replication
SELECT client_addr, state, sync_state,
       pg_wal_lsn_diff(pg_current_wal_lsn(), replay_lsn) AS lag_bytes
FROM pg_stat_replication;
```

Alert when `lag_bytes` exceeds a threshold (e.g. **> 100 MB** or **> 60s** wall-clock
lag via `replay_lag` on PG 14+). Read-only routing for user SQL must not serve
stale results if the product requires fresh data.

### Disk usage

10M-row `demo.sales` consumes **~1.7 GB** heap + **~678 MB** indexes baseline;
adding the case-study covering index brings total indexes to **~1.1 GB**
([DATASET.md](../DATASET.md), [case study](../case-studies/01-query-optimization.md)).

```sql
-- Database size
SELECT pg_size_pretty(pg_database_size('pgquerynarrative'));

-- demo.sales partitions (parent shows 0; sum children)
SELECT pg_size_pretty(SUM(pg_total_relation_size(c.oid))) AS demo_sales_total
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_class par ON par.oid = i.inhparent
WHERE par.relname = 'sales'
  AND par.relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = 'demo');

-- Largest relations
SELECT schemaname, relname,
       pg_size_pretty(pg_total_relation_size(relid)) AS total,
       pg_size_pretty(pg_relation_size(relid)) AS heap
FROM pg_stat_user_tables
ORDER BY pg_total_relation_size(relid) DESC
LIMIT 15;
```

**Suggested alerts:**

- Volume **> 80%** full (Docker: `pgquerynarrative_data` mount).
- Database growth **> 20%** week-over-week (unexpected seed or log bloat).
- `app` schema size spike (runaway reports / embeddings).

### Bloat and vacuum

Partitioned `demo.sales` with heavy `INSERT` during `make seed-large-docker` benefits
from the migration's post-load `ANALYZE`. Production:

```sql
SELECT schemaname, relname, n_live_tup, n_dead_tup,
       last_vacuum, last_autovacuum, last_analyze
FROM pg_stat_user_tables
WHERE schemaname IN ('demo', 'app')
ORDER BY n_dead_tup DESC
LIMIT 20;
```

Alert when `n_dead_tup` is large relative to `n_live_tup` on hot partitions, or
when `last_autovacuum` is stale on append-heavy children.

### Connection saturation

Compose defaults: `DATABASE_MAX_CONNECTIONS: 5` for the app. Monitor:

```sql
SELECT usename, count(*) FROM pg_stat_activity GROUP BY usename;
```

Alert when connections for `pgquerynarrative_app` or `pgquerynarrative_readonly`
approach `max_connections` (default 100 on Postgres).

### `pg_stat_statements` — top queries (B9)

Enabled in the default Compose stack via `shared_preload_libraries=pg_stat_statements`
and migration `000019_pg_stat_statements` (`CREATE EXTENSION` + `GRANT pg_read_all_stats`
to `pgquerynarrative_readonly`). Complements `POST /api/v1/queries/explain` for plan
regression detection across real workloads.

**First-time / after config change:** `shared_preload_libraries` only applies on server
start. Recreate Postgres, then migrate:

```bash
make postgres-recreate   # docker compose up -d --force-recreate postgres
make migrate-docker      # applies 000019_pg_stat_statements
```

**API** (read-only pool):

```bash
# Top by total time (default)
curl -s 'http://localhost:8080/api/v1/queries/stats?order_by=total_time&limit=10' | jq .

# Top by mean time or call count
curl -s 'http://localhost:8080/api/v1/queries/stats?order_by=mean_time&limit=5' | jq .
curl -s 'http://localhost:8080/api/v1/queries/stats?order_by=calls&limit=5' | jq .
```

UI: **Query Stats** (`/stats`) — same data in a sortable table.

**Direct SQL** (as superuser or `pg_read_all_stats`):

```sql
SELECT left(query, 120) AS query_preview,
       calls,
       round(total_exec_time::numeric, 2) AS total_ms,
       round(mean_exec_time::numeric, 2) AS mean_ms,
       rows
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY total_exec_time DESC
LIMIT 10;
```

**Suggested alerts:**

- A single normalized query's `mean_exec_time` **> 5s** on `pgquerynarrative_readonly`
  (approaching the 30s app timeout).
- `calls` spike **> 3×** baseline hour-over-hour on the same `queryid` (runaway client or missing cache).
- New top-10 entry with `total_exec_time` dominating cluster time after a deploy (plan regression).

Reset stats after benchmark runs: `SELECT pg_stat_statements_reset();` (superuser).

---

## 4. Security model (defense in depth)

PgQueryNarrative exposes **arbitrary read-only SQL** against `demo` (and `public`).
Security is layered: no single control is sufficient.

```
┌─────────────────────────────────────────────────────────────┐
│  Client (browser / API)                                     │
└───────────────────────────┬─────────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────────┐
│  App layer                                                  │
│  · pg_query parse-tree validator (single stmt, SELECT/WITH) │
│  · Allowed schemas: demo, public (DATABASE_ALLOWED_SCHEMAS) │
│  · EXPLAIN unwrap + same inner-query checks (B1)            │
│  · Row cap: SELECT * FROM (<sql>) LIMIT $1 (default 1000)   │
│  · Per-query timeout: 30s (QUERY_TIMEOUT)                   │
│  · Optional: SECURITY_AUTH_ENABLED, rate limits               │
└───────────────────────────┬─────────────────────────────────┘
                            │ pgquerynarrative_readonly
┌───────────────────────────▼─────────────────────────────────┐
│  PostgreSQL                                                 │
│  · GRANT SELECT on demo / public only                       │
│  · default_transaction_read_only = on (migration 000011)    │
│  · No INSERT/UPDATE/DELETE/DDL privileges for query role    │
│  · app schema: pgquerynarrative_app only (saved_queries…)   │
└─────────────────────────────────────────────────────────────┘
```

### Role separation

Created in `infra/postgres-init/00-init.sql` and reinforced in migrations:

| Role | Purpose | `demo.sales` | `app.*` | DDL |
|---|---|---|---|---|
| `postgres` | Superuser, migrations in Docker | full | full | yes |
| `pgquerynarrative_app` | App runtime, writes metadata | owner (bypasses RLS) | read/write | in owned schemas |
| `pgquerynarrative_readonly` | User SQL + EXPLAIN + stats | `SELECT` all rows (RLS policy) | no access | no |
| `pgquerynarrative_sales_rep` | RLS demo only (`psql`; not used by app) | `SELECT` own rep via GUC | no access | no |

| API / path | DB role |
|---|---|
| `POST /api/v1/queries/run` | `pgquerynarrative_readonly` |
| `POST /api/v1/queries/explain` | `pgquerynarrative_readonly` |
| `GET /api/v1/queries/stats` | `pgquerynarrative_readonly` |
| Saved queries, reports, embeddings, schedules | `pgquerynarrative_app` |
| Manual RLS demo | `pgquerynarrative_sales_rep` + `SET app.current_rep` |

User-facing query endpoints use the **read-only pool** (`app/db/connection.go`).
Saved queries, reports, and embeddings use the **app pool** (`pgquerynarrative_app`).
Step-by-step RLS verification: [rls-demo.md](rls-demo.md).

### App-layer validator (B1 / parser-backed)

`app/queryrunner/validator.go` uses **`pg_query_go`** (Postgres parser) to enforce:

- Exactly **one** statement.
- Top-level **SELECT** / **WITH** / **EXPLAIN** of a SELECT.
- Rejection of write/DDL nodes anywhere in the parse tree (including inside CTEs).
- Schema allowlist — default `demo` and `public`; `app` is not exposed to ad-hoc SQL.

[B1](../BACKLOG.md) fixed substring false positives (`created_at` no longer matches
`create`) by walking the AST instead of `strings.Contains` blocklists. **Defense in
depth:** even if validation had a bug, the readonly role cannot write.

`EXPLAIN (FORMAT JSON [, ANALYZE, BUFFERS])` is allowed only when the inner
statement passes the same read-only checks — users can inspect plans without
gaining mutation paths.

### Database-layer read-only enforcement

Migration `000011_readonly_role_transaction_readonly`:

```sql
ALTER ROLE pgquerynarrative_readonly SET default_transaction_read_only = on;
```

Every session as the query role rejects writes even if the app validator were
bypassed. `make start-docker` also re-applies this after migrate.

`demo` grants (from `000003`, preserved in `000018`):

```sql
GRANT USAGE ON SCHEMA demo TO pgquerynarrative_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA demo TO pgquerynarrative_readonly;
```

The readonly role **cannot** read `app.saved_queries` or invoke functions in
untrusted schemas unless explicitly granted — keep it that way.

### Query execution guards

From `app/queryrunner/runner.go`:

1. **Validator** — reject before connect.
2. **Subquery wrap** — `SELECT * FROM (<user sql>) AS pgqn_sub LIMIT $1` caps rows
   (default max 1000 via `MaxRowsPerQuery`).
3. **Context timeout** — `context.WithTimeout` at `QUERY_TIMEOUT` (30s).

`EXPLAIN` with `analyze: true` executes the inner query on the readonly connection;
treat it as production load and keep timeouts enabled.

### Index tradeoff as a security/ops lesson

The [query optimization case study](../case-studies/01-query-optimization.md) documents
a deliberate product choice: **no composite index** on `(region, product_category)`
in the baseline migrations so EXPLAIN can demonstrate seq scans. The fix added
`idx_sales_region_category_covering` — **7.9× faster** but **+426 MB** index storage
and higher write/vacuum cost on every partition.

That tradeoff applies to production hardening too: indexes that reduce long-running
readonly queries also **shrink the blast radius of DoS-by-analytics** (queries
staying under the 30s timeout). Measure with `EXPLAIN (ANALYZE, BUFFERS)` before
adding covering indexes at scale.

### Row-level security (B10 — `demo.sales`)

Migration `000021_sales_rls_demo` enables RLS on the partitioned `demo.sales`
parent (policies apply to all monthly partitions). Two `SELECT` policies:

```sql
ALTER TABLE demo.sales ENABLE ROW LEVEL SECURITY;

-- API path: pgquerynarrative_readonly — unchanged full visibility
CREATE POLICY sales_select_api_readonly ON demo.sales
    FOR SELECT TO pgquerynarrative_readonly USING (true);

-- Demo path: pgquerynarrative_sales_rep — rep-scoped rows
CREATE POLICY sales_select_own_rep ON demo.sales
    FOR SELECT TO pgquerynarrative_sales_rep
    USING (sales_rep = current_setting('app.current_rep', true));
```

The rep demo role is **not** wired to the HTTP API. It exists for `psql` sessions
that `SET app.current_rep = 'A. Lee'` (or another seed rep) before querying
`demo.sales`. Unset GUC → no visible rows (`current_setting(..., true)` is NULL).

`pgquerynarrative_app` owns `demo` and bypasses RLS (no `FORCE ROW LEVEL SECURITY`),
so migrations and `make seed-large-docker` are unaffected.

Defense in depth: validator → role separation → RLS (rep demo) or permissive policy
(API readonly) → `LIMIT`/timeout. Full walkthrough: [rls-demo.md](rls-demo.md).

### Production hardening checklist

- [ ] Replace default passwords in `infra/postgres-init/00-init.sql` / Compose env.
- [ ] `DATABASE_SSL_MODE=require` (or verify-full) against managed Postgres.
- [ ] Enable `SECURITY_AUTH_ENABLED` and set `SECURITY_API_KEY`.
- [ ] Do not expose Postgres port 5432 publicly; app only on private network.
- [ ] Run migrations with a dedicated role; app containers use `pgquerynarrative_app`
      and never superuser.
- [ ] Audit extensions (`pgcrypto`, `pg_stat_statements`) — minimal attack surface; stats view is read-only via `pg_read_all_stats`.

---

## Quick reference (this repo)

```bash
# Stack
make postgres-up          # postgres:16-alpine, 2G limit, volume pgquerynarrative_data
make postgres-recreate    # after shared_preload_libraries change (pg_stat_statements)
make migrate-docker       # golang-migrate, migrations 000001–000021+
make seed-large-docker    # ~10M rows into demo.sales

# Roles (docker-compose.yml defaults)
#   pgquerynarrative_app      — app writes
#   pgquerynarrative_readonly — user SQL (RLS: all rows on demo.sales)
#   pgquerynarrative_sales_rep — RLS demo only (SET app.current_rep)

# Verify readonly enforcement
docker compose exec -T postgres psql -U pgquerynarrative_readonly -d pgquerynarrative \
  -c "INSERT INTO demo.sales DEFAULT VALUES;"   # expect ERROR: read-only transaction

# Verify app health
curl -s http://localhost:8080/ready
```
