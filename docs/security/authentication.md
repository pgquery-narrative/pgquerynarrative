# Authentication and roles

## Whether auth is required at all

`SECURITY_AUTH_ENABLED=false` requires an explicit
`SECURITY_ALLOW_INSECURE_NO_AUTH=true` — an opt-in for local/dev open access, and
one that's rejected outright under production StrictMode. With auth off, requests
run as an implicit admin principal.

## Ways to authenticate

| Method | How | Notes |
|---|---|---|
| Static API key | `Authorization: Bearer <SECURITY_API_KEY>` | Plaintext; rejected in production — use a hash instead |
| Hashed API key | `SECURITY_API_KEY_HASH` (unsalted SHA-256 hex) | Compared in constant time |
| Managed keys | `SECURITY_API_KEYS_JSON`, or `POST /admin/api-keys` | Each carries a role, scopes (`admin`/`write`/`read`), an optional expiry, and can be revoked (`POST /admin/api-keys/{id}/revoke`) without touching the others |
| Session cookie | Browser login via OIDC | HttpOnly, `Secure` under StrictMode, `SECURITY_SESSION_TTL` (default 8h) |
| OIDC bearer JWT | `SECURITY_OIDC_ISSUER`/`_AUDIENCE`/`_JWKS_URL` | For machine-to-machine or API clients presenting an IdP-issued token |

At least one of `SECURITY_API_KEY`, `SECURITY_API_KEY_HASH`, `SECURITY_API_KEYS_JSON`
or `SECURITY_OIDC_ISSUER` is required whenever auth is enabled.

`SECURITY_API_KEYS_JSON` is checked strictly at startup, and the server refuses to start on
any of these, so a typo can never leave a key missing, over-privileged or never expiring:
invalid JSON, an unknown field (`keyhash`), an entry with neither `key` nor `key_hash`, a
`key_hash` that is not 64 hex characters, a missing or unrecognised `role`, an `expires_at`
that is not RFC 3339 (`2030-01-01T00:00:00Z`, not `2030-01-01`), an unknown scope, and an
array that holds no key when nothing else supplies a credential. With authentication
enabled and no usable key, every protected request answers `401`.

## Roles and write permission

| Role | Can do |
|---|---|
| `platform_admin` | Everything, including cross-organization admin routes |
| `admin` (tenant admin) | Everything within their organization |
| `analyst` | GET/HEAD/OPTIONS anywhere, plus a specific write allowlist: the full Investigation flow (create, suggest/rank, compare, fix, report), acknowledging a regression, creating/updating/running/deleting their own schedules and saved queries |
| `viewer` | GET/HEAD/OPTIONS only |

A role is never guessed. Accepted spellings are `platform_admin`, `tenant_admin` (also `admin`),
`analyst` and `viewer` (also `read`, `readonly`, `reader`). A key, membership or admin request
with any other role (`read-only`, `guest`) is refused. Where a role is read from an identity
provider, an unrecognised or missing value becomes `viewer`, the least privilege, and a stored
membership always overrides the token's claim.

`GET /reports/shared/{token}` is reachable by any role (including no auth at all) —
it is the one intentionally public read path.

## Admin routes

| Path | Requires |
|---|---|
| `/admin/api-keys[/…]` | Tenant admin or above |
| `/admin/organizations` | Platform admin |
| `/admin/memberships` | Tenant admin or above |
| `/admin/connection-assignments`, `/admin/connection-permissions`, `/admin/connection-secrets` | Tenant admin or above |
| `/me`, `/me/organizations`, `/me/organization` | Any authenticated user — view/switch their own memberships |

These are hand-registered HTTP routes (`cmd/server/admin_api.go`, `me_api.go`), not
part of the Goa/OpenAPI surface — see [API reference](../reference/api.md#manual-routes)
for the full classification.

## OIDC and organization membership

- Login is PKCE-based browser OIDC (`/auth/login`, `/auth/callback`, `/auth/logout`,
  `/auth/refresh`, `/auth/session`), registered only when
  `SECURITY_OIDC_ISSUER`/`_CLIENT_ID` are set.
- **`SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG` defaults to `false`**, and is **forbidden**
  (must stay `false`) under production StrictMode — a first-time OIDC login gets no
  organization membership unless one is provisioned explicitly via the admin API.
  When it is enabled outside production, a first login is auto-joined to the default
  organization as a viewer.
- A user with memberships in more than one organization must pick one explicitly
  (`POST /me/organization`); requests are scoped to that choice for the session.
- In production, `SECURITY_OIDC_AUDIENCE` is required whenever `SECURITY_OIDC_ISSUER`
  is set, and `SECURITY_SESSION_SECRET` is required whenever browser OIDC is
  configured.

## Rate limiting

`SECURITY_RATE_LIMIT_RPM` (per-client-IP, 0 = disabled) with an in-memory or
distributed (PostgreSQL-backed) implementation
(`SECURITY_RATE_LIMIT_DISTRIBUTED`). Failure mode
(`SECURITY_RATE_LIMIT_FAILURE_MODE`: `open`/`closed`/`local_fallback`) controls what
happens if the distributed backend is unreachable — `open` is refused whenever auth
is enabled, and under production StrictMode the distributed limiter itself is
required once rate limiting is on. See
[Health and monitoring](../operate/monitoring.md) for the metric and alert names, and
[Troubleshooting](../operate/troubleshooting.md) for what to do when the backend fails.

## See also

[Organizations and tenancy](tenancy.md) · [Data handling](data-handling.md) ·
[Configuration reference](../reference/configuration.md#security) ·
[API reference](../reference/api.md)
