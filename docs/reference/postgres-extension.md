# PostgreSQL extension

Call PgQueryNarrative from SQL via a PostgreSQL extension. Requires the PgQueryNarrative service running (see [Quick start](../getting-started/quickstart.md)) and PostgreSQL 16+. Extension files: `infra/postgres-extension/`.

## Install

**Postgres already running:**

1. Copy extension files into the Postgres extension directory:
   - **Local:** `make install-extension` (uses `pg_config --sharedir`)
   - **Docker:** `make install-extension-docker`
2. In psql, in your database:
   ```sql
   CREATE EXTENSION pgquerynarrative;
   ```
   Optional (for real API calls from SQL): `CREATE EXTENSION http;` then create pgquerynarrative; the extension uses the `http` extension when present.

**Docker full setup** (start Postgres, init, migrate, install extension, create it, seed):

```bash
make setup-extension-docker
```

## Configuration

```sql
SELECT pgquerynarrative_set_api_url('http://localhost:8080');
SELECT pgquerynarrative_get_api_url();
```

From inside Docker to reach app on host: `http://host.docker.internal:8080`. See [Configuration](../configuration.md) for server port.

## Functions

| Function | Description | API equivalent |
|----------|-------------|----------------|
| `pgquerynarrative_run_query(query_sql, row_limit)` | Run read-only query; returns JSON. Default limit 100. | [POST /api/v1/queries/run](../api/README.md#queries) |
| `pgquerynarrative_generate_report(query_sql)` | Generate narrative report (JSON). Requires [LLM](../getting-started/llm-setup.md). | POST /api/v1/reports/generate |
| `pgquerynarrative_list_saved(limit, offset)` | List saved queries (JSON). | GET /api/v1/queries/saved |

Without the `http` extension, these return a JSON "pending" message; install `http` for real API calls.

## Example

```sql
SELECT pgquerynarrative_run_query('SELECT product_category, SUM(total_amount) FROM demo.sales GROUP BY product_category', 10);
```

## See also

- [API reference](../api/README.md) — REST endpoints used by the extension
- [Configuration](../configuration.md) — Server and database
- [Troubleshooting](troubleshooting.md) — Common issues
- [Documentation index](../index.md)
