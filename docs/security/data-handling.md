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
| Regression statement snapshots | `REGRESSION_SNAPSHOT_RETENTION_DAYS` | 7 days |
| Saved queries, reports, investigations | No automatic expiry | Kept until deleted |

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
| `required` | High-risk actions (`queries.run`, `reports.generate`) **fail the request** if the audit write fails; other events remain best-effort | Yes |
| `buffered` | Queued (1,000 entries) and flushed in the background; on a full queue or a failed write, entries spill to a durable table and are replayed every 30 seconds | Yes |

`best_effort` is rejected under production StrictMode specifically because a
security review needs to know that logging silently dropping does not also mean
the request silently succeeded unaudited. Recorded event types include API
requests, authentication failures, and rate-limit rejections.

## See also

[Trust model](../trust-model.md) · [Authentication and roles](authentication.md) ·
[Configuration reference](../reference/configuration.md#security) ·
[Health and monitoring](../operate/monitoring.md)
