# Connect your PostgreSQL

Point PgQueryNarrative at **your** database, usually a replica or a reporting
database, through a dedicated read-only role. For the bundled demo dataset, use
[Quick start](quickstart.md) instead.

Read [Trust model](../trust-model.md) and [Database roles](../security/database-roles.md)
before opening production-adjacent data.

## 1. Create a read-only role

On the target PostgreSQL (adjust names and schemas):

```sql
CREATE ROLE pqn_readonly LOGIN PASSWORD 'choose-a-strong-secret';

-- Example: one reporting schema only
GRANT CONNECT ON DATABASE your_db TO pqn_readonly;
GRANT USAGE ON SCHEMA reporting TO pqn_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA reporting TO pqn_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA reporting
  GRANT SELECT ON TABLES TO pqn_readonly;
```

Prefer a **replica**. Never grant this role write access, and never grant it
anything on the schema that holds PgQueryNarrative's own metadata.

## 2. Configure the app

Minimum environment for the **default** connection (full list:
[Configuration reference](../reference/configuration.md)):

```bash
# App metadata DB (investigations, saved queries, reports, orgs); can be the Compose Postgres
DATABASE_HOST=...
DATABASE_USER=pgquerynarrative_app
DATABASE_PASSWORD=...

# Analytical queries: your replica + readonly role
DATABASE_READONLY_USER=pqn_readonly
DATABASE_READONLY_PASSWORD=...
DATABASE_ALLOWED_SCHEMAS=reporting
QUERY_TIMEOUT=30s
```

For **additional** analytical sources beside `default`, set `DATABASE_CONNECTIONS_JSON`
(a JSON array of connection objects, camelCase keys, durations as integer
nanoseconds, see [Configuration – multiple connections](../reference/configuration.md#multiple-database-connections))
and pass `connection_id` in the API/UI/MCP. See
[Multiple connections](../workflows/connections.md).

## 3. Allowlist only what investigators need

`DATABASE_ALLOWED_SCHEMAS` is a hard allowlist enforced in the query validator.
Start narrow: one reporting schema or a curated set of views. `app`, `public` (in
production), `pg_catalog`, `information_schema` and `pg_toast*` can never be
allowlisted; the config loader rejects them outright.

## 4. Timeouts and EXPLAIN ANALYZE

| Setting | Guidance |
|---|---|
| `QUERY_TIMEOUT` | Keep tight on shared replicas (15–30s); raise only for approved ANALYZE work |
| `SECURITY_EXPLAIN_ANALYZE_ENABLED` | Off unless you accept that compare may **execute** candidate SQL. Forbidden in production StrictMode |
| Result size limits | Keep the defaults until you know the report workload |

## 5. Verify

```bash
curl -s http://localhost:8080/ready
curl -s http://localhost:8080/api/v1/connections
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT 1","limit":1}'
```

Then open **Investigate**, paste a real expensive query from that schema, and run
compare only once you understand the ANALYZE policy above.

## 6. Production checklist

Leaving laptop demo mode: [Production configuration](../operate/production.md) and
[Deployment](../operate/deployment.md).

## See also

- [Trust model](../trust-model.md) · [Database roles](../security/database-roles.md)
- [Configuration](../reference/configuration.md)
- [Installation](installation.md)
- [REST API](../integrations/rest-api.md)
- [Install the pqn extension](pqn-installation.md): the alternative that runs inside your database, with no PgQueryNarrative server
