# Trust model

A plain-language statement of scope for security reviewers and DBAs. Each guarantee
links to the page that documents how it is enforced. The same vocabulary is used in
[SECURITY.md](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/.github/SECURITY.md)
and the [Architecture](architecture.md#security-boundaries) page.

## What PgQueryNarrative is for

- **Internal** engineering and analytics teams investigating expensive PostgreSQL queries
- Running against a **dedicated read-only role**, ideally on a **replica** or reporting database
- Producing **plan evidence** and **engineering reports** that a person acts on

Public multi-tenant SaaS and "paste production credentials into a chatbot" are non-goals.

## What it guarantees

| Guarantee | Enforced by |
|---|---|
| User SQL is a single read-only statement | Parse-tree validation — only `SELECT`/`WITH`, or `EXPLAIN (FORMAT JSON)` of one; DML, DDL, utility statements, `SELECT … INTO` and row locks are rejected. [Query execution safety](security/query-safety.md) |
| User SQL cannot write, even if validation failed | A read-only transaction **and** a role with no write or DDL privileges, verified in CI by `tools/db/verify_security.sh`. [Database roles](security/database-roles.md) |
| User SQL stays inside the schemas you allow | `DATABASE_ALLOWED_SCHEMAS` (default `demo`) for table references and schema-qualified functions, operators and types. `app`, `pg_catalog`, `information_schema` and `pg_toast` can never be allowlisted |
| Side-effecting functions are refused | A named deny-list (advisory locks, `set_config`, `pg_sleep`, file and large-object access, sequence mutation, backend/WAL control, `dblink`, `query_to_xml`, `pg_notify`, stats reset). Unqualified and `pg_catalog`/`information_schema` functions not on the list are allowed |
| Queries are bounded | `QUERY_TIMEOUT`, lock and idle timeouts, row limit, result/cell/column caps |
| Proposals are never applied | PgQueryNarrative never automatically applies a proposed rewrite, index or DDL to the analytical target database. Index DDL is suggest-only; HypoPG projections are hypothetical and reset in the same transaction |
| `EXPLAIN` does not execute; `ANALYZE` does and is opt-in | `SECURITY_EXPLAIN_ANALYZE_ENABLED` (off by default, forbidden in production) plus a per-connection `analyze` permission |
| Result checks are labelled for what they are | Five equivalence states; sampled evidence is never reported as verified. [Verify result equivalence](workflows/verify-results.md) |
| Organizations cannot see each other's metadata | Row-level security on `app.*` keyed on the organization, plus per-organization connection assignments. [Organizations and tenancy](security/tenancy.md) |
| Row data does not leave for a cloud LLM unless you say so | Cloud providers require `LLM_ALLOW_EXTERNAL_DATA=true`; rows are omitted unless `LLM_SEND_ROW_DATA=true`. [Data handling](security/data-handling.md) |

## What it will not do

- Execute writes or DDL through the query path
- Grant itself broader rights than the roles you configure
- Let user SQL read tables outside the allowlist, including the `app` metadata schema
- Take session-scoped locks or change session settings through user SQL
- Apply any proposed change to your database, or mark a fix `confirmed` without measured statistics
- Replace a DBA's judgement — findings, rewrites and rankings are recommendations for review

It does write its **own** metadata (investigations, reports, snapshots, audit records,
alert state), and migrations change the schema they own. Those writes go to the
metadata database through the app role, never through the read-only role.

## Three database identities

| Identity | Purpose |
|---|---|
| Migration role (`DATABASE_MIGRATION_*`) | Runs migrations once at container start; creates extensions. Unset before the server starts |
| App role (`DATABASE_USER`) | Application metadata in the `app` schema |
| Read-only role (`DATABASE_READONLY_USER`, or per connection) | Every user-supplied statement |

If the read-only role is a superuser or can write, you have defeated the model.
[Database roles](security/database-roles.md) lists the grants and the verification script.

## Demo versus your database

| Mode | Schemas | Intent |
|---|---|---|
| Guided demo | `demo` (optionally `opendata`) | Reproducible partition-pruning story |
| Your PostgreSQL | Schemas you allowlist, with grants you give the read-only role | Real investigations — see [Connect your PostgreSQL](getting-started/connect-postgres.md) |

`APP_ENV=demo` fabricates workspace KPIs and seeds demo regression alerts. Never enable it
where the numbers will be acted on.

## Seeing the configured state

The workbench **Security & Trust** page (`GET /api/v1/trust?connection_id=…`) reports the
hardening actually in effect for a connection — read-only role, allowlist, ANALYZE policy,
StrictMode expectations — so operators can confirm what is enabled rather than assume it.

## See also

[Architecture](architecture.md) · [Production configuration](operate/production.md) ·
[Configuration reference](reference/configuration.md)
