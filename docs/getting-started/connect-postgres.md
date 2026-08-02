# Connect your PostgreSQL

Point PgQueryNarrative at **your** database (usually a replica) with a dedicated read-only role. For the bundled demo dataset, use [Quick start](quickstart.md) instead.

Read [Trust model](../trust-model.md) before opening production-adjacent data.

## 1. Create a read-only role

On the target PostgreSQL (example — adjust names and schemas):

```sql
CREATE ROLE pqn_readonly LOGIN PASSWORD 'choose-a-strong-secret';

-- Example: analytics reporting schema only
GRANT CONNECT ON DATABASE your_db TO pqn_readonly;
GRANT USAGE ON SCHEMA reporting TO pqn_readonly;
GRANT SELECT ON ALL TABLES IN SCHEMA reporting TO pqn_readonly;
ALTER DEFAULT PRIVILEGES IN SCHEMA reporting
  GRANT SELECT ON TABLES TO pqn_readonly;
```

Prefer a **replica**. Do not use a superuser or a role that can write application data.

## 2. Configure the app

Minimum environment (see [Configuration](../configuration.md)):

```bash
# App DB (saved queries, reports, orgs) — can be the Compose Postgres
DATABASE_HOST=...
DATABASE_USER=pgquerynarrative_app
DATABASE_PASSWORD=...

# Analytical queries — your replica + readonly role
DATABASE_READONLY_USER=pqn_readonly
DATABASE_READONLY_PASSWORD=...
DATABASE_ALLOWED_SCHEMAS=reporting
QUERY_TIMEOUT=30s
```

For a **second** analytical source (in addition to `default`), use `DATABASE_CONNECTIONS_JSON` and pick connections in the UI / API `connection_id`.

## 3. Allowlist only what investigators need

`DATABASE_ALLOWED_SCHEMAS` is a hard allowlist enforced in the query validator. Start narrow (one reporting schema or a set of views). Never put `app` (or your product’s private schema) on that list.

## 4. Timeouts and EXPLAIN ANALYZE

| Setting | Guidance |
|---------|----------|
| `QUERY_TIMEOUT` | Keep tight on shared replicas (e.g. 15–30s); raise only for approved ANALYZE demos |
| `SECURITY_EXPLAIN_ANALYZE_ENABLED` | Off unless you accept that compare may **execute** candidate SQL |
| Result size limits | Keep defaults until you know report workloads |

## 5. Verify

```bash
curl -s http://localhost:8080/ready
curl -s http://localhost:8080/api/v1/connections
curl -s -X POST http://localhost:8080/api/v1/queries/run \
  -H "Content-Type: application/json" \
  -d '{"sql":"SELECT 1","limit":1}'
```

Then open **Investigate**, paste a real expensive query from that schema, and run compare only after you understand ANALYZE policy.

## 6. Production checklist

When leaving laptop demo mode: see [Deployment](../reference/deployment.md) and [Trust model](../trust-model.md).

## See also

- [Trust model](../trust-model.md)
- [Configuration](../configuration.md)
- [Installation](installation.md)
- [API examples](../api/examples.md)
