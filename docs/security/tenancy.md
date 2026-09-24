# Organizations and tenancy

!!! warning "Two different things share the phrase 'row-level security'"
    This page is about PgQueryNarrative's **own** multi-organization isolation of
    its metadata. It is unrelated to the [demo-data RLS walkthrough](../examples/rls-demo.md),
    which demonstrates PostgreSQL RLS on the `demo.sales` **analytical** table using a
    role the application never connects as. Do not read one as documentation for the
    other.

## What is isolated, and how

PgQueryNarrative's tenancy unit is the **organization**. Every metadata table that
holds organization-scoped data (investigations, their candidate history and
linked regression alerts), reports, saved queries, schedules, regression snapshots
and alerts, and more) has row-level security enabled and forced. That includes the identity
tables `organization_members` and `oidc_group_org_mappings` and the audit writer's
`audit_log_buffer` (migration `000060`). Login resolves an identity before an organization
is chosen, so those two tables have one narrow, read-only exception each: a user's own
memberships in every organization, and the mappings for the groups in the token. Each is a `SELECT`
policy only: an `INSERT`, `UPDATE` or `DELETE` can reach a row only inside the current organization. Only tables that hold no organization-scoped data have none:
`organizations` (the list itself), `api_key_usage`, `oidc_pkce_states` and
`rate_limit_buckets`:

```sql
USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''))
```

`current_setting(..., true)` returns `NULL` when the GUC is unset, so an
unauthenticated or misconfigured request context sees **no rows**, not every
organization's rows. The application sets `app.current_org_id` with
`set_config(..., is_local := true)` at the start of every transaction that touches
`app.*`, scoped to that transaction only.

The **app role is `NOBYPASSRLS`**, so these policies apply even to the server's own
connections; isolation does not depend on the application "remembering" to filter
by organization, though services also filter explicitly by `organization_id` as a
second layer.

## Background jobs

Scheduled work (the schedule runner, webhook retry, EXPLAIN snapshot retention) runs
across every organization's data, so it uses a separate `app.scheduler_bypass`
setting that specific policies are written to honour, rather than running as a
different, RLS-exempt role.

## Analytical database separation

Isolation on the **analytical** side is a different mechanism entirely: there is no
RLS requirement for your own database:

- **Connection assignment**: an organization can only use connections explicitly
  assigned to it (or, if `SECURITY_CONNECTION_ALLOWLIST_REQUIRED` is off and the
  organization has no assignments at all, every configured connection).
- **Per-connection actions**: `query`, `explain`, `analyze`, `schema`, `report`,
  `schedule`, `stats`, `ask`, granted per (organization, connection). See
  [Multiple connections](../workflows/connections.md#connection-permissions).
- **Per-organization secrets**: `/admin/connection-secrets` lets an organization
  supply its own connection DSN (encrypted at rest, see
  [Data handling](data-handling.md)), so two organizations can point the same
  connection id at genuinely different databases.

- **Statement statistics**: `pg_stat_statements` is kept per database role, not per
  organization. On a connection several organizations share (one read-only role), the SQL
  text of one organization's analysts would appear in every other organization's
  `GET /queries/stats`. When more than one organization exists, only a platform
  administrator may read it on a shared role (`STAT_STATEMENTS_SHARED`); an organization
  with its own credentials sees only its own role's statements.
  The regression poller skips such a connection for the same reason (it would copy the
  other organizations' SQL text into this one's tables), and the two workload totals on the
  workspace overview read as zero for non-platform users there.

An organization with no assignment for a connection, under
`SECURITY_CONNECTION_ALLOWLIST_REQUIRED` (on by default in production), gets **400
`CONNECTION_FORBIDDEN`** rather than silent access.

## What this is not

PgQueryNarrative is explicitly **not** a public multi-tenant SaaS product, see
[Trust model](../trust-model.md). Organization isolation exists to let one
internal deployment serve multiple teams without their investigations, reports and
saved queries leaking into each other, not to safely host untrusted third parties.

## See also

[Authentication and roles](authentication.md) · [Database roles](database-roles.md) ·
[Multiple connections](../workflows/connections.md) · [Demo-data RLS](../examples/rls-demo.md)
