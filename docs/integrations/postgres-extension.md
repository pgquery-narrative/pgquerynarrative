# PostgreSQL extension

Call the running PgQueryNarrative service **from SQL**. This is an integration
layer — a set of `plpgsql` wrapper functions that call the REST API over HTTP — not
the query engine running inside PostgreSQL. Files: `infra/postgres-extension/`
(`pgquerynarrative.control`, `pgquerynarrative--1.0.sql`).

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

## Configuration

The API URL is a **session-scoped** setting (`set_config(..., is_local := false)`),
not a persisted table row — it resets on reconnect and defaults to
`http://localhost:8080`:

```sql
SELECT pgquerynarrative_set_api_url('http://localhost:8080');
SELECT pgquerynarrative_get_api_url();
```

From inside a Postgres container reaching the app on the host, use
`http://host.docker.internal:8080`. See [Configuration – server](../reference/configuration.md#server).

## Functions

| Function | Calls | Notes |
|---|---|---|
| `pgquerynarrative_set_api_url(url TEXT) RETURNS void` | — | Sets the session GUC |
| `pgquerynarrative_get_api_url() RETURNS TEXT` | — | Reads it, or the default |
| `pgquerynarrative_run_query(query_sql TEXT, row_limit INTEGER DEFAULT 100) RETURNS JSON` | `POST /api/v1/queries/run` | Read-only query. There is no `connection_id` parameter — always the default connection |
| `pgquerynarrative_generate_report(query_sql TEXT) RETURNS JSON` | `POST /api/v1/reports/generate` | Workbench narrative report. Uses the LLM when configured, with a deterministic metrics fallback if it fails — the LLM is not strictly required |
| `pgquerynarrative_list_saved(query_limit INTEGER DEFAULT 50, query_offset INTEGER DEFAULT 0) RETURNS JSON` | `GET /api/v1/queries/saved` | — |

`EXECUTE` on all five functions is granted to `PUBLIC` — any role that can connect
to the database can call them, including `pgquerynarrative_set_api_url`, which
redirects where every subsequent call in that session goes. Restrict this with
`REVOKE EXECUTE ... FROM PUBLIC` plus explicit grants if that is not acceptable in
your environment.

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

- **No API key is sent.** The extension's HTTP calls carry only a `Content-Type`
  header — no `Authorization`. If the target server has `SECURITY_AUTH_ENABLED=true`,
  every call fails with 401 (surfaced as the exception text above). The extension is
  only usable today against a server running with auth disabled, or reachable
  without auth from wherever Postgres runs.
- **`EXECUTE ... TO PUBLIC`** means any authenticated database role can call the
  running service, including generating reports, from inside SQL.
- The extension can only reach whatever `pgquerynarrative_set_api_url` points it at
  — any session on the database can repoint it, so treat it as no more trustworthy
  than the network path from Postgres to that URL.

## See also

[REST API](rest-api.md) — the endpoints the extension calls ·
[Configuration](../reference/configuration.md) ·
[Troubleshooting](../operate/troubleshooting.md)
