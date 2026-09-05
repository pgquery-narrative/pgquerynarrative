# Branch protection for `main`

`main` currently has **no branch protection rule**. Every check described below runs on
pull requests, but nothing enforces that they passed before a merge — protection is the
piece that turns a green CI into an actual gate.

This page is the checklist a maintainer applies in GitHub. It is deliberately explicit
about check *names*, because a required check whose name does not match a real job blocks
every merge with "Expected — waiting for status to be reported", and that failure mode is
easy to misread as a broken pipeline.

## Apply it

**Settings → Branches → Add branch protection rule**, branch name pattern `main`.

| Setting | Value | Why |
|---------|-------|-----|
| Require a pull request before merging | on | No direct pushes to `main`. |
| Required approvals | 1 | A second pair of eyes on every change. |
| Dismiss stale pull request approvals when new commits are pushed | on | An approval describes the diff it was given for. |
| Require status checks to pass before merging | on | The actual gate (list below). |
| Require branches to be up to date before merging | on | Forces re-run against the merge target; catches semantic conflicts that merge cleanly. |
| Require linear history | on | Squash-merge only; keeps `main` bisectable. |
| Require conversation resolution before merging | on | No merging over unaddressed review comments. |
| Allow force pushes | off | |
| Allow deletions | off | |
| Do not allow bypassing the above settings | on | Applies the rule to admins too. |

Merge button configuration lives in **Settings → General → Pull Requests**: enable
**Allow squash merging** only, and disable merge commits and rebase merging so
"Require linear history" cannot be violated.

## Required status checks

Names must match exactly. These are the job names as they appear on a pull request:

**CI** (`.github/workflows/ci.yml`)

- `Lint`
- `Test (1.26.6)`
- `Build`
- `Frontend lint and typecheck`
- `Race detector`
- `Go vulnerability scan`
- `DB security verify`
- `Migration up/down/up`
- `Helm StrictMode gates`
- `Schedule multi-replica`
- `Load smoke`
- `Release build smoke`
- `Docker image smoke`

**Security** (`.github/workflows/security.yml`)

- `Supply Chain Baseline`
- `CodeQL Analysis (go)`
- `CodeQL Analysis (javascript-typescript)`
- `Go Security Checker`
- `Secret Scanning`
- `Dependency Review`

**E2E** (`.github/workflows/e2e.yml`)

- `End-to-End Tests`
- `Browser E2E (Playwright)`

**PR hygiene** (`.github/workflows/pr-hygiene.yml`)

- `Require linked issue in PR body`

### Do not require these

- `Scorecard` — reports `NEUTRAL` on pull requests, which never satisfies a required check.
- `Sourcery review` — third-party, skipped on many runs; an outage would block all merges.
- `CodeQL` — the aggregate umbrella check. Require the two per-language
  `CodeQL Analysis (...)` jobs instead; the umbrella can report before the analyses finish.

### The `Test (1.26.6)` trap

`Test` is a matrix job (`go-version: ['1.26.6']` in `ci.yml`), so its check name embeds the
Go version. **Bumping the Go toolchain renames the check**, and the old name stays required
and never reports — every PR blocks until someone updates the protection rule.

When changing `go-version` in `ci.yml`, update the required check name in the same change,
or drop the matrix so the job is plainly named `Test`.

## Verify the rule works

After saving, open a scratch PR that deliberately fails one check (e.g. add an unformatted
Go file) and confirm the merge button is blocked. A rule that exists but lists no valid
required checks looks identical to a working one from the PR page.

## When CI gains a job

New blocking jobs must be added here **and** to the protection rule; a job that runs but is
not required is advisory only. Jobs planned but not yet merged (for example a `Docs` job
running `mkdocs build --strict`) must **not** be added to the rule before they exist on
`main`, for the "waiting for status" reason above.

## Related

- [RELEASING.md](../../RELEASING.md) — the release gate these checks feed
- [Deployment model](../../deploy/README.md)
