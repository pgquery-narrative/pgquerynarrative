# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Entries: edit `changelog/unreleased.md` then run `make changelog`.

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

### Fixed

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

## [2.2.0] - 2026-09-07

Evidence honesty. A review of the project against established PostgreSQL tooling
found six places where the numbers on screen claimed more than they had measured;
this release fixes all of them, and makes result verification both cheaper and
stronger while doing it.

### Breaking

- **`POST /api/v1/queries/explain/compare` renames the cost row and changes its format.**
  `Total cost … −26.9×` is now `Planner cost (estimate) … −96.3%`. Cost is an abstract
  quantity the planner uses to choose between plans — scaled page-fetch units, not
  proportional to elapsed time and not reliably comparable across plans — so rendering it
  as a fold change beside an execution-time row asserted a speedup the number cannot
  support. It is now a percentage, never an `N×`, and carries a caveat saying so. Anything
  parsing the `evidence` string or expecting `×` in `change` for that row must be updated.

### Added

- **`timing_runs` on compare (1–5, default 1).** Above 1, each side runs that many times
  under ANALYZE and the row reports the **median** with the observed range —
  `28ms (5 runs, 26ms–40ms)`. The median rather than the mean, so one cold or contended
  run cannot drag the figure to a number no execution produced.
- **Noise detection.** When the run-to-run spread is at least as large as the gap between
  the medians, the tool says the result is inside the measurement noise rather than
  printing a confident percentage.
- **`caveat` on `PlanComparisonMetric`.** Rendered inline beneath the number, and carried
  into exported reports, so a PDF says what the screen says.
- **`EXPLAIN (SETTINGS)` on every plan.** Non-default planner configuration —
  `random_page_cost`, `work_mem`, a disabled `enable_seqscan` — changes both the plan
  chosen and every cost shown, so a comparison is only meaningful when both sides were
  produced under the same settings. Costs no extra execution and does not imply ANALYZE.

### Changed

- **Result verification is roughly twice as cheap and no longer capped.** It ran four
  executions, and past 1000 rows sampled with `ORDER BY md5(row::text) LIMIT 1000` — a hash
  of every row plus a top-N sort of the entire result, on a query the user already
  considers too slow. It is now one aggregate pass per side:

  ```sql
  SELECT count(*), sum(hashtextextended(t::text, 0)), bit_xor(hashtextextended(t::text, 0))
  FROM (<query>) t
  ```

  No sort, three scalars on the wire, two executions instead of four. All three aggregates
  are commutative, so row order cannot affect the answer, and together they separate
  multisets any one would miss. **`VerifiedEqual` now means every row was compared, at any
  result size** — previously anything past 1000 rows could only ever be `SampleMatch`.
  The count-plus-sample path remains as a fallback, so `SampleMatch` is still reachable and
  the status enum is unchanged.
- **Execution time is labelled as the single sample it is** when `timing_runs` is 1.
- **The demo seeds 300k rows (~55 MB) instead of 8000.** At ~160 rows per partition the
  whole table sat in shared buffers, partition pruning saved microseconds, and run-to-run
  variance exceeded the difference being reported — the same query measured 2ms and 6ms on
  consecutive runs. The comparison now reports 24ms → 7ms. Seeding takes about 2 seconds;
  the 10M-row path (`make seed-large-docker`) is unchanged.

### Fixed

- **The "no rewrite offered" message was wrong and discouraging.** It omitted the
  `LEFT JOIN … IS NULL` → `NOT EXISTS` pattern and listed `COALESCE` unqualified when only
  `COALESCE` over a date column is handled. It now lists every pattern accurately and says
  that declining is the normal outcome, not a failure.
- **Release signature verification instructions.** Artifacts are signed as Sigstore v0.3
  bundles, which cosign v2 cannot read — it fails with `bundle does not contain cert for
  verification`, which reads like a bad signature rather than a version mismatch. The
  README now states that cosign v3 or newer is required and quotes the error.

### Documentation

- The README leads with the capability that is actually differentiated — proposing a
  rewrite and then proving the rows still match — and states plainly that the rewriter is a
  rule engine over PostgreSQL's parser with about six patterns, that it declines more often
  than it fires, and that a query outside those shapes yields plan findings and no rewrite.

## [2.1.0] - 2026-09-06

Query Investigation: propose a rewrite, prove it with the planner, and verify the rows still
match — plus a remediation pass over every place the tool previously overstated what it had
actually checked.

### Breaking

- **`POST /api/v1/queries/explain` no longer returns `execution_time_ms`.** The field reported
  the server's wall-clock time for the EXPLAIN round trip — network, planning, and parsing —
  even when the query was never executed, so it read as an execution time that nothing had
  measured. It is replaced by four honest fields: `request_wall_time_ms` (the round trip),
  `planning_time_ms` and `server_execution_time_ms` (PostgreSQL's own numbers, the latter
  non-zero only under ANALYZE), and `evidence_mode` (`estimated` or `observed`). Clients
  generated from the v2.0.0 OpenAPI spec must regenerate. This is the only field removed
  anywhere in the API in this release.

### Added

- **Query Investigation workflow:** open an investigation from SQL or a `pg_stat_statements`
  regression, get a verdict-first plan diagnosis, propose rewrite candidates, rank them with a
  dry EXPLAIN, and compare before/after plans.
- **Rewrite patterns:** `DATE_TRUNC` equality → sargable range, `LEFT JOIN … IS NULL` anti-join
  → `NOT EXISTS`, `OR` → `UNION`, and parameterized-query rewrites with `EXPLAIN (GENERIC_PLAN)`.
- **Result equivalence:** a compare can execute both queries and report a five-state status —
  `VerifiedEqual`, `SampleMatch`, `Different`, `Unverified`, `NotRequested` — using `COUNT(*)`
  plus an order-independent multiset fingerprint.
- **Index advice with hypopg:** planner-backed cost projection for a candidate index, labelled
  `hypopg` when real and `heuristic` when not, so a guess is never presented as a measurement.
- **Regression poller:** rolling baseline, self-observation filter, and an applied-fix lifecycle,
  polling every authorized connection with advisory-lock leader election.
- **Report export:** Markdown, JSON, and SQL, alongside HTML and PDF. PDF embeds a Unicode font.
- **Candidate history:** every tested candidate is kept, not just the winner.
- **Sample bind values** for comparing parameterized queries.
- **Security & Trust page** reports live per-connection posture: real TLS mode, real allowed
  schemas, real timeout and row cap, a live read-only probe, and the caller's actual permissions.

### Changed

- **Investigations use estimate-only EXPLAIN by default.** ANALYZE executes the query, so it is
  now opt-in per request rather than implied by opening an investigation.
- **Candidate ranking is honest about "no improvement."** A candidate that beats nothing gets no
  rank, and the list carries an explicit `recommendation` when nothing improves.
- **Regression detection compares poll-to-poll interval deltas**, not cumulative
  `pg_stat_statements` counters, whose percent changes grew with uptime until they always fired.
- **"Rows scanned" counts actual rows across loops** instead of the maximum at any single node.
- **Investigations are org-wide by design**; deletion is gated on `created_by`.
- **One deployment model:** the root `Dockerfile` is the blessed single image (API plus built
  SPA). The divergent `deploy/docker/` variant is removed.

### Fixed

- **`DATE_TRUNC('month', col) = '2025-01-15'` is no longer rewritten.** The predicate is
  unsatisfiable; widening it to the whole month changed the result set.
- **`OR` → `UNION` no longer drops NULL rows.** The generated branch negated the previous
  predicate with a plain `NOT`, which discards rows where it evaluates to NULL; it now uses
  `IS NOT TRUE`.
- **Bind substitution is hardened against injection.** The timestamp pattern was unanchored and
  quotes were not escaped, so a value beginning like a timestamp could inject a predicate.
- **Result verification requires the `query` permission,** not just `explain` — it executes rows.
- **Regression alerts cannot duplicate:** a partial unique index permits one open alert per
  (organization, connection, queryid).
- **Cross-organization integrity** for investigation children, via composite foreign keys.
- **Webhook retry backoff no longer overflows.** `base * 2^attempt` wrapped negative past attempt
  29, which would have scheduled a retry in the past.
- **Demo scenarios derive dates from the live dataset** instead of hardcoded literals that aged
  out and returned zero rows.
- **A fresh deploy of the container image now completes its migrations.** The entrypoint ran
  them as `DATABASE_USER`, the runtime role — which deliberately cannot create extensions or
  `ALTER ROLE`, because it also executes user SQL. Every fresh install stopped at migration
  `000019` with `permission denied to create extension "pg_stat_statements"`, so `docker compose
  up` on a clean volume, and any first deploy of the published image, could not work. Set
  `DATABASE_MIGRATION_USER` / `DATABASE_MIGRATION_PASSWORD` (or `DATABASE_MIGRATION_URL`) to a
  role that may; unset, behaviour is unchanged for an already-migrated database.
- **Migrations fail loudly on stale or dirty state,** with the recovery command printed.
- **Partition findings collapse even without a schema prefix.** The normalizer required
  `schema.table_YYYY_MM`, but EXPLAIN reports a bare relation when `search_path` resolves it,
  so those findings never grouped and reports listed one line per partition.
- **Plan-finding collapse in the UI** no longer folds a scan of the parent table into its own
  partition group, no longer labels plain duplicates as partitions, no longer drops the repeat
  count for non-partition duplicates, and no longer strips the month from a lone partition.
- **Rollback no longer breaks on a database that has been used.** The down migrations for
  `000008` (audit event types) and `000042` (SQL storage class) re-added a narrowed `CHECK`
  that is validated against existing rows, so any deployment that had served an HTTP request
  or enabled SQL encryption could not roll back past them. Both now add the constraint
  `NOT VALID`: enforced on new writes, and history is left intact rather than rewritten —
  which for an audit log would have falsified the record, and for encrypted snapshots would
  have relabelled ciphertext as plaintext.

### Security

- Read-only boundary is verified with hypopg's read-only lift in play: writes and DDL must still
  fail on privilege, not merely on the `transaction_read_only` flag.
- `golang.org/x/crypto` and the CI/Docker Go toolchain bumped to clear `govulncheck`.
- The security policy now states the guarantees the boundary is meant to keep, so a reproducible
  break in any of them is recognisably a vulnerability, along with the limits that are deliberate
  (notably that `APP_ENV=demo` fabricates workspace KPIs).

### Internal

- Coverage floors raised to sit just under measured values (`app/service` 12 → 25,
  `app/queryrunner` 40 → 65, `app/security` 40 → 50, `app/audit` 20 → 45, core total 18 → 35).
- `make test-unit` now runs `app/service`, `app/security`, `app/llm`, `app/audit` and `app/story`,
  which previously executed only inside the CI coverage step.
- Documentation is gated on `mkdocs build --strict`. Enabling `attr_list` fixed twelve dead
  cross-page anchors whose explicit heading ids had been rendering as literal text.
- `make generate` strips goa's seeded OpenAPI examples — roughly 97% of those artifacts. A
  one-field design change went from rewriting 55,441 lines to 14, so the specs are diffable
  again and no longer hidden behind `-diff`.
- **Release artifacts are signed with Sigstore bundles.** `cosign sign-blob` was still being
  called with `--output-signature`/`--output-certificate`, which current cosign deprecates and
  then ignores in favour of `--bundle`; with no bundle path it failed on the first artifact and
  skipped release creation entirely. Each archive, `checksums.txt` and the SBOM now ship a
  `.cosign.bundle` alongside them, and the README documents `cosign verify-blob`.
- **The release pipeline works.** `actions/download-artifact` was pinned to a SHA that does not
  exist upstream, so a tag push built every binary and then died in "Set up job" before
  publishing anything. `Lint` now resolves every pinned action SHA on each pull request —
  including multi-segment paths like `github/codeql-action/init` — failing only on 404/422 so a
  GitHub API outage cannot wedge every open PR.
- **The repository is no longer 94% committed dependencies.** `frontend/node_modules` (11,147
  files) and a stale `frontend/dist` were tracked despite every build running `npm ci`. Tracked
  files went from 11,909 to 742, and a shallow clone from 205 MB to 9.3 MB.
- Documentation is published to GitHub Pages from `main`.

## [2.0.0] - 2026-06-28

Postgres-first repositioning: secure read-only SQL, plan analysis, and analytics at scale, with optional AI narratives.

### Added

- **10M-row benchmark dataset:** Monthly range-partitioned `demo.sales`, reproducible via `make seed-large-docker` ([docs/DATASET.md](docs/DATASET.md)).
- **EXPLAIN JSON API:** `POST /api/v1/queries/explain` with seq-scan detection, cost flags, and index suggestions.
- **Parser-based SQL validation:** `pg_query_go` parse-tree walk in `app/queryrunner/validator.go` (replaces substring blocklist).
- **`pg_stat_statements` dashboard:** Query stats UI and `GET /api/v1/queries/stats`.
- **RLS multi-tenant demo:** Row-level security on `demo.sales` ([docs/ops/rls-demo.md](docs/ops/rls-demo.md)).
- **pgvector semantic search:** HNSW index on saved-query embeddings.
- **SQL period comparison:** `LAG` window functions in `app/queryrunner/period_comparison.go`; Go metrics path is fallback.
- **Case study:** 1.1s → 145ms covering index optimization on 10M rows ([docs/case-studies/01-query-optimization.md](docs/case-studies/01-query-optimization.md)).
- **Production ops docs:** Backup, migrations, monitoring (see deployment/operations reference docs).
- **Multiple database connections:** `GET /api/v1/connections`, plus optional `connection_id` across run/save/list/report/schema/ask flows. Saved queries and reports persist `connection_id`.
- **Structured logging (zerolog):** `LOG_LEVEL` (debug, info, warn, error) and `LOG_PRETTY` for local dev. One message per request (`http request`), with level by status: 4xx/5xx → error; `/health`, `/ready`, `/version` → debug; else info.
- **Configurable metrics windows:** `METRICS_MAX_TIMESERIES_PERIODS` (default 24, range 2–120) caps the periods included in time-series output.

### Changed

- README and public positioning lead with Postgres query intelligence; AI narrative layer is optional.
- `default_transaction_read_only` enforced on the read-only database role (migration 000011).
- Documentation refreshed across configuration, API reference and examples, UI overview, quick start, CLI usage, and troubleshooting to match multi-connection behaviour and current request/response fields.

## [1.0.0]

### Planned (Release 2)

Additional analytics: further cohort metrics, configurable windows, and seasonal adjustments.

### Added
- **Isolation Forest anomaly detection:** `METRICS_ANOMALY_METHOD=isolation_forest` now supported; calculator branches on method and uses Isolation Forest (random trees, median split, anomaly score) when set; z-score remains default
- **Rate limit burst:** Token-bucket limiter; `SECURITY_RATE_LIMIT_BURST` is now used (refill at RPM, cap at burst); new keys start with full bucket
- **Cohort analysis:** When a dimension column name contains `cohort` (e.g. `cohort_month`), metrics calculator aggregates by (cohort, period) and fills `metrics.cohorts` with period values and optional retention %; period column can be text or numeric
- **Cohort in report UI:** Report detail Analytics card shows a Cohorts section (cohort label, retention %, period–value table) when `metrics.cohorts` is present
- **Cohort in HTML/PDF export:** Report export (HTML and PDF) includes a Cohorts section with the same structure
- **E2E test for cohort:** `Generate_Cohorts` subtest in reports E2E verifies cohort-shaped query produces `metrics.cohorts`
- **Docs:** Cohort input shape documented (Configuration – Cohort analysis); UI overview and docs index updated; reference to removed file removed from changelog
- **Period comparison:** Automatic period-over-period (e.g. this month vs last month) when query results have a date/time column and numeric measures; derived % change and trend (up/down/flat); "Vs previous period" block in query results and report UI with optional period labels
- **Run query API:** `period_comparison` array and optional `period_current_label` / `period_previous_label` on run response
- **Reports API:** `metrics.period_current_label` and `metrics.period_previous_label` when time series is present
- **Configurable trend threshold:** `PERIOD_TREND_THRESHOLD_PERCENT` (default 0.5) for when to label change as up/down vs flat
- **Narrative:** LLM prompt rule to include at least one takeaway on period-over-period change when time series metrics exist
- **Metrics:** Support for PostgreSQL `NUMERIC` (pgtype.Numeric) in column profiling and aggregation so measures like `SUM(...)` appear in period comparison
- Groq LLM provider: `LLM_PROVIDER=groq`, `LLM_MODEL`, `LLM_API_KEY` (OpenAI-compatible API; e.g. llama-3.3-70b-versatile)
- OpenAI (GPT) LLM provider: `LLM_PROVIDER=openai`, `LLM_MODEL`, `LLM_API_KEY` (Chat Completions API)
- Claude LLM provider: `LLM_PROVIDER=claude`, `LLM_MODEL`, `LLM_API_KEY` (Anthropic Messages API)
- Gemini LLM provider: `LLM_PROVIDER=gemini`, `LLM_MODEL`, `LLM_API_KEY` for report generation
- MCP server (`cmd/mcp-server`): tools for Claude desktop / Cursor (run query, generate report, list saved/reports); `config/mcp-example.json`, docs
- **MCP schema, context, and query suggestions:** Schema API `GET /api/v1/schema` (queryable tables/columns from `information_schema`); suggestions API `GET /api/v1/suggestions/queries?intent=...&limit=...` (curated examples + saved-query match by intent); MCP tools `get_schema`, `get_context` (schema + saved queries merged), `suggest_queries`, `list_schemas`, `ask_question`, `explain_sql`; `app/catalog`, `app/suggestions`
- **Ask (NL→SQL→report):** `POST /api/v1/suggestions/ask` — natural-language question → generated SQL → run → narrative report in one step
- **Explain SQL:** `POST /api/v1/suggestions/explain` — plain-English explanation of a SQL query (one or two sentences)
- **Semantic search (similar queries):** `GET /api/v1/suggestions/similar?text=...` — embedding-based retrieval of saved queries similar to text; RAG context in report generation when embeddings enabled
- **Embeddings:** `EMBEDDING_BASE_URL`, `EMBEDDING_MODEL`; in-memory or pgvector (migration 000007); powers similar-query and RAG
- **Configurable schemas:** `DATABASE_ALLOWED_SCHEMAS` (default `public,demo`) — comma-separated schemas queries may access; migration 000010 grants readonly access to `public`
- **demo.sales_summary view:** Read-only aggregated view (migration 000009) for schema discovery and queries
- **Health, readiness, metrics, version:** `GET /health` (liveness), `GET /ready` (readiness, DB check), `GET /metrics` (pool stats), `GET /version` (build version)
- **CORS:** `CORS_ORIGINS` for configurable allowed origins
- **Report export:** HTML (`/web/reports/export?id=...`) and PDF (`/web/reports/export/pdf?id=...`) download; auth-protected when `SECURITY_AUTH_ENABLED` true
- **Query Runner UI:** Schema browser (left sidebar, schema→tables→columns, click to insert); query suggestions card (fetches API, click to run); Ctrl+E focus editor, Ctrl+Enter run; session-stored query history (last 10)
- Report UI: show LLM provider and model; improved report card layout and CSS
- PostgreSQL extension for calling PgQueryNarrative from SQL
- CLI tool for Docker-only usage
- API documentation, contributing guidelines, security policy
- Security scanning (secret scan, CodeQL, govulncheck, gosec)
- **Chart suggestions:** By data structure (time series → line/area; category+value → bar/pie; table); suggestion buttons and chart-type dropdown built from API on query page; area chart support; report page shows suggested charts; unit tests (app/charts/suggester_test.go)
- **Advanced metrics:** Richer time-series (last N periods, 3-period moving average); anomaly detection (z-score, configurable threshold); trend analysis (linear regression over last 6 periods, direction and summary); report API (`periods`, `moving_average`, `anomalies`, `trend_summary`); report UI (trend summary, anomalies list, period history table); unit tests (app/metrics/calculator_test.go)
- **Authentication:** Optional API key (Bearer token) for `/api/*` and `/web/reports/export*`; `SECURITY_AUTH_ENABLED`, `SECURITY_API_KEY`; 401 when missing or invalid; `/health` and `/ready` always unauthenticated
- **Rate limiting:** Per-client IP; `SECURITY_RATE_LIMIT_RPM` (0 = disabled), `SECURITY_RATE_LIMIT_BURST`; 429 when exceeded
- **Audit trail:** `app.audit_logs` table and migration; request logging middleware records API_REQUEST (path, status, identity); AUTH_FAILURE and RATE_LIMIT_EXCEEDED on auth/rate-limit events
- **Testing:** Unit tests for auth (ValidateRequest) and ratelimit (NewLimiter, Allow); integration test for audit store (Record + DB); E2E tests for GET /health and GET /ready
- **Versioning and releases:** Versioning and releases doc (`docs/reference/versioning-and-releases.md`); `make build-release` for multi-arch server + MCP binaries and checksums; release workflow builds server and MCP for linux/amd64, darwin/amd64, darwin/arm64

### Changed
- **Documentation:** Reorganized into `docs/api/`, `docs/usage/`; added Period comparison and Metrics section in configuration (`PERIOD_TREND_THRESHOLD_PERCENT`); API examples in `docs/api/examples.md`, CLI usage in `docs/usage/cli-usage.md`; docs index and cross-links updated
- Documentation: single generic LLM setup guide (Ollama, Gemini, Claude, OpenAI, Groq, MCP); docs shortened and standardized
- Go 1.23 → 1.24; PostgreSQL 18 as default (16, 17, 18 supported)
- Docker: postgres:18-alpine, memory limits

### Fixed
- E2E migration: roles created in 000001 so 000003 GRANT succeeds; migration permission errors
- CLI shell and argument passing for Alpine/Docker
- Postgres init script role creation order
- **Narrative number scale:** LLM prompt now formats sample data with comma-separated thousands and instructs the model to preserve exact magnitude when citing metrics (avoids e.g. 848M instead of 84.8M)

### Security
- Secret scanning, dependency vulnerability scanning, CodeQL, gosec
- Optional API authentication (Bearer token), per-IP rate limiting, and audit logging to `app.audit_logs`

[Unreleased]: https://github.com/pgquery-narrative/pgquerynarrative/compare/v2.2.0...HEAD
[1.0.0]: https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v1.0.0
[2.0.0]: https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v2.0.0
[2.1.0]: https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v2.1.0
[2.2.0]: https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v2.2.0
