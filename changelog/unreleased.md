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
  `accept_sample_match: true`.** `SampleMatch` is the fallback taken when
  full-result fingerprinting could not run; treating it as automatically
  shippable put sampled evidence on the same footing as verification. The UI
  asks for confirmation before sending it.
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

### Security

- **The schema allowlist now covers function calls.** It walked `RangeVar`
  (table) nodes only, so `SELECT other_schema.some_function(...)` was ungoverned
  while `SELECT other_schema.table` was blocked. Schema-qualified calls are now
  held to the same allowlist as tables.
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
- **Migrations fail closed in production.** With `APP_ENV=production` and no
  migration credential, startup is now a configuration error instead of an
  attempt that fails partway with an opaque permission error.
- **Every external GitHub Action is pinned to a commit SHA.** Four were on
  mutable tags. The pin checker verified only refs that were already SHAs, so an
  action written as `@v7` was structurally invisible to it; it now rejects any
  unpinned external `uses:`.

### Fixed

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
