# Trust model

Plain-language scope for security reviewers and operators.

## What PgQueryNarrative is for

- **Internal** analytics / engineering teams investigating expensive PostgreSQL queries
- Running against a **dedicated read-only role**, ideally on a **replica**
- Surfacing **plan evidence** and producing **engineering reports**

Public multi-tenant SaaS and “paste production credentials into a chatbot” are **non-goals**.

## What it will do

| Behavior | Mechanism |
|----------|-----------|
| Run user SQL read-only | Separate `DATABASE_READONLY_*` credentials; app pool is for app tables only |
| Bound result size and time | `QUERY_TIMEOUT`, result/cell/column limits |
| Restrict schemas | `DATABASE_ALLOWED_SCHEMAS` (default `demo`; never include `app`) |
| Validate statements | Single-statement checks; write / DDL patterns blocked; `SELECT ... INTO` and `FOR UPDATE`/`FOR SHARE` rejected |
| Restrict SQL functions | Side-effecting and admin functions denied by name; schema-qualified calls held to the same allowlist as tables (see below) |
| Explain and compare | `EXPLAIN` / optional `EXPLAIN ANALYZE` when enabled server-side |
| Audit sensitive LLM use | Policy flags + audit events when cloud LLM data egress is allowed |

### Function safety

A read-only transaction stops writes. It does **not** make every `SELECT`
expression side-effect free, so the validator applies its own function policy on
top of it:

| Category | Policy | Why |
|----------|--------|-----|
| Advisory locks (`pg_advisory_lock`, `pg_try_advisory_xact_lock`, …) | **Denied** | Session-scoped: they outlive the transaction and poison the pooled connection |
| Session state (`set_config`) | **Denied** | Mutates GUCs on a connection other requests will reuse |
| Sleep (`pg_sleep`, …) | **Denied** | Holds a connection for its full duration |
| Server file and large-object access (`pg_read_file`, `pg_ls_dir`, `lo_import`, …) | **Denied** | Reads outside the database |
| Sequence mutation (`nextval`, `setval`) | **Denied** | Writes; denied explicitly rather than relying on the transaction |
| Backend / WAL / replication control, `pg_stat_*_reset` | **Denied** | Server-wide effects |
| Second-query execution (`query_to_xml`, `dblink`, …) | **Denied** | Runs SQL that never passes the validator |
| Notification (`pg_notify`) | **Denied** | Observable outside the transaction |
| Anything schema-qualified outside `DATABASE_ALLOWED_SCHEMAS` — functions, explicit operators (`OPERATOR(s.+)`) and type names (`col::s.t`) | **Denied** | Operators and types are backed by functions in the same schema, so either reaches code the allowlist excludes without producing a function-call node |
| `pg_catalog` / `information_schema` functions not named above | Allowed | Read-only catalog access |
| Unqualified names (`count`, `now`, `coalesce`, …) | Allowed | Resolve through the analytical role's pinned `search_path`, itself limited to the allowed schemas |

Volatility is deliberately *not* the test: `VOLATILE` covers `random()` and
`now()`, which are harmless here, while some `STABLE` functions still reach
outside the session. The policy names behaviors instead.

This is enforced in the application, in addition to — never instead of — the
read-only transaction and the role's own grants. Review `EXECUTE` privileges on
the analytical role as well; the validator cannot revoke what the role was
granted directly.

## What it will not do

- **Execute writes or DDL** through the query runner (insert/update/delete/create/drop, etc.)
- **Grant itself** broader database rights than the roles you configure
- **Bypass** the schema allowlist for ad-hoc exploration of system catalogs you did not allow
- **Take session-scoped locks or mutate session settings** through user SQL (see Function safety)
- **Send row data to a cloud LLM** unless you explicitly enable that path (`LLM_ALLOW_EXTERNAL_DATA` and related flags — default is fail-closed for cloud egress)
- **Replace** your DBA review — findings are triage signals; you own production changes

## Two database roles (intentional)

| Role | Purpose |
|------|---------|
| App role (`DATABASE_USER`) | Migrations, saved queries, reports, org metadata |
| Read-only role (`DATABASE_READONLY_USER`) | All user-facing SQL and EXPLAIN |

If both point at the same superuser-like account, you have defeated the model. Use a true read-only grant set in production.

## Demo vs your database

| Mode | Schemas | Intent |
|------|---------|--------|
| Guided demo | `demo` (and optional `opendata`) | Reproducible partition-pruning story |
| Your Postgres | Schemas you allowlist + grants you give the readonly role | Real investigations |

Connecting your own database: [Connect your PostgreSQL](getting-started/connect-postgres.md).

## UI visibility

The workbench **Security & Trust** page (`GET /api/v1/trust`) reflects configured hardening so operators can see what is enabled locally vs production StrictMode expectations.

## See also

- [Connect your PostgreSQL](getting-started/connect-postgres.md)
- [Configuration](configuration.md) — security and database variables
- [Deployment](reference/deployment.md) — Docker / Compose / Kubernetes
- [Concepts](concepts.md)
