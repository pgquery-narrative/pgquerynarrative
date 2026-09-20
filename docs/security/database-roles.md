# Database roles

Three identities, deliberately unequal in power.

| Role | Configured by | Used for | Lifetime |
|---|---|---|---|
| **Migration** | `DATABASE_MIGRATION_USER`/`_PASSWORD`, or `DATABASE_MIGRATION_URL` | Runs `migrate up` once at container start; creates extensions (`pg_stat_statements`, `vector`, `hypopg`) and `ALTER ROLE` | Container entrypoint only. The variables are unset from the environment before the server process execs, so a compromised server never holds them |
| **App** (`DATABASE_USER`) | `DATABASE_USER`/`DATABASE_PASSWORD` | All application metadata: organizations, users, API keys, sessions, saved queries, reports, investigations, regression data, schedules, audit log | Server process. `NOBYPASSRLS`, so the organization row-level-security policies apply to it too |
| **Read-only** (`DATABASE_READONLY_USER`, or per connection) | `DATABASE_READONLY_USER`/`_PASSWORD`, or `readOnlyUser` in `DATABASE_CONNECTIONS_JSON` / per-org secrets | Every user-supplied statement: run, EXPLAIN, ANALYZE, compare, result verification, `pg_stat_statements` reads | Server process, one pool per connection |

If the read-only role is a superuser, or can write, or shares a password with the app
role, the model is defeated — use a real read-only grant set in production.

## What each role can and cannot do

- **Migration role** may `CREATE EXTENSION` and `ALTER ROLE`. It has no other special
  status; it is used for exactly one command and then discarded.
- **App role** has ordinary DML on `app.*` (except `app.audit_logs`, which it can only insert into and read) and is subject to the same row-level
  security as everyone else at the SQL level — the application enforces isolation by
  setting `app.current_org_id` per transaction (see
  [Organizations and tenancy](tenancy.md)), not by bypassing RLS.
- **Read-only role** has every privilege on the `app` schema and on `public`
  explicitly revoked (`infra/postgres-init/00-init.sql`, migration `000043`), plus
  default privileges revoked so future tables in those schemas aren't accidentally
  exposed. It has `USAGE`/`SELECT` on the schemas you allowlist, and nothing else —
  the schema allowlist enforced in the application (`DATABASE_ALLOWED_SCHEMAS`) is a
  second, independent layer on top of these grants, not a substitute for them.

The one place a read-only pool briefly gains write capability is the HypoPG index
projection, which runs `SET LOCAL transaction_read_only = off` inside a single
transaction to create a **hypothetical** index (`hypopg_create_index`), then resets
it and rolls back — nothing is committed to your schema.

PostgreSQL lets a role change its own stored defaults (`ALTER ROLE … RESET statement_timeout`, and
so on) whenever it holds a read-write transaction, and nothing can prevent that. So the application
does not depend on them: every pooled connection sets the search path and the three timeouts itself,
and every user statement runs in an explicit `READ ONLY` transaction, whatever the role's defaults
say (an integration test wipes them and checks). The stored defaults protect anyone who connects
with the role directly; keep those credentials out of reach and compare `pg_roles.rolconfig`
with the migrations if in doubt.

## On a fresh database

Without a migration credential set, the entrypoint falls back to running migrations
as `DATABASE_USER`, which fails at migration `000019` with
`permission denied to create extension "pg_stat_statements"`. Under production
StrictMode the entrypoint instead **refuses to start** with that same condition,
rather than leaving the schema half-migrated. Set `DATABASE_MIGRATION_USER` /
`DATABASE_MIGRATION_PASSWORD` (or `DATABASE_MIGRATION_URL`) to a role that may create
extensions and alter roles before first start. Against an already-migrated database,
the fallback is harmless — `migrate up` is a no-op.

## Verifying the boundary

`tools/db/verify_security.sh` (run in CI on every pull request as the `DB security
verify` job) checks, as the read-only role:

- No `INSERT` into an analytical table, no `CREATE TABLE`
- No read access to `pg_authid` or `app.saved_queries`, no `USAGE` on the `app` schema
- No privileged role membership
- The HypoPG read-write boundary still resets correctly

The server runs the same write and DDL probes at startup in production, with the read-only
session flag lifted first, so the result depends on the role's privileges and not on the
`default_transaction_read_only=on` the migrations set.

## See also

[Trust model](../trust-model.md) · [Query execution safety](query-safety.md) ·
[Architecture — database identities](../architecture.md#database-identities) ·
[Deployment](../operate/deployment.md) ·
[Install the pqn extension](../getting-started/pqn-installation.md#install-without-a-superuser) (the roles of extension mode)
