# Data handling

What happens to SQL text and query results once they're inside PgQueryNarrative.

## Encryption at rest

AES-GCM with a key derived from `SECURITY_DATA_ENCRYPTION_KEY` (SHA-256 of the
secret), stored as a versioned envelope. Sealed today:

- Per-organization connection DSNs (passwords included)
- Saved query SQL, workbench report SQL, investigation report SQL, and schedule SQL
- EXPLAIN snapshot SQL and stored plans

Without a key configured, this SQL is stored **in plaintext** — sealing only
happens when `SECURITY_DATA_ENCRYPTION_KEY` (or, as a fallback,
`SECURITY_SESSION_SECRET`) is set, and one of the two is **required** under
production StrictMode. There is a single active key: no rotation and no
multi-key support, so rotating the key means re-sealing existing rows yourself.
Writes fail closed — if sealing errors, the write is rejected rather than falling
back to plaintext.

## Redaction

EXPLAIN snapshots are always redacted with a constant placeholder before storage,
independent of the encryption key. Saved-query and report SQL is sealed (when a key
is configured) but is **not** separately redacted — a person who can read the
decrypted row sees the original SQL, literals included.

Before an LLM prompt is built, `LLM_REDACT_PII=true` (default) strips common PII
patterns and SQL string literals from the SQL text and metrics that go into the
prompt — see [LLM providers](../integrations/llm.md#data-egress-and-privacy).

## Retention

| What | Control | Default |
|---|---|---|
| EXPLAIN snapshots | `SECURITY_EXPLAIN_SNAPSHOT_RETENTION_DAYS` | 90 days (0 = keep forever). Cleaned up every 6 hours |
| Regression statement snapshots (`app.stat_statement_polls` and the linked `app.stat_statement_snapshots`, which holds `pg_stat_statements` query text) | `REGRESSION_SNAPSHOT_RETENTION_DAYS` | 7 days, cleaned up on every poll cycle. Unlike EXPLAIN snapshots, `0` here does not mean keep forever: the cleanup code falls back to 14 days whenever the configured value is `<= 0` |
| Saved queries, reports, investigations, investigation candidates, regression alerts | No automatic expiry | Kept until deleted (investigations: a duplicate created by a race between two concurrent regression-alert opens is deleted immediately; that is not a retention policy) |

## Reports and share links

A report can contain whatever the underlying query returned — treat a generated
report, and especially a share link, as exactly as sensitive as the data behind it.
Share-link mechanics (token handling, expiry, SQL exposure toggle) are covered in
[Reports and sharing](../workbench/reports.md#share-links).

## LLM egress

Covered in full on [LLM providers](../integrations/llm.md#data-egress-and-privacy):
cloud providers require an explicit opt-in (`LLM_ALLOW_EXTERNAL_DATA`), row data is
withheld by default, and PII redaction and per-provider row caps apply when a cloud
provider is sent rows at all.

## Audit

`SECURITY_AUDIT_MODE` controls durability, not content:

| Mode | Behavior | Allowed in production? |
|---|---|---|
| `best_effort` | Logged asynchronously; a write failure never blocks the request | No |
| `required` | High-risk actions (`queries.run`, `reports.generate`, and every admin change: keys, memberships, connection permissions) **fail the request** if the audit write fails; other events remain best-effort | Yes |
| `buffered` | Queued (1,000 entries) and flushed in the background; on a full queue or a failed write, entries spill to a durable table and are replayed every 30 seconds | Yes |

`best_effort` is rejected under production StrictMode specifically because a
security review needs to know that logging silently dropping does not also mean
the request silently succeeded unaudited. Recorded event types include API
requests, authentication failures, and rate-limit rejections, plus key create and revoke,
membership and connection-permission changes, and share create and revoke. The application
role can insert and read `app.audit_logs` but not update, delete or truncate it (migration
`000059`); a role that owns the table can still grant those back, so run migrations as a
different owner if the audit trail must resist a compromised application role.

There is no REST API endpoint that reads `app.audit_logs`: nothing in this
codebase exposes an audit-log list or export route. The only way to read the
audit trail is a direct database connection with sufficient PostgreSQL
privileges, governed entirely by the role grants above, not by an application
role/permission check.

Every audit and request-log entry carries the client's `User-Agent` and IP
address (see below), but no other HTTP request header. A handful of other
headers are read (`Accept` for content negotiation, `Origin` for CORS,
`X-Request-ID` for correlation), but only for that immediate routing
decision; none of their values are written into a log or audit record.
Application (not audit) logs additionally record a request id, either
client-supplied via `X-Request-ID` or generated per request, purely for
correlating log lines to one request; it is not itself sensitive.

## Client IP address

The client IP recorded in `app.audit_logs.ip_address` and used as the
rate-limit key comes from `X-Forwarded-For`/`X-Real-IP` only when the
immediate connecting peer is in the configured `TRUSTED_PROXIES` list;
otherwise it is the raw TCP connection address. An untrusted caller cannot
spoof the recorded IP by sending its own forwarding headers.

## See also

[Trust model](../trust-model.md) · [Authentication and roles](authentication.md) ·
[Configuration reference](../reference/configuration.md#security) ·
[Health and monitoring](../operate/monitoring.md)
