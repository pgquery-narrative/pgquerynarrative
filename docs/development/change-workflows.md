# Change workflows

Task-shaped guides for the changes contributors make most often. See
[Repository architecture](repository.md) for the map these refer to.

## Adding or changing an API endpoint

1. Edit `api/design/*.go` — add the `Method`, its `Payload`/`Result`, HTTP verb and
   path, and any new `Error(...)`.
2. `make generate`. Commit the regenerated `api/gen/` and
   `frontend/src/api/schema.gen.ts` alongside the design change — CI's `Lint` job
   fails if they drift.
3. Implement the handler in `app/service/`.
4. Add a unit test (`test/unit/app/service/...`) and, if it touches the database, an
   integration test (`test/integration/...`).
5. Update [API reference](../reference/api.md) (and
   [API errors](../reference/api-errors.md) for a new error code) in the same
   change — `make docs-contract-check` fails on a documented route/error set that
   disagrees with the code.

## Adding configuration

1. Add the field to the right struct in `app/config/config.go`, read with the
   matching `getEnv*` helper and a literal default.
2. If it needs a production restriction, add the check to `Validate()` in
   `app/config/validate.go` and a test in `validate_test.go`.
3. Add it to [Configuration reference](../reference/configuration.md) — same
   variable name, same literal default — in the same change. `docs-contract-check`
   extracts every `getEnv*` call from `config.go` and fails on a mismatch or an
   undocumented variable.
4. If it's user-facing in production, add it to `.env.example` and, if relevant,
   `deploy/helm/pgquerynarrative/values.yaml` / `deploy/kubernetes/configmap.yaml`.

## Adding a migration

1. Add `app/db/migrations/0000N_name.up.sql` and the matching `.down.sql`.
2. If it changes a guarantee the server depends on at boot, bump
   `RequiredMigrationVersion` in `app/db/migrations_check.go` to the new highest
   number.
3. Run `make migrate-cycle-docker` (up → down -all → up) to prove it's reversible —
   CI's `Migration up/down/up` job runs the same check.
4. If it changes grants or RLS policies, re-run
   `make db-security-verify-docker`.

## Adding a rewrite rule

A dedicated guide, because rewrite correctness is a trust boundary:
[Adding a rewrite rule](rewrite-rules.md).

## Adding a plan finding

1. Add the detection to `app/queryrunner/plan_analysis.go`, naming the new
   `node_type` value.
2. Add it to the table on
   [Evidence and status vocabulary — plan findings](../reference/evidence.md#plan-findings)
   and, if it changes user-facing guidance, to
   [Understand plan findings](../workflows/plan-findings.md).
3. Add a unit test with a captured `EXPLAIN` fixture that should (and one that
   should not) trigger it.

## Adding a documentation page

1. Check this site's [nav](https://github.com/pgquery-narrative/pgquerynarrative/blob/main/mkdocs.yml)
   for whether the content belongs on an existing page first — the goal is fewer,
   more complete pages, not maximum page count.
2. If a new page is warranted, add it under the right top-level section in
   `mkdocs.yml`'s `nav`; `make docs-contract-check` fails if a page under `docs/`
   isn't listed there.
3. Verify every claim against the code it describes — this documentation is held to
   the same standard as the code: cite what you can, and don't restate a default or
   a route from memory when you can read it.
4. Run `make docs-check` (strict build: broken links and anchors are errors) before
   committing.

## See also

[Repository architecture](repository.md) · [Testing](testing.md) ·
[Adding a rewrite rule](rewrite-rules.md)
