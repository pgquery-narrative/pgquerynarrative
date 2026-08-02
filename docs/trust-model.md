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
| Validate statements | Single-statement checks; write / DDL patterns blocked |
| Explain and compare | `EXPLAIN` / optional `EXPLAIN ANALYZE` when enabled server-side |
| Audit sensitive LLM use | Policy flags + audit events when cloud LLM data egress is allowed |

## What it will not do

- **Execute writes or DDL** through the query runner (insert/update/delete/create/drop, etc.)
- **Grant itself** broader database rights than the roles you configure
- **Bypass** the schema allowlist for ad-hoc exploration of system catalogs you did not allow
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
