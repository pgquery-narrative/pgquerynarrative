# Releasing

This is the **gate**: what must be true before a version tag is pushed. For the mechanics
of versioning, changelog, and what CI does on a tag, see
[docs/project/releases.md](docs/project/releases.md).

## The rule

> **Do not tag a release while any P0 or P1 remediation item is open.**

The external review of 2026-09 catalogued 25 findings; the P0/P1 subset are correctness and
authorization defects, plus the "confidence signals overstate what was verified" class
(equivalence status, EXPLAIN timing, regression alerts). Shipping a version while those are
open puts a number on behaviour the tool itself reports incorrectly.

## Remediation status

Items map to the 12-PR remediation plan. Update this table as PRs land.

| PR | Scope | Review items | Status |
|----|-------|--------------|--------|
| 1 | Misaligned `DATE_TRUNC` equality, NULL-safe `OR`→`UNION` | #1, #2 | Merged (#138) |
| 2 | Bind-substitution injection hardening | #4 | Merged (#139) |
| 3 | Compare authz + 5-state equivalence | #3, #5, #6 | Merged (#140) |
| 4 | Honest timing fields, real "Rows scanned" | #7, #13 | Merged (#141) |
| 5 | Estimate-only EXPLAIN, honest ranking | #8, #14 | Merged (#142) |
| 6 | Regression detection on interval deltas | #9 | Merged (#151) |
| 7 | Per-connection polling, leader election, unique alert | #10, #11, #12 | Merged (#152) |
| 8 | Cross-org integrity, org-wide visibility | #15, #16 | Merged (#153) |
| 9 | Security & Trust reports real per-connection state | #17 | Merged (#155) |
| 10 | Service coverage floor, hero-path tests, HypoPG gate | #19, #20, #24 | Merged (#161) |
| 11 | Docs-strict CI, mkdocs config, codegen stabilization | #21, #22 | Merged (#163) |
| 12 | Single deployment model, branch protection, this file | #23, #18, #25 | Merged (#157) |

**A tag requires every row above to read "Merged".** As of 2026-09-06 every row does. This gate
did not exist before the 2026-09 review, so it constrains `v2.1.0` onward (`v1.0.0` and `v2.0.0`
predate it); every release since has satisfied it. Leave this table in place: it is the record
of which review items a given version actually contains, and the next review will add rows
rather than replace them.

## Pre-tag checklist

Run from a clean tree on an up-to-date `main`.

1. **Working tree is clean and on `main`**

   ```bash
   git checkout main && git pull --ff-only && git status --porcelain
   ```

   Any output means stop.

2. **Generated code matches the design**

   ```bash
   make generate && git diff --exit-code api/gen frontend/src/api/schema.gen.ts
   ```

3. **Build, vet, format, unit tests**

   ```bash
   go build ./... && go vet ./... && gofmt -s -l .
   make test-unit   # not a bare `go test ./app/... ./pkg/...`, see docs/development/testing.md
   ```

4. **Integration tests** (Docker required)

   ```bash
   make test-integration
   ```

5. **Migrations are reversible**

   ```bash
   make migrate-cycle-docker
   ```

   Also confirm `db.RequiredMigrationVersion` equals the highest migration number: a new
   migration that does not bump it is not required at startup, and the roundtrip test's
   tip-version assertion will fail.

6. **Read-only boundary holds**

   ```bash
   make db-security-verify-docker
   ```

7. **Frontend**

   ```bash
   cd frontend && npm ci && npm run test && npm run build
   ```

8. **CI is green on the exact commit being tagged**, every check in
   [branch protection](docs/project/branch-protection.md), not merely the required subset.

9. **Image builds and serves the UI**

   ```bash
   docker build -t pgquerynarrative:rc .
   ```

   The image must contain `/app/frontend/dist`; a release image that serves an empty UI is
   the failure the [single deployment model](deploy/README.md) exists to prevent.

10. **Changelog**: move `changelog/unreleased.md` entries into
    `changelog/released/<version>.md`, run `make changelog`, commit.

    `CHANGELOG.md` is **generated**: `tools/changelog/build.sh` concatenates
    `changelog/unreleased.md` and `changelog/released/*.md` and overwrites the file. Anything
    hand-written directly into `CHANGELOG.md` is destroyed by the next `make changelog`. The
    `v2.0.0` section was added by hand and had no `changelog/released/2.0.0.md` behind it, so
    it survived only until someone ran the target; it has since been backfilled. Write the
    per-version file, never the output.

## Choosing the number

SemVer here is scoped by [the stability table](docs/project/releases.md#pkgnarrative-api-stability):
`pkg/narrative` is the stable surface, and Goa types under `api/gen/` are explicitly unstable.
Decide the bump against that table, not against the raw diff.

Check what actually changed before picking a number:

```bash
# Public surface of the embeddable client
git diff v<previous>..main -- pkg/narrative/

# Fields removed from any API type: the usual source of an unplanned break
git show v<previous>:api/gen/queries/service.go > /tmp/old.go
diff <(awk '/^type [A-Za-z]+ struct/{t=$2} /^\t[A-Z]/{print t"."$1}' /tmp/old.go | sort -u) \
     <(awk '/^type [A-Za-z]+ struct/{t=$2} /^\t[A-Z]/{print t"."$1}' api/gen/queries/service.go | sort -u)
```

A field removed from a REST response is a break for anyone generating clients from the
published OpenAPI spec even when `pkg/narrative` is untouched. That does not by itself force a
major bump under the table above, but it **must** appear under a `### Breaking` heading in the
changelog and in the release notes. `v2.1.0` removed `ExplainQueryResult.execution_time_ms` on
exactly these terms.

## Upgrade notes belong in the release

`RequiredMigrationVersion` in `app/db/migrations_check.go` is a **readiness** gate, not a
startup gate: a server whose database is behind that number still starts and accepts
connections, but `GET /ready` returns 503 until migrations catch up (see
`docs/operate/upgrades.md`). Whenever the required version moves, the release notes must say
so and name the range, or operators discover it as a failed rollout only once something checks
readiness. `v2.2.0` requires schema version 57, up from 19 at `v2.0.0`.

## Tag and publish

```bash
git tag -s v<version> -m "Release v<version>"
git push origin main && git push origin v<version>
```

The Release workflow builds binaries, publishes the GitHub Release, and builds, pushes, and
cosigns `ghcr.io/pgquery-narrative/pgquerynarrative:<version>` from the root `Dockerfile`.

## After the tag

- Verify the published image serves the UI: run it against a migrated database (or the
  release-smoke Postgres compose the CI job uses) and check `curl -f http://localhost:8080/health`
  and that `/` returns the SPA. The entrypoint waits for Postgres and execs the server, so
  `docker run ... --help` does not work (the entrypoint ignores arguments).
- Verify the signature: `cosign verify ghcr.io/pgquery-narrative/pgquerynarrative:<version> ...`
- Confirm the GitHub Release lists binaries and `checksums.txt`.

## If a release must be pulled

Do not delete the tag. Cut a new patch version with the fix and mark the bad release as
deprecated in the GitHub Release notes: consumers may already have pulled the image
digest, and a deleted tag makes their build unreproducible rather than merely outdated.
