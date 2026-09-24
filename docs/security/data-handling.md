# Data handling

What happens to SQL text and query results once they're inside PgQueryNarrative.

## Encryption at rest

AES-GCM with a key derived from `SECURITY_DATA_ENCRYPTION_KEY` (SHA-256 of the
secret), stored as a versioned envelope. Sealed today:

- Per-organization connection DSNs (passwords included)
- Saved query SQL, workbench report SQL, investigation report SQL, and schedule SQL
- EXPLAIN snapshot SQL and stored plans

Without a key configured, this SQL is stored **in plaintext**; sealing only
happens when `SECURITY_DATA_ENCRYPTION_KEY` (or, as a fallback,
`SECURITY_SESSION_SECRET`) is set, and one of the two is **required** under
production StrictMode. There is a single active key: no rotation and no
multi-key support, so rotating the key means re-sealing existing rows yourself.
Writes fail closed: if sealing errors, the write is rejected rather than falling
back to plaintext.

## Redaction

EXPLAIN snapshots are always redacted with a constant placeholder before storage,
independent of the encryption key. Saved-query and report SQL is sealed (when a key
is configured) but is **not** separately redacted; a person who can read the
decrypted row sees the original SQL, literals included.

Before an LLM prompt is built, `LLM_REDACT_PII=true` (default) strips common PII
patterns and SQL string literals from the SQL text and metrics that go into the
prompt, see [LLM providers](../integrations/llm.md#data-egress-and-privacy).

## Retention

| What | Control | Default |
|---|---|---|
| EXPLAIN snapshots | `SECURITY_EXPLAIN_SNAPSHOT_RETENTION_DAYS` | 90 days (0 = keep forever). Cleaned up every 6 hours |
| Regression statement snapshots | `REGRESSION_SNAPSHOT_RETENTION_DAYS` | 7 days |
| Saved queries, reports, investigations | No automatic expiry | Kept until deleted |
| Distributed rate-limit buckets | `SECURITY_RATE_LIMIT_BUCKET_MAX_AGE` | 24 hours of inactivity, swept every 10 minutes |
| Audit log (`app.audit_logs`), webhook delivery records, LLM audit events | No automatic expiry | Kept indefinitely; purge (or archive and truncate) is an operator task if retention limits apply to your deployment |

`app.audit_log_buffer` (used only in `buffered` audit mode) is transient
staging, not a retention store: each entry is removed once successfully
replayed into `app.audit_logs`; only an entry that keeps failing to replay
stays queued.

Browser sessions (`app.browser_sessions`) and short-lived OIDC PKCE state
(`app.oidc_pkce_states`) expire on their own schedule, covered under
[Sessions](#sessions-and-browser-storage) below, not this table.

## Reports and share links

A report can contain whatever the underlying query returned; treat a generated
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

Per-call LLM records (provider, model, policy decision, data classes, token
counts, cost) are a separate table, `app.llm_audit_events`, and are always
written best-effort: `SECURITY_AUDIT_MODE=required`/`buffered` does not extend
to them, so a durable-audit requirement for LLM usage specifically is not met
by that setting alone.

Audit rows include the client's IP address (`ip_address`, from `X-Forwarded-For`/
`X-Real-IP` only when the request came through a configured `TRUSTED_PROXIES`
entry, otherwise the direct connection address) and `User-Agent` string. Rows
never carry SQL text, row values, or LLM prompt/response content: `details` is
a JSON field of event-specific metadata (ids, decisions), not query content.

## Sessions and browser storage

The browser session cookie (`pgqn_session`) is `HttpOnly` always, `Secure` when
`APP_ENV=production`/`prod` or `SECURITY_STRICT=true`, and `SameSite=Lax`.
Default lifetime is 8 hours (`SECURITY_SESSION_TTL`). Server-side session state,
when a session store is configured, lives in `app.browser_sessions` with an
AES-GCM-sealed refresh token; rows are tied to the cookie's own expiry and
revocation, not the general retention table above.

The frontend also keeps a theme preference, guided-demo dismissal flags, and a
capped query-text history in `sessionStorage` (cleared when the tab closes) or
`localStorage`, purely for UI state; none of this is ever transmitted to the
server. Separately, the frontend can hold the caller's own API key in
`localStorage` and send it as the `Authorization` header on each request,
bypassing the server-managed session cookie entirely. This path is on
automatically in a Vite dev build and otherwise requires an explicit
`VITE_ALLOW_BROWSER_API_KEY=true` at build time; since `localStorage` is
readable by any script on the page (XSS, a malicious extension), treat that
flag as accepting that risk for a given deployment (an internal-only tool,
for example), not as a default for a public-facing production build.

## See also

[Trust model](../trust-model.md) · [Authentication and roles](authentication.md) ·
[Configuration reference](../reference/configuration.md#security) ·
[Health and monitoring](../operate/monitoring.md)
