# Security Policy

## Supported versions

Fixes land on `main` and ship in the next release. Only the latest minor release
receives security fixes; there are no long-term support branches.

| Version | Supported |
| ------- | --------- |
| 2.2.x   | Yes |
| 2.1.x   | No, upgrade to 2.2.x |
| 2.0.x   | No |
| 1.x     | No |

## Reporting a Vulnerability

We take security vulnerabilities seriously. If you discover a security vulnerability, please follow these steps:

### 1. **Do NOT** open a public issue
Security vulnerabilities should be reported privately to protect users.

### 2. Report Privately
Use [**Report a vulnerability**](https://github.com/pgquery-narrative/pgquerynarrative/security/advisories/new),
which opens an advisory visible only to maintainers.

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### 3. Response Timeline
- **Acknowledgement**: within 3 working days
- **Initial assessment**: within 10 working days
- **Fix or documented mitigation**: tracked in the advisory until closed

This is a small project, not a vendor with a 24/7 rota. If something is being
actively exploited, say so in the first line and it will be prioritised.

### 4. Disclosure Policy
- We will acknowledge receipt of your report
- We will keep you informed of the progress
- We will credit you in the security advisory (if you wish)
- We will coordinate public disclosure after a fix is available

## What the boundary guarantees

These are the properties the project intends to hold. A reproducible break in any
of them is a vulnerability, and worth reporting.

- **Analytical queries are read-only, enforced by database privilege**, not by an
  application flag. The querying role cannot `INSERT`, `UPDATE`, `DELETE` or run
  DDL, and that holds even when `transaction_read_only` is lifted, which index
  cost projection requires for hypopg. `tools/db/verify_security.sh` asserts this
  and CI runs it on every pull request.
- **Only `SELECT`/`WITH` reaches the database**, validated by walking the
  `pg_query` parse tree rather than matching strings.
- **Bind values are never spliced into SQL as syntax.** A value that merely looks
  like a timestamp is quoted and escaped into an inert literal, or refused.
- **Organization isolation is enforced by row-level security** on the application's
  own metadata (`app.*`), keyed on the organization; analytical database access is
  isolated separately, by connection assignment. See
  [Organizations and tenancy](https://pgquery-narrative.github.io/pgquerynarrative/security/tenancy/).
- **Query results are not sent to an external LLM unless explicitly configured.**
  The investigation loop runs with no model at all.
- **PgQueryNarrative never automatically applies a proposed rewrite, index, or DDL
  to the analytical target database.** Its own metadata (investigations, reports,
  audit records, migrations) is written by the application as part of its normal
  operation; that is a different thing from acting on your data.

## Known limits (not vulnerabilities)

- **`APP_ENV=demo` fabricates workspace KPIs** so the demo has something to show
  against an empty database. It is off by default and gated in one place
  (`app/service/workspace.go`). Never enable it where the numbers will be acted on.
- **Reports may contain query results**, whatever your SQL selected. Treat a
  generated report, and especially a share link, as being as sensitive as the data
  behind it.
- **The `app` schema stores SQL text.** With a data encryption key configured it is
  sealed at rest; without one it is stored in plaintext.
- **Anything under `app/*` is internal** and outside the `pkg/narrative` SemVer
  guarantee. A breaking change there is not a security issue.

## Scope

In scope: this repository, the published container image, and the Helm and
Kubernetes manifests under `deploy/`.

Out of scope: vulnerabilities in PostgreSQL itself, in a model provider you
configure, or issues that require an attacker to already hold database superuser
or host root.

## Security Features

### Current Security Measures
- SQL injection prevention (AST query validation via pg_query)
- Read-only query execution (separate database role + session flags)
- Query timeout / result size / column limits
- Schema allowlisting (`DATABASE_ALLOWED_SCHEMAS`)
- API authentication (hashed API keys, managed keys, OIDC)
- Distributed rate limiting with fail-closed modes in production
- Audit trail with required/buffered durability modes
- EXPLAIN snapshot sealing (AES-GCM) when encryption keys are configured
- Security headers (CSP, frame denial, etc.)
- **StrictMode** (`APP_ENV=production` / `SECURITY_STRICT=true`): process refuses to start on unsafe config; Helm chart fails install on placeholder secrets
- Open-admin disabled unless `SECURITY_ALLOW_INSECURE_NO_AUTH=true` (forbidden in production)
- Default query schema allowlist is `demo` only; `app`, `pg_catalog` and `information_schema` can never be allowlisted; readonly role cannot read `app.*`. Unqualified and `pg_catalog`/`information_schema` *functions* not on the deny-list are still callable, see [Query execution safety](https://pgquery-narrative.github.io/pgquerynarrative/security/query-safety/)
- Root `docker-compose.yml` is localhost-bound local/dev only; production-shaped compose lives under `deploy/docker/` (both build the same root `Dockerfile`)
- Webhook hostname allowlist is **required** (empty fails closed); NetworkPolicy + HSTS (when HTTPS) in deploy templates
- Query/EXPLAIN errors do not embed Postgres driver detail; SQL at-rest seal fails closed when a key is configured
- Rate-limit failure mode cannot be `open` when auth is enabled

### Production StrictMode (mandatory for company data)
Key gates: auth on, no plaintext API keys, TLS DB modes, non-placeholder passwords, rate-limit failure mode not `open`, audit not `best_effort`, share links / EXPLAIN ANALYZE off, webhook allowlist when schedules enabled. Full list: `docs/operate/production.md`. See also `docs/trust-model.md` and `docs/operate/deployment.md`.

## Security Scanning

We use automated security scanning:
- **GitHub CodeQL**: Static analysis
- **Dependabot**: Dependency vulnerability alerts
- **govulncheck**: Go vulnerability scanning
- **gosec**: Go security checker
- **TruffleHog**: Secret scanning

## Security Updates

Security updates are released as:
- **Critical**: Immediate patch release
- **High**: Patch release within 7 days
- **Medium**: Next minor release
- **Low**: Next major/minor release

## Acknowledgments

We thank all security researchers who responsibly disclose vulnerabilities. Contributors will be credited in security advisories (with permission).
