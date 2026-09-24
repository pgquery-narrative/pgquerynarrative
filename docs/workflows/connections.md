# Multiple connections

Every query, explain, compare, report, schema and stats call can target a specific
analytical data source.

## The default connection

Out of the box there is one connection, `default`, built from `DATABASE_HOST`,
`DATABASE_PORT`, `DATABASE_NAME` and the readonly credentials. Its id is whatever
`DATABASE_DEFAULT_CONNECTION_ID` names (default `default`); if that id isn't among
the configured connections, the app falls back to the first one instead of erroring,
since this is a startup configuration issue, not a per-request one.

Additional connections come from `DATABASE_CONNECTIONS_JSON` (a static list) or, for
organizations, per-organization connection secrets managed via the admin API. See
[Configuration – multiple database connections](../reference/configuration.md#multiple-database-connections).

## Resolving `connection_id`

| Request value | Behavior |
|---|---|
| Omitted, or empty string | The configured default connection |
| A known id | That connection |
| An unknown, non-empty id | **400 `CONNECTION_NOT_FOUND`** |

There is no silent fallback to a different database for a request that names an
unknown connection: it fails closed, so a typo in `connection_id` cannot
accidentally run against the wrong data source.

## Connection permissions

Each connection grants a subset of actions to a given organization: `query`,
`explain`, `analyze`, `schema`, `report`, `schedule`, `stats`, `ask`. If an
organization has **no** rows for a connection at all, every action is allowed by
default, unless `SECURITY_CONNECTION_ALLOWLIST_REQUIRED` is set (its default is on
under production StrictMode), in which case an unassigned connection is denied
outright. If the organization has any assignment for the connection, only the
actions explicitly granted apply; platform/tenant admins bypass the check. A request
missing the required action fails with **400 `CONNECTION_FORBIDDEN`**.

| Action | Needed for |
|---|---|
| `query` | Run query; `verify_results` on a compare |
| `explain` | EXPLAIN, compare (plan-only) |
| `analyze` | EXPLAIN ANALYZE, compare with `analyze: true` |
| `schema` | Schema browsing |
| `report` | Workbench report generation |
| `schedule` | Scheduled report runs |
| `stats` | `pg_stat_statements` reads, regression polling. On a read-only role shared by several organizations only a platform admin may read statistics, and the poller skips it ([why](../security/tenancy.md#analytical-database-separation)) |
| `ask` | Natural-language Ask |

## Readiness per connection

`GET /ready/connections` reports each configured pool's readiness independently:
name, role, whether it's initialized (per-organization pools are created lazily on
first use), and any connection error. It always returns 200; it's a diagnostic view,
not a liveness gate. `GET /ready` itself only checks the app metadata pool and the
schema migration version.

## Cross-database identity

A `queryid` from `pg_stat_statements` is only meaningful **within one connection**:
the same SQL text on two different databases gets different `queryid` values, and
the regression poller, snapshots, baselines and alerts are all scoped by
(organization, connection, `queryid`). Comparing regression history across
connections means comparing by SQL shape, not by id.

## See also

[Configuration reference](../reference/configuration.md#multiple-database-connections) ·
[Regressions and applied fixes](regressions.md) · [Organizations and tenancy](../security/tenancy.md)
