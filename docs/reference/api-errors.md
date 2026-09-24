# API errors

Every error response is `{"name": "...", "message": "...", "code": "..."}`. `code`
is what to match on programmatically; `name`/`message` are for humans. This table is
checked by `make docs-contract-check` against every `strPtr("CODE")` literal in
`app/service`, plus the three codes emitted directly by the HTTP middleware.

| Code | HTTP | Condition | Likely remediation |
|---|---|---|---|
| `VALIDATION_ERROR` | 400 | SQL failed the read-only validator; an illegal fix-status transition; a missing/incomplete field (e.g. `SELECT * INTO`, an unsafe fix transition, a share expiry over 720h, an empty Ask question) | Fix the request per the message; see [Query execution safety](../security/query-safety.md) for the SQL rules |
| `TIMEOUT_ERROR` | 400 | The query exceeded `QUERY_TIMEOUT` | Simplify the query, raise the timeout, or add an index |
| `QUERY_RESULT_TOO_LARGE` | 400 | Result exceeded `QUERY_MAX_RESULT_BYTES` | Add a `LIMIT`, narrow columns, or raise the cap |
| `STAT_STATEMENTS_UNAVAILABLE` | 400 | `pg_stat_statements` is off or unreadable on the target connection | Enable the extension and `SECURITY_STAT_STATEMENTS_ENABLED` |
| `STAT_STATEMENTS_SHARED` | 400 | The connection's read-only role is shared by several organizations, so its statement text belongs to all of them | Ask a platform administrator, or give the organization its own read-only credentials |
| `CONNECTION_NOT_FOUND` | 400 | An unknown, non-empty `connection_id` | Check `GET /connections`; see [Multiple connections](../workflows/connections.md) |
| `CONNECTION_FORBIDDEN` | 400 | The organization lacks the required action on that connection | Grant the action via `/admin/connection-permissions`, or check `SECURITY_CONNECTION_ALLOWLIST_REQUIRED` |
| `ENCRYPTION_ERROR` | 400 | Sealing SQL at rest failed (a data-encryption-key problem) | Check `SECURITY_DATA_ENCRYPTION_KEY`/`SECURITY_SESSION_SECRET` |
| `STORAGE_ERROR` | 400 | A schedule (or similar) failed to persist | Check metadata database health |
| `SHARE_LINKS_DISABLED` | - | `POST /reports/share` called with `SECURITY_SHARE_LINKS_ENABLED=false` | Enable sharing, or don't call this endpoint |
| `EQUIVALENCE_SAMPLE_ONLY` | 400 | Report requested with equivalence `SampleMatch` and no `accept_sample_match=true` | Add `?accept_sample_match=true`, understanding it marks the report `results_sampled` |
| `EQUIVALENCE_NOT_EQUAL` | 400 | Report requested with equivalence `Different`, `Unverified`, or `NotRequested` | Re-run verification, or investigate why results differ, see [Verify result equivalence](../workflows/verify-results.md) |
| `NOT_FOUND` | 404 | The id doesn't exist (investigation, report, dashboard, schedule, run, share link, regression alert) | Check the id |
| `LLM_ERROR` | 500 | The LLM call failed, or returned no usable SQL/narrative | Check [LLM providers](../integrations/llm.md): provider, model, key, connectivity |
| `SCHEMA_ERROR` | 500 | Ask/chat couldn't load the schema needed to build a prompt | Check the connection's `schema` permission and reachability |
| `REPORT_ERROR` | 500 | Ask/chat's report step failed | Check LLM and metadata database health |
| `SESSION_ERROR` | 500 | Preparing, persisting, or loading a chat session failed | Retry; check metadata database health |
| `UNAUTHORIZED` | 401 | Missing or invalid credentials | Add a valid `Authorization` header or session |
| `FORBIDDEN` | 403 | Authenticated, but the role doesn't allow this method on this path | Use a role with sufficient permission, see [Authentication and roles](../security/authentication.md) |
| `RATE_LIMIT_EXCEEDED` | 429 | `SECURITY_RATE_LIMIT_RPM` exceeded for this client | Back off and retry |

A request that fails Goa's own payload decoding or validation (a missing required
field, a bad enum value) returns Goa's standard error shape, which does not carry a
`code` field; treat any 4xx without a recognized `code` as a request-shape problem
and check the field constraints on the relevant [API](api.md) entry.

## See also

[API reference](api.md) · [Verify result equivalence](../workflows/verify-results.md) ·
[Authentication and roles](../security/authentication.md)
