## [Unreleased]

Trust boundaries. A deep review of the repository found that four of the
guarantees the product makes were not actually enforced end to end: a rewrite
could change results, the schema allowlist governed tables but not functions,
the fix lifecycle mixed up two databases reporting the same `queryid`, and
"verified" claimed more than the algorithm delivers. This release closes all
four, plus the lifecycle and deployment gaps found alongside them.

### Breaking

- **`col::numeric = const` and `col::text = 'const'` are no longer rewritten.**
  Dropping a cast off a column is only sound when the column's real PostgreSQL
  type makes the cast a no-op, and the rewriter has no catalog access to know
  that. On a `numeric` column holding `1.4`, `amount::integer = 1` is TRUE while
  `amount = 1` is FALSE; `price::text = '12'` is FALSE for `12.0` while
  `price = 12` is TRUE; and on a `text` column the rewrite produced SQL
  PostgreSQL rejects outright (`operator does not exist: text = integer`). The
  `implicit_cast` rewrite category no longer exists. Re-enabling this needs
  catalog-resolved column types and a rewrite only when the cast target equals
  the source type.
- **`POST /api/v1/investigations/{id}/fix` no longer accepts `fix_status` of
  `confirmed` or `regressed`.** Both are post-deployment *measurements* derived
  from `pg_stat_statements` by the regression poller. Accepting them over the
  API made a hand-set status indistinguishable from a measured one, which
  defeats the point of measuring. They remain readable, and remain valid
  *source* states — a fix can still be re-applied or abandoned after the poller
  has ruled on it.
- **Generating a report on `SampleMatch` equivalence now requires
  `accept_sample_match=true`.** `SampleMatch` is the fallback taken when
  full-result fingerprinting could not run; treating it as automatically
  shippable put sampled evidence on the same footing as verification. The UI
  asks for confirmation before sending it, and the resulting report carries
  `results_sampled` in its provenance so a reader can tell the two apart. It is
  a query parameter rather than a request body, because giving this endpoint a
  body would reject every existing body-less `POST`.
- **The SQL validator rejects statements it previously allowed.** Side-effecting
  and administrative functions, schema-qualified functions outside
  `DATABASE_ALLOWED_SCHEMAS`, `SELECT ... INTO`, and `FOR UPDATE` / `FOR SHARE`
  now fail validation. See Security below.
- **`connection_mode` and `tenant_isolation` on the security-trust endpoint
  changed values.** `connection_mode` is now derived from the live read-only
  probe (`Read-only (verified)` / `Read-only not verified`) instead of the
  constant `"Read-only"`, which could contradict the `readonly` field beside it.
  `tenant_isolation` reports `Row-level security on the metadata store` instead
  of `Dedicated database (RLS)`: whether the analytical database is physically
  dedicated is a deployment property this endpoint cannot observe, and RLS being
  enabled is not evidence for it.

- **PostgreSQL extension 1.1 withholds `EXECUTE` from `PUBLIC`.** Roles that used
  the `pgquerynarrative_*` functions lose access on `ALTER EXTENSION pgquerynarrative
  UPDATE` until the owner runs `SELECT pgquerynarrative_grant_access('role')`. The API
  URL is now a stored setting that only the owner can change with
  `pgquerynarrative_set_api_url`; the old per-session URL override is gone.

- **`SECURITY_API_KEYS_JSON` and roles are checked strictly.** The server refuses to start
  on a key list that could weaken or drop a key: invalid JSON, an unknown field, an entry
  with neither `key` nor `key_hash`, a `key_hash` that is not 64 hex characters, a missing
  or unrecognised `role`, an `expires_at` that is not RFC 3339, an unknown scope, or no
  usable credential. `read-only`, `guest` and a missing role used to become `analyst`; the
  configuration and the admin API now refuse them, and a role that an identity provider
  omits or that is unrecognised becomes `viewer`. Fix the entry the startup message names.
- **`/web/reports/export/{md,json,sql}` require authentication**, as the HTML and PDF
  exports already did.
- **Statement statistics on a database role shared by several organizations** are limited
  to platform administrators (`STAT_STATEMENTS_SHARED`). The regression poller skips such a
  connection, and the workspace overview's two workload totals read zero for other users
  there. Give each organization its own read-only credentials to see its own. Single-
  organization installs and organizations with their own credentials are unaffected.
- **Migrations `000058` to `000060`; the schema gate is now 60.** Run them before the new
  binary, or `/ready` reports 503. What they change is under Security.
- **The `pqn` alias in the CLI container's shell (`make cli-shell`) is removed.** `pqn` now
  names the terminal tool for the `pqn` extension, so the alias would have run a different
  program. Use `pgquerynarrative`.

### Security

- **The schema allowlist now covers function calls, explicit operators and type
  names.** It walked `RangeVar` (table) nodes only, so
  `SELECT other_schema.some_function(...)` was ungoverned while
  `SELECT other_schema.table` was blocked. Operators and types are backed by
  functions in the same schema, so `SELECT 1 OPERATOR(other.+) 2` and
  `col::other.t` reach code in a disallowed schema without ever producing a
  function-call node; all three are now held to the same allowlist as tables.
- **Side-effecting functions are denied by name.** A read-only transaction stops
  writes; it does not make every `SELECT` expression side-effect free. Denied:
  session-scoped advisory locks (which outlive the transaction and poison the
  pooled connection they were taken on), `set_config`, `pg_sleep`, server-side
  file and large-object access, sequence mutation, backend/WAL/replication
  control, `pg_stat_*_reset`, `pg_notify`, `dblink`, and the `query_to_xml`
  family (which executes a second, unvalidated query). Unqualified `count()`,
  `now()`, `coalesce()` and friends are unaffected. Documented in
  `docs/trust-model.md`.
- **`SELECT ... INTO` and row-locking clauses are rejected by the validator.**
  The read-only transaction already blocked the table creation, but the
  validator claims to enforce read-only semantics and should say so first.
- **Migration credentials no longer survive into the server process.** The
  container entrypoint used them for migrations and then `exec`'d the server
  with `DATABASE_MIGRATION_*`, `PGPASSWORD` and `DB_URL` still in its
  environment, handing a compromised server process the `CREATE EXTENSION` and
  `ALTER ROLE` rights the runtime/migration role split exists to withhold. They
  are now unset before handover. Running migrations as a separate Job remains
  the stronger shape; `PGQUERYNARRATIVE_SKIP_MIGRATIONS=true` supports it.
- **Migrations fail closed in production.** In strict mode with no migration
  credential, startup is now a configuration error instead of an attempt that
  fails partway with an opaque permission error. The entrypoint mirrors
  `config.StrictMode()` — `APP_ENV` of `production`/`prod` in any case, or
  `SECURITY_STRICT` — rather than matching one exact string, so every strict
  deployment is covered. `PGQUERYNARRATIVE_SKIP_MIGRATIONS=true` opts out for
  deployments that migrate from a separate Job.
- **Every external GitHub Action is pinned to a commit SHA.** Four were on
  mutable tags. The pin checker verified only refs that were already SHAs, so an
  action written as `@v7` was structurally invisible to it; it now rejects any
  unpinned external `uses:`.
- **The `golang-migrate` CLI is pinned to the version `go.mod` depends on.** It
  was invoked as `@latest` from a `golang:1.24-alpine` container, so the day
  `v4.20.1` shipped requiring Go >= 1.25.11 every migration job in CI began
  failing — unrelated to any code change. The CLI now tracks the library the
  tests link against (`MIGRATE_VERSION`), and the container image satisfies
  `go.mod`'"'"'s `go` directive (`MIGRATE_GO_IMAGE`).

- **The PostgreSQL extension can no longer be redirected by any role.** In 1.0
  `pgquerynarrative_set_api_url` was executable by `PUBLIC` and set a session
  setting, so any role could make the database server send HTTP requests to an address
  of its choosing. In 1.1 the URL is stored, the owner alone can change it, and it must
  be `http://` or `https://`. Callers also supply their own API key with
  `pgquerynarrative_set_api_key`, sent as a Bearer token, so the extension works
  against a server with authentication enabled. Verified by
  `tools/db/verify-extension.sh`.

- **Bad key configuration no longer turns authentication off.** With authentication
  enabled, an invalid, empty or misspelled `SECURITY_API_KEYS_JSON` used to start the server
  and serve every caller, with no token or a wrong one, as `platform_admin`, in production
  too. `AuthRequired()` now depends only on the enable switch, so a server with no usable
  key answers `401`, and startup refuses the configuration (see Breaking).
- **Every route is decided.** The report exports `md`, `json` and `sql` ran as the default
  organization's admin with no credential, and `app/httpmw` had no tests. Everything under
  `/web/reports/export` except the shared-link PDF is now authenticated by prefix, and a
  route-matrix test fails when a route `main.go` registers is reachable without a credential
  and is not listed as public.
- **The audit trail records what it should, and the application role cannot edit it.**
  `audit_logs_event_type_check` rejected seven event types the code emits (key create and
  revoke, membership and connection-permission changes, share create and revoke, raw-SQL
  views), so they were dropped in `best_effort` and, in `required` mode, failed the request
  after the change had been applied. Migration `000058` allows them, and a test compares the
  code's event types with the latest constraint. Migration `000059` revokes `UPDATE`,
  `DELETE` and `TRUNCATE` on `audit_logs` from the application role; a role that owns the
  table can grant them back, so run migrations as a different owner if the trail must resist
  a compromised application role.
- **`schema_to_xml`, `database_to_xml`, their `_xmlschema` forms and `ts_stat` are denied.**
  Each reads a schema outside the allowlist.
- **Row-level security on the identity tables** (migration `000060`): `organization_members`,
  `oidc_group_org_mappings` and `audit_log_buffer` now isolate organizations like the rest
  of the schema. Login keeps working through two read-only lookups (a user's own memberships,
  and the mappings for the token's groups). Those lookups are `SELECT` policies only:
  `INSERT`, `UPDATE` and `DELETE` have their own policies that name the current organization,
  so a session that set a login lookup could otherwise have deleted a user's memberships in
  other organizations.
- **Statement statistics on a shared role are refused by what the connection is, not what the
  role is called.** The check compared the pool's login name with the configured one, so a
  shared role with a non-default name skipped it. It now asks whether the organization has
  credentials of its own; an unknown case counts as shared.
- **The admin API no longer returns database errors.** Nine write handlers answered `400` with
  the raw error (constraint and table names). A conflict is `409 already exists`, a bad
  reference or value is a fixed `400`, and anything else is a fixed `500`; the error is logged.
  A message the store wrote for the caller (`user_id … are required`) is still shown.
- **`pqn`: analyst SQL measured by `measure_pair` runs read only.** It runs as `pqn_owner`, so
  a volatile function inside a statement could write. It now runs in a read-only sub-transaction
  that is rolled back on exit, so `prove()` can still record afterwards.
- **Only a schedule's owner or an admin can update, run or retry it**, as the docs said.
- **The application does not depend on the read-only role's stored defaults.** A role can
  reset its own defaults and PostgreSQL cannot prevent it, but the server already set the
  search path and timeouts on every connection and opened every statement `READ ONLY`. A
  test wipes the role's defaults and proves it.

### Added

- **`pqn`: the product's pitch from a terminal, inside your database.** *We do not ask you for the
  rewrite. We propose it from the plan, then prove it.* `pqn` is a PostgreSQL extension plus a
  terminal tool (`make build-pqn`, `bin/pqn`) that needs no PgQueryNarrative server, no REST API and
  no API key: it logs in as you.
  - `pqn top` ranks the statements that cost the most. `pqn investigate` reads the plan, names what
    is wrong (sequential scan, a function on a column, a missing index), proposes a rewrite from the
    plan with the existing rewrite engine, and **proves it**: both statements are run, the rows are
    compared by a fingerprint of every row, and both are timed. `Proven` means the same rows and at
    least 1.2x faster. A rewrite that returns different rows is `Different`, however fast, and exits
    with code 2. Nothing is created or changed in your database, and index proposals stay review
    only. Plan, findings and proofs are recorded in an audit ledger under the caller's own name.
  - Proof timing runs the statements the way your application does. `EXPLAIN ANALYZE` uses parallel
    workers; timing through a cursor does not, and had overstated the speedup of a parallel statement
    (88x measured, 36x real). `make verify-pqn-pitch` checks the pitch against independent oracles on a
    17-million-row database. `pqn evidence 7 --json` now honors the flag after the id.
  - Per-person limits are enforced from outside the session. A statement timeout is a setting a
    person can lift for themselves, so `enroll` also records it where they cannot reach it and
    `pqn_api.enforce_limits()`, run every few seconds from `pg_cron` or cron, cancels any enrolled
    person's statement that has outlived it, even after they lifted their own timeout or reset
    their role settings.
  - A proof `prove()` computes is stamped `"source": "database"`; one stored with
    `record_evidence`, as the replica flow does, is stamped `"source": "client"` whatever its
    payload says.
  - Everything the tool prints passes a filter that strips terminal control characters:
    titles, notes and proof text are chosen by the people who write them, and an administrator
    reads them in a terminal.
  - The extension writes only to its own ledger, reads your data only through views a DBA chose, runs
    analyst SQL as one read-only statement, and lets PostgreSQL do the authentication. An ordinary
    non-superuser installs it, it works on a hot standby for everything that reads, and
    `DROP EXTENSION` keeps the ledger. `verify_setup()` (`pqn doctor`) is the safety report.
  - **Installing is two commands.** As a superuser, `CREATE EXTENSION pqn` creates the roles it needs and
    `SELECT pqn_api.init()` creates the ledger. `make build-pqn-image` builds a PostgreSQL image
    (`tools/docker/postgres-pqn.Dockerfile`) that does both on first start with `pg_stat_statements`
    preloaded, so a published image is just `docker run`. An installer who is not a superuser still uses
    `pqn-roles.sql`.
  - Tables are exposed with a scope: `view` (only the listed columns can be read or planned) or
    `full` (statements over every column can be planned and measured, while `run` still returns only
    the listed columns).
  - **Delivery.** Release archives now include `bin/pqn` and `pqn-extension/` (the extension files and an
    `install.sh`). The release workflow builds and signs a PostgreSQL image with `pqn` per major version
    (`ghcr.io/<owner>/<repo>/pqn-postgres:16`, `:17`, `:18`). CI runs the extension on PostgreSQL 16, 17 and 18,
    the tool, the pqn documentation, and the image on four bases (`make verify-pqn-image`), which includes
    swapping the image under an existing data volume. `tools/db/pqn-heavy-scenario.sh` plays a user story on
    17 million rows (not part of CI).
  - Verified on PostgreSQL 16, 17 and 18 by `make verify-pqn-extension` (including a primary with a
    hot standby and a real 1.0 to 1.1 upgrade that keeps the data) and `make verify-pqn-cli`
    (the terminal tool, end to end, against a slow-query lab). See
    `docs/integrations/postgres-extension.md`.

### Fixed

- **`pqn investigate` no longer suggests dropping an index that its own proposed rewrite uses.**
  An index can show no scans only because the slow statement cannot use it. The finding and its
  `DROP INDEX` suggestion are withheld for an index a proposal uses, and the report says why.
- **`pqn` reads flags written after the statement.** `pqn investigate "SELECT …" --no-record` used to
  treat `--no-record` as part of the SQL (after `--`, a comment), so it still wrote to the ledger;
  `--replica`, `--bind`, `--title`, `--json`, `--dsn` and `-n` were ignored the same way. Flags now
  work anywhere. An unknown flag is an error with a hint, a statement given twice (`--sql` plus words,
  or `--file`) and stray arguments (`pqn top 5`) are refused, and `pqn run SELECT -1` and a `--`
  comment after a word still reach PostgreSQL as SQL.
- **Migration `000058` adds its constraint `NOT VALID`.** A validated `ADD CONSTRAINT` scans the
  audit table under an exclusive lock, which blocks every audit insert. The constraint still
  applies to new rows, and no existing row can violate it.
- **A schedule's owner is looked up inside its organization.** With row-level security on
  `organization_members` the worker's lookup, which had no organization, found nothing and disabled
  the schedule as "owner unauthorized". The claimed organization is now set first.
- **`pqn`: `prove` no longer calls two empty results proven.** Equal because both are empty is not a
  comparison; the verdict is `Unverified` and the reason says so.
- **`pqn`: `unexpose` takes back the removed view's columns.** With another view on the same table
  it kept every column grant, and a removed `full` view left the whole table readable.
- **`pqn`: `enroll` requires a unit on the timeout.** `'500'` was set as 500 ms by PostgreSQL and
  recorded as 500 s. Use `15s`, `500ms` or `2min`.
- **`pqn`: one refused cancel no longer stops `enforce_limits()`.** Cancelling a superuser's session
  raised an error and aborted the pass for everyone else; it is now reported as not cancelled.
- **`pqn` prints each wrapped `note:` once**, and every `--json` field is `snake_case`.
- **The setup check's standby message was wrong.** It said `record_*` "reflects this server only";
  on a standby `record_*`, `investigate` and `prove` fail, and only `top()` reflects that server.

- **Validator rejections explain themselves again.** The four new validator
  errors were not in `ClassifyRunError`, so a query calling a denied function
  surfaced as a bare "Query validation failed." with no reason, unlike every
  other validator rejection.
- **An acknowledged regression alert is re-opened when it escalates.** Now that
  one alert absorbs every later detection for a query, a regression that grew
  worse after being acknowledged would otherwise never return to the inbox.
  Acknowledgement still suppresses a steady regression while it is handled.

- **The fix lifecycle mixed measurements across connections.** A `queryid` is
  only unique *within* a connection, but the apply-time baseline
  (`UpdateFix`), the post-deploy re-measurement (`reconcileAppliedFixes`) and
  the SQL lookup (`latestSnapshotSQL`) all keyed on `(organization, queryid)`.
  Two databases reporting the same `queryid` had their independent cumulative
  counters interleaved into one `lag()` series, which could confirm a fix on one
  database from another's traffic, mark a genuinely improved query as regressed,
  or open an investigation with the wrong database's SQL. All three now key on
  `(organization, connection, queryid)`, matching the detection query.
- **Acknowledging a regression alert broke both uniqueness and recovery.** The
  partial unique index and the auto-resolve query both required
  `acknowledged_at IS NULL`, so acknowledging an alert let the next poll open a
  *second* alert for the same query, and left the acknowledged one unresolved
  forever even after the query recovered. Acknowledgement ("an analyst has seen
  this") and resolution ("performance returned to baseline") are now
  independent.
- **Report persistence and investigation completion are now one transaction.** A
  failure between them left a stored report attached to an investigation that
  still reported itself incomplete.
- **Opening an investigation from a regression alert is now an atomic claim.**
  Two concurrent callers could both see `investigation_id IS NULL` and both
  create one. The link is a compare-and-swap; the loser discards its own
  investigation and returns the winner's.
- **Evidence serialization errors are no longer swallowed.** `json.Marshal`
  failures on plan, stat and report evidence were discarded with `_`, which
  could persist `null` into the columns whose whole purpose is keeping evidence.
- **The investigation list no longer nests queries inside a result iteration.**
  It held one pool connection open while acquiring two more per row, which under
  concurrency with a bounded pool deadlocks rather than merely running slowly.

- **The production startup boundary probe no longer fails on the project's own read-only
  role.** Migration `000011` sets `default_transaction_read_only=on`, so a plain write probe
  failed with SQLSTATE 25006 and production start was refused. The probe lifts the flag first.
- **Errors the services already returned now have their real status.** Deleting a schedule
  or dashboard that is not yours or does not exist is `404`, and share links being disabled
  is `400` (both were `500`: the Goa design did not declare the error). The design also
  declares the validation error `save` and `create_share` can return.
- **Admin API errors.** Unknown roles and scopes are `400`, a key created without `scopes`
  no longer fails with `500`, and database errors are logged instead of returned.

### Documentation

- **Installing and setting up the `pqn` extension is documented and executed.** A
  [quick start](docs/getting-started/pqn-extension.md) and an
  [installation guide](docs/getting-started/pqn-installation.md) cover the extension files, the
  role script, creating and initializing the extension, exposing tables, enrolling people, the
  setup check, a read replica, upgrading, backup and restore, and uninstalling, with the real error
  messages and their fixes, and stays short. `make verify-pqn-docs` runs every `bash` block of both pages
  against real servers, and also runs the upgrade, restore and uninstall steps and a non-superuser DBA.
  Restoring onto a server without the roles fails, so the guide restores the roles first with
  `pg_dumpall --roles-only`. The verified-rewrite case study no longer says the tool "mathematically
  proves" equivalence (it is a verification, never a proof), and `docs-contract-check` now rejects that
  wording. `make docs-contract-check`
  now fails when those pages name a file, role, function, `make` target, command, flag, warning or
  error message that the code does not have. A [`pqn` reference](docs/reference/pqn.md) lists
  every command, flag, environment variable, SQL function, role and table, and the same check
  fails when it leaves any of them out. The CLI reference, the two workflow pages `pqn`
  builds on, the index, the README and the roles page now link to it.

- **The documentation was reorganised by audience** — Learn → Investigate →
  Integrate → Secure → Deploy & Operate → Reference → Develop. New pages:
  `architecture.md` (system map, request paths, the metadata vs analytical
  database split, background workers), a Core Workflows section (investigate,
  plan findings, candidates, compare, verify results, regressions & applied
  fixes, multiple connections), a Security & Access section, a real Deploy &
  Operate section (production StrictMode checklist, health/monitoring, migrations
  & upgrades, incident runbooks), and lookup-only Reference pages
  (`reference/configuration.md` now covers every environment variable,
  `reference/api.md` every Goa operation plus the manual routes,
  `reference/api-errors.md`, `reference/evidence.md`,
  `reference/versions-limits.md`). Old URLs redirect via `mkdocs-redirects`.
- **`make docs-contract-check` (new)** ties the documentation to the code: the Go
  version, the Compose Postgres default, every configuration variable and a set
  of critical defaults, the release platform matrix, every OpenAPI operation,
  every structured error code, forbidden stale vocabulary, and the
  links/anchors in the repo-root Markdown that MkDocs does not build. It runs in
  the CI `Docs` job and in `make test-unit` (`tools/docscheck`).
- **External links are checked in CI** by a new `docs-links` workflow (lychee,
  pinned), and locally by `make docs-links`. Config in `.lychee.toml`.
- **`make docs` binds the preview to `127.0.0.1` and drops the TTY assumption**;
  `docs/Dockerfile` now installs pinned packages from `docs/requirements.txt`.
- Corrected factual drift across the docs and repo Markdown: Go 1.26, the Compose
  Postgres default (`postgres:16-alpine` + HypoPG, built not pulled), the
  `make seed` row count (300,000), `regression_alert_id` (not `regression_id`),
  the equivalence report gate (`VerifiedEqual`, or `SampleMatch` with
  `accept_sample_match=true`), the three database identities, the release
  platform list (four, including `linux/arm64`), `SECURITY_OIDC_AUTO_JOIN_DEFAULT_ORG`
  defaulting to `false`, the schema-migration gate being a readiness check rather
  than a startup one, and the `pgquery-narrative` GitHub organization for
  browser/clone URLs (the Go module path is unchanged).

### Changed

- **`VerifiedEqual` is described as full-result fingerprint verification, not
  proof.** The check is a `count` + `sum` + `bit_xor` over a 64-bit hash of each
  row's text. That is strong whole-result verification, but it compares row
  *text* and is deliberately order-independent, so column types, column names
  and `ORDER BY` are not part of what it covers, and a collision is
  astronomically unlikely rather than impossible. README, in-code vocabulary,
  report narrative and UI captions now say this precisely, and the README's
  stale claim that `SampleMatch` applies "past the 1000-row cap" — removed in
  2.2.0 — is corrected.
