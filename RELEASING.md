# Releasing

This is the **gate**: what must be true before a version tag is pushed. For the mechanics
of versioning, changelog, and what CI does on a tag, see
[docs/reference/versioning-and-releases.md](docs/reference/versioning-and-releases.md).

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
| 10 | Service coverage floor, hero-path tests, HypoPG gate | #19, #20, #24 | **Open** |
| 11 | Docs-strict CI, mkdocs config, codegen stabilization | #21, #22 | **Open** |
| 12 | Single deployment model, branch protection, this file | #23, #18, #25 | In review |

**A tag requires every row above to read "Merged".**

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
   go test ./app/... ./pkg/...
   ```

4. **Integration tests** (Docker required)

   ```bash
   make test-integration
   ```

5. **Migrations are reversible**

   ```bash
   make migrate-cycle-docker
   ```

   Also confirm `db.RequiredMigrationVersion` equals the highest migration number — a new
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

8. **CI is green on the exact commit being tagged** — every check in
   [branch protection](docs/ops/branch-protection.md), not merely the required subset.

9. **Image builds and serves the UI**

   ```bash
   docker build -t pgquerynarrative:rc .
   ```

   The image must contain `/app/frontend/dist`; a release image that serves an empty UI is
   the failure the [single deployment model](deploy/README.md) exists to prevent.

10. **Changelog** — move `changelog/unreleased.md` entries into
    `changelog/released/<version>.md`, run `make changelog`, commit.

## Tag and publish

```bash
git tag -s v<version> -m "Release v<version>"
git push origin main && git push origin v<version>
```

The Release workflow builds binaries, publishes the GitHub Release, and builds, pushes, and
cosigns `ghcr.io/pgquery-narrative/pgquerynarrative:<version>` from the root `Dockerfile`.

## After the tag

- Verify the published image runs: `docker run --rm ghcr.io/pgquery-narrative/pgquerynarrative:<version> --help`
- Verify the signature: `cosign verify ghcr.io/pgquery-narrative/pgquerynarrative:<version> ...`
- Confirm the GitHub Release lists binaries and `checksums.txt`.

## If a release must be pulled

Do not delete the tag. Cut a new patch version with the fix and mark the bad release as
deprecated in the GitHub Release notes — consumers may already have pulled the image
digest, and a deleted tag makes their build unreproducible rather than merely outdated.
