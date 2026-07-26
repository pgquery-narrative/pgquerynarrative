# Security Policy

## Supported Versions

We actively support the following versions with security updates:

| Version | Supported |
| ------- | --------- |
| 1.x.x   | Yes       |
| < 1.0   | No        |

## Reporting a Vulnerability

We take security vulnerabilities seriously. If you discover a security vulnerability, please follow these steps:

### 1. **Do NOT** open a public issue
Security vulnerabilities should be reported privately to protect users.

### 2. Report Privately
Create a [private security advisory](https://docs.github.com/en/code-security/security-advisories/working-with-repository-security-advisories/creating-a-repository-security-advisory) on GitHub (recommended), or contact the project maintainers through the repository.

Include:
- Description of the vulnerability
- Steps to reproduce
- Potential impact
- Suggested fix (if any)

### 3. Response Timeline
- **Initial Response**: Within 48 hours
- **Status Update**: Within 7 days
- **Fix Timeline**: Depends on severity (typically 30-90 days)

### 4. Disclosure Policy
- We will acknowledge receipt of your report
- We will keep you informed of the progress
- We will credit you in the security advisory (if you wish)
- We will coordinate public disclosure after a fix is available

## Security Best Practices

### For Users
- Always use the latest stable version
- Keep dependencies up to date
- Use strong database passwords
- Enable SSL/TLS for database connections in production
- Review and restrict database user permissions
- Monitor logs for suspicious activity

### For Developers
- Never commit secrets or credentials
- Use environment variables for sensitive configuration
- Review all pull requests for security issues
- Run security scans before merging
- Keep dependencies updated
- Follow secure coding practices

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
- Default query schema allowlist is `demo` only; `app` / system catalogs rejected; readonly role cannot read `app.*`
- Root `docker-compose.yml` is localhost-bound local/dev only; production-shaped compose lives under `deploy/docker/`
- Webhook hostname allowlist is **required** (empty fails closed); NetworkPolicy + HSTS (when HTTPS) in deploy templates
- Query/EXPLAIN errors do not embed Postgres driver detail; SQL at-rest seal fails closed when a key is configured
- Rate-limit failure mode cannot be `open` when auth is enabled

### Production StrictMode (mandatory for company data)
See `docs/ops/PRODUCTION.md`. Key gates: auth on, no plaintext API keys, TLS DB modes, non-placeholder passwords, rate-limit failure mode not `open`, audit not `best_effort`, share links / EXPLAIN ANALYZE off, webhook allowlist when schedules enabled.

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

## Resources

- [OWASP Top 10](https://owasp.org/www-project-top-ten/)
- [Go Security Best Practices](https://go.dev/doc/security/best-practices)
- [GitHub Security Best Practices](https://docs.github.com/en/code-security)
