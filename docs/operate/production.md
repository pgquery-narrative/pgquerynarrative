# Production configuration

Everything `config.Validate()` requires under **StrictMode** (`APP_ENV=production`,
`APP_ENV=prod`, or `SECURITY_STRICT=true`), checked at process start, so a
misconfigured production deploy fails fast rather than starting insecurely. This is
the authoritative source for what "production-ready" means here; treat it as the
checklist.

| Requirement | Rejected value | Env var |
|---|---|---|
| Open-admin opt-out is off | `SECURITY_ALLOW_INSECURE_NO_AUTH=true` | - |
| Auth is on | `SECURITY_AUTH_ENABLED=false` | - |
| Rate limiting is on | `SECURITY_RATE_LIMIT_RPM<=0` | - |
| Rate limiter backend is distributed | `SECURITY_RATE_LIMIT_DISTRIBUTED=false` when RPM > 0 | - |
| Rate-limit failure mode is not fail-open | `SECURITY_RATE_LIMIT_FAILURE_MODE=open` | - |
| Query timeouts are set | `QUERY_TIMEOUT`/`QUERY_LOCK_TIMEOUT`/`QUERY_IDLE_IN_TX_TIMEOUT` ≤ 0 | - |
| Result caps are set | `QUERY_MAX_RESULT_BYTES`/`_CELL_BYTES`/`_COLUMNS` ≤ 0 | - |
| TLS to the database | `DATABASE_SSL_MODE` not one of `require`/`verify-ca`/`verify-full` | - |
| Strong, non-default passwords | `DATABASE_PASSWORD`/`DATABASE_READONLY_PASSWORD` (and per-connection) < 16 chars, or a known placeholder | - |
| Schema allowlist is non-empty and excludes `public` | Empty `DATABASE_ALLOWED_SCHEMAS`, or including `public` | - |
| Connection allowlist is enforced: an org with no connection assignments cannot use every configured connection | `SECURITY_CONNECTION_ALLOWLIST_REQUIRED=false` | - |
| EXPLAIN ANALYZE is off | `SECURITY_EXPLAIN_ANALYZE_ENABLED=true` | - |
| Share links are off | `SECURITY_SHARE_LINKS_ENABLED=true` | - |
| Audit is not best-effort | `SECURITY_AUDIT_MODE=best_effort` | - |
| API key is hashed, not plaintext | Non-empty `SECURITY_API_KEY` | Use `SECURITY_API_KEY_HASH` |
| Managed keys use hashes | A `key` field (not `key_hash`) in `SECURITY_API_KEYS_JSON` | - |
| A data-at-rest key exists | Both `SECURITY_DATA_ENCRYPTION_KEY` and `SECURITY_SESSION_SECRET` empty | Set one, ≥32 chars, not a placeholder |
| OIDC auto-join is off | `SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG=true` | - |
| OIDC audience is set when OIDC is configured | `SECURITY_OIDC_ISSUER` set with empty `SECURITY_OIDC_AUDIENCE` | - |
| Session secret is set when browser OIDC is configured | Empty `SECURITY_SESSION_SECRET` with issuer + client ID set | - |
| Cloud LLM budgets fail closed | `LLM_BUDGET_FAIL_CLOSED=false` for a cloud provider | - |
| Cloud LLM row sampling is bounded | `LLM_MAX_SAMPLE_ROWS > 5` | - |
| PII redaction stays on for cloud row data | `LLM_REDACT_PII=false` with `LLM_SEND_ROW_DATA=true` and a cloud provider | - |
| Schedule runner uses durable leases | `SCHEDULE_RUNNER_ENABLED=true` with `SCHEDULE_DURABLE_LEASES=false` | - |
| Webhook destinations are allowlisted and signed | `SCHEDULE_RUNNER_ENABLED=true` with an empty `SECURITY_WEBHOOK_ALLOWED_HOSTS`, or a signing secret < 16 chars or a placeholder | - |

These checks run **regardless of StrictMode**: auth/key pairing, the open-admin
opt-in, and the rate-limit `open` restriction while auth is enabled.

## Not enforced by validation, but required in practice

- **A migration credential**, or `PGQUERYNARRATIVE_SKIP_MIGRATIONS=true` plus a
  separate migration Job: the entrypoint (not `config.Validate()`) refuses to start
  under StrictMode without one. See
  [Deployment: migration identity](deployment.md#migration-identity).
- **`SECURITY_OIDC_REDIRECT_URL`** has no production check, but defaults to
  `http://localhost:8080/auth/callback`. Set it explicitly to your real callback URL;
  an empty string in a values file is treated as unset, not as "no redirect".

## Weak-secret detection

Passwords and secrets are also rejected in production when they match a known
placeholder set (`changeme`, `change-me-*`, `replace-me`/`replace-with-*`,
`password`, `secret`, `admin`, `root`, the shipped default credentials, or any
string of one repeated character), independent of length.

## Verifying your configuration

The workbench **Security & Trust** page (`GET /api/v1/trust`) reports the hardening
actually in effect for a connection. `tools/ops/helm_strict_check.sh` renders the
Helm chart and asserts these gates hold without a live cluster (CI job
`Helm StrictMode gates`).

## See also

[Trust model](../trust-model.md) · [Deployment](deployment.md) ·
[Configuration reference](../reference/configuration.md) ·
[Data handling](../security/data-handling.md)
