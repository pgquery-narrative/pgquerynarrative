# Install the pqn extension

Two commands on a superuser connection: `CREATE EXTENSION pqn` and `SELECT pqn_api.init()`. Everything else is choosing what
analysts may see and who they are. To try it first, use the [Quick start](pqn-extension.md).

`bash` blocks are run by `make verify-pqn-docs`; `shell` blocks depend on your machine. Examples use the local socket; add
`-h host -p port` or set `PGHOST` for a remote server.

## Requirements

- PostgreSQL **16 or later** (tested on 16, 17 and 18). On 15 the extension installs, but a statement with `$n` placeholders, which is every statement `pqn top` lists, cannot be planned. The setup check says so.
- A server you can add files to: self-managed or a container. Managed services that refuse custom extension files cannot run `pqn` yet.
- `pg_stat_statements` in `shared_preload_libraries`, for `pqn top` only.
- The `pqn` tool: `bin/pqn` in the [release archive](../project/releases.md), or build it with Go 1.26 and a C toolchain.

## 1. Get the extension onto the server

| You have | Do |
|---|---|
| A new server | **Use the image.** Run `ghcr.io/pgquery-narrative/pgquerynarrative/pqn-postgres:18` (16 and 17 too), or build it with `make build-pqn-image`. The extension is created and `pg_stat_statements` preloaded on first start, so go to step 3. Other bases: `--build-arg POSTGRES_IMAGE=postgres:17` |
| An existing database on a volume | Swap in the same image on the same data directory and run `CREATE EXTENSION pqn; SELECT pqn_api.init();`. The first-start setup runs only on a new data directory, so add `pg_stat_statements` to your own `shared_preload_libraries` |
| A server and a clone of this repository | `make install-pqn-extension`. `PG_CONFIG=/path/to/pg_config` picks an installation |
| A server, no clone | Unpack the release archive and run `pqn-extension/install.sh`. Or copy `pqn.control`, `pqn--1.0.sql` and `pqn--1.0--1.1.sql` into `$(pg_config --sharedir)/extension` |

```shell
make build-pqn-image
docker run -d -p 127.0.0.1:5432:5432 -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=app pqn-postgres:18
```

The extension's `default_version` is **1.1**, and it needs both `.sql` files (1.0, then the step to 1.1). No restart is needed.
`tools/docker/postgres-pqn.Dockerfile` is the image; copy its `COPY` and `RUN` lines into your own. The release workflow publishes it per PostgreSQL major, so from the next tagged
release `docker run` is enough.

```bash
psql -U postgres -d app -At -c "SELECT name || ' ' || default_version FROM pg_available_extensions WHERE name = 'pqn'"
```

```text
pqn 1.1
```

## 2. Create it

As a superuser. This also creates the roles it needs (`pqn_owner`, `pqn_reader`, `pqn_stats`, `pqn_ledger` and the groups
`pqn_viewer`, `pqn_analyst`, `pqn_admin`, all `NOLOGIN`). `init()` creates the audit ledger:

```bash
psql -U postgres -d app -c "CREATE EXTENSION IF NOT EXISTS pg_stat_statements"
psql -U postgres -d app -c "CREATE EXTENSION pqn"
psql -U postgres -d app -At -c "SELECT pqn_api.init()"
```

```text
ledger at version 2, exposure registry ready
```

### Install without a superuser

If the installer must be an ordinary role, a DBA first runs the role script, then the installer creates the extension and the
DBA takes its extra membership away. The script is `pqn-roles.sql`; read it first. It needs `-v installer=<role>`:

```bash
psql -U postgres -d reports -c "CREATE ROLE pqn_installer LOGIN"
psql -U postgres -d reports -v installer=pqn_installer -f - < infra/pqn-extension/pqn-roles.sql
psql -U pqn_installer -d reports -c "CREATE EXTENSION pqn"
psql -U pqn_installer -d reports -At -c "SELECT pqn_api.init()"
psql -U postgres -d reports -c "REVOKE pqn_owner, pqn_reader, pqn_stats, pqn_ledger FROM pqn_installer"
```

The installer needs the membership again only to upgrade. Run the script in each database you install into.

The DBA can also be a non-superuser. It needs `CREATEROLE`, `CREATE` on the database and `ADMIN OPTION` on `pg_monitor` to run the
script, and to call `expose` and `enroll` it needs `pqn_admin` and `pqn_owner` with inherit. PostgreSQL 16 gives the creator of a
role a membership that carries no rights, so grant them once:

```shell
psql -U dba -d app -c "GRANT pqn_admin TO dba WITH INHERIT TRUE, SET TRUE"
psql -U dba -d app -c "GRANT pqn_owner TO dba WITH INHERIT TRUE, SET TRUE"
```

## 3. Expose the tables analysts may see

Analysts read views, not tables. Print the SQL, review it, run it:

```bash
psql -U postgres -d app -At -c "CREATE SCHEMA IF NOT EXISTS hr"
psql -U postgres -d app -c "CREATE TABLE IF NOT EXISTS hr.people (id int PRIMARY KEY, dept text, ssn text)"
psql -U postgres -d app -At -c "SELECT pqn_api.expose_sql('hr.people', ARRAY['id','dept'])"
psql -U postgres -d app -At -c "SELECT pqn_api.expose('hr.people', ARRAY['id','dept']) IS NOT NULL"
```

This exposes `id` and `dept` and leaves `ssn` out. Pick a scope with a fourth argument:

- `view` (default): only the listed columns can be read, planned or measured.
- `full`: statements over every column can be planned and measured, and `run` still returns only the listed columns. Use it to
  investigate real application statements, which use `SELECT *`. A plan or row count over a hidden column can reveal how common a
  value is, so the setup check warns about each `full` table.

List with `pqn_api.exposed()`, remove with `pqn_api.unexpose('people')`.

## 4. Enroll people

```bash
psql -U postgres -d app -c "CREATE ROLE alice LOGIN PASSWORD 'change-me'"
psql -U postgres -d app -At -c "SELECT pqn_api.enroll_sql('alice', 'analyst')"
psql -U postgres -d app -At -c "SELECT pqn_api.enroll('alice', 'analyst') IS NOT NULL"
```

```text
GRANT pqn_analyst TO alice;
ALTER ROLE alice SET statement_timeout = '15s';
ALTER ROLE alice SET lock_timeout = '2s';
ALTER ROLE alice SET idle_in_transaction_session_timeout = '10s';
ALTER ROLE alice SET temp_file_limit = '1GB';
```

Groups: `viewer` reads their own investigations. `analyst` also plans, runs, ranks statements, investigates and checks rewrites.
`admin` also exposes tables, enrolls people and runs the setup check, and is not a superuser. The third argument sets the timeout
(`'120s'`, `'2min'`). Limits are set on the login, because a limit on a group role does nothing. Only a superuser can set
`temp_file_limit`; for anyone else that line is a comment. These are session defaults, not a ceiling: PostgreSQL lets a person `SET statement_timeout = 0` for
their session or `ALTER ROLE` their own login, so treat them as guard rails against mistakes. `pqn doctor` reports a login that has lost its timeout.

Authentication is PostgreSQL's: `pg_hba.conf`, `scram-sha-256`, certificates, Kerberos. `pqn` stores no secret; keep passwords
in `~/.pgpass`. Ledger rows record the real login. PgBouncer with a pool per user, its default, keeps it (tested in transaction pooling mode); a pooler that connects as one shared login would hide it.

## 5. Install the tool

```shell
make build-pqn
install -m 0755 bin/pqn /usr/local/bin/pqn
export PQN_DSN='postgres://alice@db.internal:5432/app?sslmode=verify-full'
pqn version
```

Without `PQN_DSN` it reads `PGHOST`, `PGUSER`, `PGSERVICE` and `~/.pgpass`. Commands: `doctor`, `top`, `plan`, `run`,
`investigate`, `prove`, `investigations`, `evidence`; `pqn help` lists them.

## 6. Check the setup

A member of the admin group runs the setup check. It reads only catalogs:

```bash
psql -U postgres -d app -c "CREATE ROLE carol LOGIN PASSWORD 'change-me-too'"
psql -U postgres -d app -At -c "SELECT pqn_api.enroll('carol', 'admin') IS NOT NULL"
psql -U carol -d app -At -c "SELECT level || ' ' || check_name FROM pqn_api.verify_setup() WHERE level IN ('BLOCK', 'WARN')"
```

```shell
pqn doctor        # exits 1 when anything is BLOCK
```

`BLOCK` must be fixed (each row names the fix). `WARN` is a choice to make on purpose. Warnings you should expect:

| Warning | Meaning and action |
|---|---|
| `login can become an owner role` | A non-superuser installer still holds the owner roles. Revoke them |
| `table exposed with scope full` | You chose `full`. Accept it, or expose the table with `view` |
| `reader sees pg_stat_statements` | PostgreSQL lets every role read it, so analysts see each other's `pqn` statements. Accept it, or `REVOKE SELECT ON pg_stat_statements, pg_stat_statements_info FROM PUBLIC` (affects every role outside `pg_read_all_stats`) |
| `reader TEMP privilege` | PostgreSQL gives `TEMP` to every role; a read-only transaction already refuses temporary tables. Accept it, or `REVOKE TEMP ON DATABASE app FROM PUBLIC` (affects every role) |
| `pg_stat_statements` | Not installed here, so `pqn top` will not work. See step 2 |

## Use a read replica

Install on the primary only; the extension, roles, views and limits replicate. Point the tool at both:

```shell
export PQN_DSN='postgres://alice@primary.internal/app'
export PQN_REPLICA_DSN='postgres://alice@replica.internal/app'
```

Plans, measurements and `run` go to the replica; the ledger and `pqn top` stay on the primary. Everything that writes fails on a
standby with a read-only error. A standby ranks only its own statements, and a replica can lag.

## Upgrade

Copy the new files, then update. A non-superuser installer needs its owner-role membership back first.

```shell
make install-pqn-extension
psql -U postgres -d app -c "ALTER EXTENSION pqn UPDATE"
psql -U postgres -d app -At -c "SELECT pqn_api.init()"
```

The ledger, views and enrollments are kept. There is no downgrade: take a [backup](#back-up-and-restore) first.

## Back up and restore

`pg_dump` and `pg_restore` carry the extension, the ledger and the exposed views. **Restore the roles first**: the dump changes
the ownership of the extension's schemas before it can create the roles, so without them the restore fails on
`ALTER SCHEMA pqn OWNER TO pqn_owner` and leaves the setup broken. `pg_dumpall --roles-only` brings the `pqn_*` roles, and also
your people and their limits. The new server needs the extension files (step 1):

```shell
pg_dumpall -h old-host -U postgres --roles-only | psql -h new-host -U postgres -d postgres
psql -h new-host -U postgres -c "CREATE DATABASE newdb"
pg_dump -h old-host -U postgres -Fc -d app -f app.dump
pg_restore -h new-host -U postgres -d newdb app.dump
```

## Uninstall

`DROP EXTENSION pqn` removes the functions and **keeps the ledger and the exposed views**, so recorded evidence survives. To
remove everything, as a superuser, in each database:

```shell
psql -U postgres -d app <<'SQL'
DROP EXTENSION pqn;
DROP SCHEMA pqn_api;                 -- left behind, empty
DROP SCHEMA pqn CASCADE;             -- the exposed views and the registry
DROP SCHEMA pqn_ledger CASCADE;      -- the ledger. This deletes the evidence
DROP OWNED BY pqn_owner, pqn_reader, pqn_stats, pqn_ledger, pqn_viewer, pqn_analyst, pqn_admin;
SQL
```

Then, once every database is done, drop the roles. The roles are cluster-wide:

```shell
psql -U postgres -c "DROP ROLE pqn_admin, pqn_analyst, pqn_viewer, pqn_owner, pqn_reader, pqn_stats, pqn_ledger"
psql -U postgres -c "ALTER ROLE alice RESET ALL"     # each enrolled person: clears the limits enroll set
```

If you used a non-superuser installer, add it to both lists.

## Troubleshooting installation and setup

| Message | Fix |
|---|---|
| `extension "pqn" is not available` | The files are not in this server's extension directory. Step 1 |
| `extension "pqn" has no installation script nor update path for version "1.1"` | Copy all three files, including `pqn--1.0--1.1.sql` |
| `pqn: role pqn_owner does not exist. Run pqn-roles.sql first, or create the extension as a superuser.` | Create the extension as a superuser, or [install without a superuser](#install-without-a-superuser) |
| `pqn: <role> must be a member of pqn_owner to hand objects over. Run pqn-roles.sql with -v installer=<role>.` | Run the role script with `-v installer=<role>` |
| `permission denied for database app` at `CREATE EXTENSION` | A non-superuser installer lacks `CREATE` on the database. Run the role script in this database |
| `schema "pqn_api" does not exist` | `CREATE EXTENSION pqn` has not run in this database |
| `permission denied for schema pqn_api` | The person is not enrolled. Step 4 |
| `pqn: run pqn_api.init() first` | Run `init()` before `expose` |
| `pqn: pg_stat_statements is not installed in this database` | `pqn top` needs it. Step 2 |
| `pqn: connect to the primary: ...` | The tool could not log in; the rest is PostgreSQL's message. Check the DSN, `pg_hba.conf` and `~/.pgpass` |

## See also

[Quick start: pqn in your database](pqn-extension.md) · [PostgreSQL extensions](../integrations/postgres-extension.md) ·
[Installation](installation.md) · [Database roles](../security/database-roles.md)
