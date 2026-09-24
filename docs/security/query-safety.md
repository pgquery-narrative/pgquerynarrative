# Query execution safety

What stands between a user-supplied string and the analytical database.

## Parse-tree validation

Every statement is parsed with `pg_query` (PostgreSQL's own parser) and walked as a
tree, not matched with regular expressions or keyword blocklists.

- **Exactly one statement.** A second statement (`; DROP TABLE …`) is rejected before
  either runs.
- **Only `SELECT`/`WITH` reaches the database**, or `EXPLAIN (FORMAT JSON)` of one.
  Any write, DDL, or utility node anywhere in the tree is rejected, including inside
  a CTE.
- **`SELECT ... INTO`** and **`FOR UPDATE`/`FOR SHARE`** locking clauses are rejected
  explicitly: a read-only transaction alone wouldn't stop the first, and the second
  would hold row locks on a pooled connection past the request.
- A length limit (`maxQueryLength`, bytes) is enforced before parsing.

## Schema allowlist

Table references, and schema-qualified functions/operators/types, must resolve to a
schema in `DATABASE_ALLOWED_SCHEMAS` (default `demo`). `app`, `public` (in
production), `pg_catalog`, `information_schema` and `pg_toast*` can never be
allowlisted; the config loader rejects them at startup, not at query time.

## Function policy

A read-only transaction stops writes, but does not make every `SELECT` expression
side-effect free, so the validator applies its own policy on top of it:

| Category | Policy | Why |
|---|---|---|
| Advisory locks (`pg_advisory_lock`, …) | Denied | Session-scoped; would outlive the transaction on a pooled connection |
| Session state (`set_config`) | Denied | Mutates GUCs a later, unrelated request would reuse |
| Sleep (`pg_sleep`, …) | Denied | Holds a connection for its full duration |
| File and large-object access (`pg_read_file`, `lo_import`, `lo_get`, …) | Denied | Reads outside the database |
| Sequence mutation (`nextval`, `setval`) | Denied | A write, denied explicitly rather than relying on the transaction |
| Backend/WAL/replication control, `pg_stat_*_reset` | Denied | Server-wide effects |
| Second-query execution (`dblink`, `query_to_xml`, …) | Denied | Runs SQL that never passed validation |
| `pg_notify` | Denied | Observable outside the transaction |
| Schema-qualified functions, operators or types **outside** the allowlist | Denied | Operators and types are backed by functions in the same schema, so either reaches disallowed code without producing an ordinary function-call node |
| `pg_catalog` / `information_schema` functions not named above | **Allowed** | Read-only catalog access |
| Unqualified names (`count`, `now`, `coalesce`, …) | Allowed | Resolve through the read-only role's pinned `search_path` |

Volatility (`STABLE`/`VOLATILE`/`IMMUTABLE`) is deliberately not the test:
`VOLATILE` covers `random()` and `now()`, which are harmless here, while some
`STABLE` functions still reach outside the session. This check is enforced by the
application in addition to (never instead of) the read-only transaction and the
role's own grants; review `EXECUTE` privileges on the read-only role directly, since
the validator cannot revoke what the role was granted.

## Timeouts and limits

| Control | Env var | Default |
|---|---|---|
| Query timeout | `QUERY_TIMEOUT` | 30s |
| Lock timeout | `QUERY_LOCK_TIMEOUT` | 2s |
| Idle-in-transaction timeout | `QUERY_IDLE_IN_TX_TIMEOUT` | 10s |
| Row limit (soft default / hard ceiling) | request `limit` | 1,000 / 10,000 |
| Result size | `QUERY_MAX_RESULT_BYTES` | 10 MiB |
| Cell size | `QUERY_MAX_CELL_BYTES` | 1 MiB |
| Column count | `QUERY_MAX_COLUMNS` | 100 |

Timeouts are set both on the Go context and as session GUCs on connect, so a
misbehaving statement is bounded even if one layer is misconfigured.

## EXPLAIN ANALYZE controls

Plain `EXPLAIN` never executes the query. `EXPLAIN ANALYZE` does, and is gated by:

- `SECURITY_EXPLAIN_ANALYZE_ENABLED` (default `false`; **forbidden** under production
  StrictMode)
- The connection's `analyze` permission (see [Multiple connections](../workflows/connections.md))
- The validator: only `FORMAT JSON` is accepted as a user-supplied EXPLAIN option, so
  ANALYZE cannot be smuggled in through the SQL text even when the flag is off

Result verification (`verify_results`) is a separate switch with its own permission
(`query`), see [Verify result equivalence](../workflows/verify-results.md).

## See also

[Database roles](database-roles.md) · [Trust model](../trust-model.md) ·
[Configuration reference](../reference/configuration.md#database)
