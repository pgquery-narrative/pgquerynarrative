# Organizations and tenancy

!!! warning "Two different things share the phrase 'row-level security'"
    This page is about PgQueryNarrative's **own** multi-organization isolation of
    its metadata. It is unrelated to the [demo-data RLS walkthrough](../examples/rls-demo.md),
    which demonstrates PostgreSQL RLS on the `demo.sales` **analytical** table using a
    role the application never connects as. Do not read one as documentation for the
    other.

## What is isolated, and how

PgQueryNarrative's tenancy unit is the **organization**. Every metadata table that
holds organization-scoped data — investigations (and their candidate history and
linked regression alerts), reports, saved queries, schedules, regression snapshots
and alerts, and more — has row-level security enabled and forced:

```sql
USING (organization_id::text = NULLIF(current_setting('app.current_org_id', true), ''))
```

`current_setting(..., true)` returns `NULL` when the GUC is unset, so an
unauthenticated or misconfigured request context sees **no rows**, not every
organization's rows. The application sets `app.current_org_id` with
`set_config(..., is_local := true)` at the start of every transaction that touches
`app.*`, scoped to that transaction only.

The **app role is `NOBYPASSRLS`**, so these policies apply even to the server's own
connections — isolation does not depend on the application "remembering" to filter
by organization, though services also filter explicitly by `organization_id` as a
second layer.

## Background jobs

Scheduled work (the schedule runner, webhook retry, EXPLAIN snapshot retention) runs
across every organization's data, so it uses a separate `app.scheduler_bypass`
setting that specific policies are written to honour, rather than running as a
different, RLS-exempt role.

## Analytical database separation

Isolation on the **analytical** side is a different mechanism entirely — there is no
RLS requirement for your own database:

- **Connection assignment**: an organization can only use connections explicitly
  assigned to it (or, if `SECURITY_CONNECTION_ALLOWLIST_REQUIRED` is off and the
  organization has no assignments at all, every configured connection).
- **Per-connection actions**: `query`, `explain`, `analyze`, `schema`, `report`,
  `schedule`, `stats`, `ask` — granted per (organization, connection). See
  [Multiple connections](../workflows/connections.md#connection-permissions).
- **Per-organization secrets**: `/admin/connection-secrets` lets an organization
  supply its own connection DSN (encrypted at rest — see
  [Data handling](data-handling.md)), so two organizations can point the same
  connection id at genuinely different databases.

An organization with no assignment for a connection, under
`SECURITY_CONNECTION_ALLOWLIST_REQUIRED` (on by default in production), gets **400
`CONNECTION_FORBIDDEN`** rather than silent access.

## What this is not

PgQueryNarrative is explicitly **not** a public multi-tenant SaaS product — see
[Trust model](../trust-model.md). Organization isolation exists to let one
internal deployment serve multiple teams without their investigations, reports and
saved queries leaking into each other, not to safely host untrusted third parties.

## See also

[Authentication and roles](authentication.md) · [Database roles](database-roles.md) ·
[Multiple connections](../workflows/connections.md) · [Demo-data RLS](../examples/rls-demo.md)
