# PostgreSQL extensions

There are two extensions, and they do different things.

| Extension | What it is | Read |
|---|---|---|
| `pgquerynarrative` | SQL wrapper functions that call the **running PgQueryNarrative service** over HTTP. An integration layer, not the query engine. Files: `infra/postgres-extension/` | The rest of this page, from [Install](#install) to [Security implications](#security-implications) |
| `pqn` | Runs **inside your database** and needs no server. A terminal tool finds slow statements, proposes rewrites from their plans, and checks them. Files: `infra/pqn-extension/` | [The `pqn` extension](#the-pqn-extension), then the [Quick start](../getting-started/pqn-extension.md) and [Install the pqn extension](../getting-started/pqn-installation.md) |

Both are verified by scripts that start a throwaway PostgreSQL: `make verify-extension` and `make verify-pqn-extension`.

## Install

**Postgres already running:**

1. Copy the extension files into the Postgres extension directory:
   - **Local:** `make install-extension` (uses `pg_config --sharedir`)
   - **Docker:** `make install-extension-docker`
2. In `psql`, in your database:

   ```sql
   -- Optional but required for real API calls — without it the functions
   -- return a "pending" stub instead of calling the service. See below.
   CREATE EXTENSION http;

   CREATE EXTENSION pgquerynarrative;
   ```

**Full Docker setup** (start Postgres, init, migrate, install the extension files,
`CREATE EXTENSION`, seed):

```bash
make setup-extension-docker
```

This target does **not** create the `http` extension for you, so a fresh install via
this path gets the stub functions until you run `CREATE EXTENSION http;` yourself
and re-run `CREATE EXTENSION pgquerynarrative;`.

**Upgrading from 1.0:** copy the new files (`make install-extension` or
`make install-extension-docker` copies every version and upgrade script), then
`ALTER EXTENSION pgquerynarrative UPDATE;`. Version 1.1 removes `EXECUTE` from
`PUBLIC`, so **every role that used the functions loses access until you grant it**
(next section).

## Granting access

Nothing is executable by `PUBLIC`. As the role that ran `CREATE EXTENSION` (a
superuser), grant the functions to each role that needs them:

```sql
SELECT pgquerynarrative_grant_access('alice');   -- alice, or any group role
SELECT pgquerynarrative_revoke_access('alice');
```

`pgquerynarrative_grant_access` covers the five functions callable by users. It never
grants `pgquerynarrative_set_api_url`, which stays with the extension owner.

## Configuration

The API URL is a **stored setting**, changed only by the extension owner. It
defaults to `http://localhost:8080` and survives reconnects and dumps:

```sql
SELECT pgquerynarrative_set_api_url('http://localhost:8080');   -- owner only
SELECT pgquerynarrative_get_api_url();
```

The URL must start with `http://` or `https://`. A trailing slash is removed.

From inside a Postgres container reaching the app on the host, use
`http://host.docker.internal:8080`. See [Configuration – server](../reference/configuration.md#server).

Each caller supplies **their own API key** for the session. It is sent as
`Authorization: Bearer <key>` to the configured URL and to nowhere else:

```sql
SELECT pgquerynarrative_set_api_key('pqn_...');
```

The key lives in that session only and is never stored. Statement logging
(`log_statement = 'all'`) records the literal, so avoid it on databases where people
type keys, or pass the key from a client that does not log it.

## Functions

| Function | Calls | Notes |
|---|---|---|
| `pgquerynarrative_set_api_url(url TEXT) RETURNS void` | — | Stores the URL. Owner only |
| `pgquerynarrative_get_api_url() RETURNS TEXT` | — | Reads it, or the default |
| `pgquerynarrative_set_api_key(api_key TEXT) RETURNS void` | — | Sets this session's API key |
| `pgquerynarrative_grant_access(to_role NAME)` / `pgquerynarrative_revoke_access(from_role NAME)` | — | Grant or withdraw the user-callable functions. Owner only |
| `pgquerynarrative_run_query(query_sql TEXT, row_limit INTEGER DEFAULT 100) RETURNS JSON` | `POST /api/v1/queries/run` | Read-only query. There is no `connection_id` parameter — always the default connection |
| `pgquerynarrative_generate_report(query_sql TEXT) RETURNS JSON` | `POST /api/v1/reports/generate` | Workbench narrative report. Uses the LLM when configured, with a deterministic metrics fallback if it fails — the LLM is not strictly required |
| `pgquerynarrative_list_saved(query_limit INTEGER DEFAULT 50, query_offset INTEGER DEFAULT 0) RETURNS JSON` | `GET /api/v1/queries/saved` | — |

`EXECUTE` on every function is withheld from `PUBLIC` and granted per role with `pgquerynarrative_grant_access`.

## Dependency on the `http` extension

Whether `http` is installed is checked **once, at `CREATE EXTENSION pgquerynarrative`
time**:

- **`http` present:** the three data functions issue real HTTP requests. A non-200
  response raises a PostgreSQL exception: `PgQueryNarrative API error: <status> - <body>`.
- **`http` absent:** the three data functions are stub versions that return
  `{"status": "pending", "message": "Install extension http for API calls: CREATE EXTENSION http;", ...}`
  without making any network call. Installing `http` **after** `pgquerynarrative`
  does not upgrade them automatically — re-run
  `CREATE EXTENSION IF NOT EXISTS pgquerynarrative;`... no, re-run
  `DROP EXTENSION pgquerynarrative; CREATE EXTENSION pgquerynarrative;` (or apply
  the SQL file again) after installing `http`, so the DO block re-evaluates.

## Security implications

- **Access is opt-in.** No role can call the functions until the owner grants them
  (version 1.0 granted `EXECUTE` to `PUBLIC`).
- **Roles cannot redirect the server.** In 1.0 any role could point its session at any
  URL, so any role could make the database server send HTTP requests to an address it
  chose. In 1.1 the URL is stored, only the owner can change it, and `SET
  pgquerynarrative.api_url` has no effect.
- **API keys are per user and per session.** With `SECURITY_AUTH_ENABLED=true` the
  server checks the key, so the extension is no longer limited to servers with auth off.
  Without a key the calls carry no `Authorization` header and fail with 401.
- **The functions call the REST API with the caller's rights on that API, not on the
  database.** The server decides what the key may do. A role that holds a valid key can
  reach everything that key allows, from any session it can open.
- **Functions run as the caller** (`SECURITY INVOKER`), except `pgquerynarrative_get_api_url`,
  which reads the stored setting for callers who have no privilege on the config table.
- The `http` extension is still a network client inside the database process. Treat the
  network path from Postgres to the configured URL as trusted, and use `https://`.

## The `pqn` extension

`pqn` is the other extension. It runs **inside your database** and needs no PgQueryNarrative server: a terminal tool,
`pqn`, finds the statements that cost the most, reads their plans, proposes a rewrite, and checks that the rewrite returns
the same rows and runs faster. It writes only to its own audit ledger, reads your data only through views a DBA chose,
and lets PostgreSQL do the authentication.

| To… | Read |
|---|---|
| Try it on a throwaway database in about ten minutes | [Quick start: pqn in your database](../getting-started/pqn-extension.md) |
| Install and set it up on your own server | [Install the pqn extension](../getting-started/pqn-installation.md) |

## See also

[REST API](rest-api.md) — the endpoints the extension calls ·
[Configuration](../reference/configuration.md) ·
[Troubleshooting](../operate/troubleshooting.md)
