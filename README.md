<p align="center">
  <img src="docs/assets/logo.png" alt="PgQueryNarrative" width="220">
</p>

<h1 align="center">PgQueryNarrative</h1>

<p align="center">
<strong>PostgreSQL query intelligence that shows its evidence</strong><br>
Investigate expensive queries, compare system-proposed rewrites against plan evidence,<br>
and ship engineering-ready reports.
</p>

<p align="center">
  <a href="https://github.com/pgquery-narrative/pgquerynarrative/actions"><img src="https://img.shields.io/github/actions/workflow/status/pgquery-narrative/pgquerynarrative/ci.yml?branch=main&label=CI" alt="CI"></a>
  <img src="https://img.shields.io/badge/Go-1.26+-00ADD8?logo=go&logoColor=white" alt="Go 1.26+">
  <img src="https://img.shields.io/badge/PostgreSQL-16%2B-336791?logo=postgresql&logoColor=white" alt="PostgreSQL 16+">
  <img src="https://img.shields.io/github/license/pgquery-narrative/pgquerynarrative" alt="License MIT">
  <a href="https://github.com/pgquery-narrative/pgquerynarrative/pkgs/container/pgquerynarrative"><img src="https://img.shields.io/badge/container-ghcr.io-2496ED" alt="Container"></a>
  <a href="https://github.com/pgquery-narrative/pgquerynarrative/releases"><img src="https://img.shields.io/github/v/release/pgquery-narrative/pgquerynarrative?label=release" alt="Latest release"></a>
  <a href=".github/SECURITY.md"><img src="https://img.shields.io/badge/security-policy-blue" alt="Security policy"></a>
</p>

<p align="center">
  <a href="#install"><strong>Install</strong></a> ·
  <a href="#try-it-5-minutes">Try the demo</a> ·
  <a href="docs/getting-started/connect-postgres.md">Connect your Postgres</a> ·
  <a href="docs/operate/deployment.md">Deploy</a> ·
  <a href="https://pgquery-narrative.github.io/pgquerynarrative/">Documentation</a> ·
  <a href=".github/SECURITY.md">Security</a>
</p>

<p align="center">
  <img src="docs/assets/demo-workflow.svg" alt="Query Investigation workflow: EXPLAIN, suggest rewrite, compare, report" width="720">
</p>

<p align="center"><sub>Investigate → system-proposed rewrite → compare with plan evidence → engineering report.</sub></p>

---

## What it is

PgQueryNarrative is a **PostgreSQL investigation workbench**. The flagship loop is:

**expensive query → plan findings → system-proposed rewrite or index candidate → measured compare + result verification → engineering report**

Safe read-only SQL and plan analysis are the core. An optional LLM can narrate
workbench analytics; it is **not** required for investigation reports (those are
evidence templates, not LLM narratives). Start with
[Concepts](docs/concepts.md) for vocabulary (evidence, EXPLAIN vs ANALYZE, what
compare checks).

**Where this fits**

Query-statistics dashboards, regression alerting, plan scoring and automated query
rewriting all have mature tools already, commercial and open source. If that is what
you need, reach for one of them. What this adds is narrow and specific:

> **It proposes a rewrite, then checks that the rows still match before you ship it.**

The rewriter is a rule engine over PostgreSQL's own parser — about a half-dozen
patterns, no model involved — and it **declines more often than it fires**, because
it only transforms what it can show is equivalent. Verification then runs both
queries and compares rows with an order-independent fingerprint. If it cannot verify
equality it says `Unverified`, which is a different answer from `Different` — and
`Unverified` is never treated as a mismatch.

If your slow query is not one of the shapes below, you will get plan findings and no
rewrite. That is the expected outcome, not a failure.

**How it works (honest):**

- Rewrites are **proposed from the query AST and plan findings** (`Suggest rewrite` /
  `Rank candidates`) — demo scenarios ship **problem SQL only**, no answer-key
  rewrite.
- **Coverage is bounded and deliberately conservative.** The patterns are: a
  function wrapping a filtered column (`DATE_TRUNC` / `EXTRACT` / `to_char` /
  `::date` / `COALESCE` over a date), `OR` across columns → `UNION ALL`, `IN` /
  `NOT IN` → `EXISTS`, and `LEFT JOIN … IS NULL` → `NOT EXISTS`. Numeric and text
  casts on a compared column are deliberately **not** rewritten — the rewriter has
  no catalog access to a column's real type, and dropping a cast is only safe when
  it happens to be a no-op for that type.
- **Planner cost is labelled an estimate, never a speed multiple.** Cost is in
  arbitrary units and is not proportional to time; only `EXPLAIN ANALYZE` produces
  a measured duration, and a single run is reported as the single sample it is.
  Pass `timing_runs` (up to 5) for a median plus the observed spread — and if the
  spread is as large as the difference, it says so instead of claiming a speedup.
- Index DDL is **suggested only** (HypoPG when installed; labelled heuristic
  otherwise) — never auto-applied. PgQueryNarrative never automatically applies a
  proposed rewrite, index, or DDL to the analytical target database.
- **Result equivalence** is reported in five states — `VerifiedEqual` (every row of
  both results contributed to a full-result, order-independent fingerprint, and the
  fingerprints matched), `SampleMatch` (full-result fingerprinting could not run and
  a bounded deterministic sample matched — supporting evidence, not full
  verification), `Different`, `Unverified` (the check could not complete — never
  reported as a mismatch), and `NotRequested`. `VerifiedEqual` gates a shippable
  investigation report once a candidate exists; `SampleMatch` additionally requires
  an explicit `accept_sample_match` acknowledgement. The fingerprint compares row
  text and is order-independent, so column types, column names and `ORDER BY` are
  **not** part of what it checks — verify those separately when they matter to your
  query's contract.
- **Regression inbox** is empty on default `make demo` unless real
  `pg_stat_statements` data exists; set `APP_ENV=demo` for seeded demo alerts and KPIs.

## Choose your path

| You want to… | Start here |
|---|---|
| **Install it** | [Install](#install) — container, binary, or source |
| **Try it in ~5 minutes** | [Try it](#try-it-5-minutes) — `make demo` + guided Investigate |
| **Connect your PostgreSQL** | [Connect your Postgres](docs/getting-started/connect-postgres.md) — readonly role + schema allowlist |
| **Deploy** | [Deployment](docs/operate/deployment.md) — Docker / Compose / Kubernetes / Helm |
| **Understand trust & scope** | [Trust model](docs/trust-model.md) — what the app will and will not do |

---

## Install

Three ways in, in order of how quickly you get a running instance.

### Container (recommended)

```bash
docker pull ghcr.io/pgquery-narrative/pgquerynarrative:2.2.0
```

One image carries the API and the built UI. It needs a PostgreSQL to talk to, and on
a fresh database it needs `DATABASE_MIGRATION_USER` / `DATABASE_MIGRATION_PASSWORD`
set to a role that may create extensions and alter roles — the runtime query role
deliberately cannot. See [Deployment](docs/operate/deployment.md) for the full
compose file, Helm chart and Kubernetes manifests.

Images are published with an SBOM and signed with cosign:

```bash
cosign verify ghcr.io/pgquery-narrative/pgquerynarrative:2.2.0 \
  --certificate-identity-regexp 'https://github.com/pgquery-narrative/pgquerynarrative/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### Binary

Download the archive for your platform from the
[latest release](https://github.com/pgquery-narrative/pgquerynarrative/releases/latest)
— `linux-amd64`, `linux-arm64`, `darwin-amd64`, `darwin-arm64` — then:

```bash
tar -xzf pgquerynarrative-2.2.0-linux-amd64.tar.gz
cd pgquerynarrative-2.2.0-linux-amd64
sha256sum -c ../checksums.txt --ignore-missing   # verify first
cp config/pgquerynarrative.env.example .env      # then edit the DATABASE_* values

# Migrations create extensions and ALTER ROLE, so they need a role that may do
# both — not the runtime query role, which deliberately cannot.
./bin/migrate -path app/db/migrations -database "$MIGRATION_DATABASE_URL" up
./bin/pgquerynarrative-server
```

Each archive ships the server, the MCP server, a `migrate` binary, the migrations
themselves, the built UI, and an example config — so a release is self-contained and
does not need this repository.

Every archive, plus `checksums.txt` and the SBOM, is signed. Each has a
`.cosign.bundle` beside it holding the signature and certificate. **Requires cosign
v3 or newer** — these are Sigstore v0.3 bundles, and cosign v2 rejects them with
`bundle does not contain cert for verification`:

```bash
cosign verify-blob pgquerynarrative-2.2.0-linux-amd64.tar.gz \
  --bundle pgquerynarrative-2.2.0-linux-amd64.tar.gz.cosign.bundle \
  --certificate-identity-regexp 'https://github.com/pgquery-narrative/pgquerynarrative/.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

### From source

Requires Go 1.26+ and CGO (`pg_query_go` is a cgo library), plus Node 22 for the UI.

```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
make build            # builds the UI, then the server into bin/
```

> **Upgrading from 2.0.x?** The schema gate moved to version 57. Run migrations
> before starting the new binary — a database behind that version makes `/ready`
> return 503 (see [Migrations, upgrades, backup](docs/operate/upgrades.md)) until it
> catches up. `POST /api/v1/queries/explain` also no longer returns
> `execution_time_ms`; see the [changelog](CHANGELOG.md#210---2026-09-06) for the
> replacement fields.

---

## Try it (5 minutes)

Requires Docker. Starts Postgres + app + small seed (~2 minutes):

```bash
make demo
```

Open **http://localhost:8080**:

1. **Start guided demo** or open **Investigate**
2. Choose **Slow dashboard query**
3. Review the finding (e.g. `DATE_TRUNC` blocking partition pruning)
4. Click **Suggest rewrite** (or **Rank candidates**) — rewrites are system-proposed, not prefilled
5. **Compare plans** with result verification on, and confirm equivalence is **VerifiedEqual**
6. **Generate report**

The demo seeds ~300k rows across 49 monthly partitions — enough that the
before/after difference is real rather than timing noise. For the 10M-row figures in
the [case study](docs/examples/query-optimization.md), run **`make demo-bootstrap`**
first (or `make seed-large-docker` on an existing stack), then repeat from step 2.

```bash
make demo-bootstrap
```

`make demo` also starts **Ollama** and pulls `llama3.2` so **Ask in natural
language** works locally (first run may download the model). Investigation still
works if you skip the LLM.

More detail: [Quick start](docs/getting-started/quickstart.md)

---

## Trust model (short)

- User SQL runs as a **dedicated read-only role**, not the app migration user
- Schemas are **allowlisted** (`DATABASE_ALLOWED_SCHEMAS`, default `demo`)
- **Writes and DDL are blocked** in the query validator
- Nothing PgQueryNarrative proposes — rewrite, index, DDL — is ever applied automatically to the analytical database
- Cloud LLM row egress is **off unless you explicitly enable it**

Full write-up: [Trust model](docs/trust-model.md)

---

## Capabilities

| Area | What you get |
|---|---|
| **Query Investigation** | EXPLAIN findings, system-proposed candidates, compare, result verification, template engineering report |
| **Rewrite engine** | AST-based `Suggest rewrite` (DATE_TRUNC, EXTRACT, COALESCE, OR→UNION ALL, IN→EXISTS, anti-join→NOT EXISTS) |
| **Candidate ranking** | `Rank candidates`: dry-EXPLAIN rewrites + optional HypoPG index projection (heuristic when HypoPG unavailable) |
| **Result verification** | Full-result order-independent fingerprint (falls back to COUNT(*) + a bounded sample) → `VerifiedEqual` / `SampleMatch` / `Different` / `Unverified` / `NotRequested`; reports require one of the first two once a candidate exists |
| **Secure read-only access** | Readonly pool, statement limits, timeouts, schema allowlist |
| **Plan analysis** | Seq-scan / cost / partition-pruning findings; optional `EXPLAIN ANALYZE` when enabled; IndexAdvice DDL (suggest-only) |
| **Regressions & applied fixes** | `pg_stat_statements`-based detection, alert inbox, poller-confirmed fix outcomes |
| **Workbench** | Plan tree, compare table, dashboards, schedules & webhooks, regression inbox, Security & Trust page |
| **Integrations** | REST API, MCP server, PostgreSQL extension, embedded Go client |
| **Scale demo** | Partitioned `demo.sales`; 10M-row seed — [Dataset](docs/examples/dataset.md) |

**Two report types:** **Investigation reports** (evidence template, no LLM) vs
**Workbench LLM reports** (`/reports/generate`, Ask). Optional narratives:
[LLM providers](docs/integrations/llm.md) · library embed:
[Embedded Go](docs/integrations/embedded-go.md)

---

## Commands

| Action | Command |
|---|---|
| **Guided demo** | `make demo` |
| Guided demo + 10M-row seed | `make demo-bootstrap` |
| API smoke after demo | `make demo-smoke` |
| Start / stop stack | `make start-docker` / `make stop` |
| Migrate (Docker) | `make migrate-docker` |
| Seed 10M rows (Docker) | `make seed-large-docker` |
| Local app (Postgres already up) | `make start-local` |
| Build / test | `make build` / `make test` |
| CLI | `make cli CMD='query "SELECT * FROM demo.sales LIMIT 5"'` |

---

## Project structure

| Path | Purpose |
|---|---|
| [`cmd/server`](cmd/server) | API, health/ready, SPA |
| [`cmd/mcp-server`](cmd/mcp-server) | Optional MCP server (query/report tools) |
| [`app/`](app/) | Config, DB, query runner, investigations, LLM, reports |
| [`api/design/`](api/design/) | Goa API design → `api/gen/` and repo-root `gen/` |
| [`frontend/`](frontend/) | React workbench |
| [`web/`](web/) | Report HTML/PDF export handlers |
| [`docs/`](docs/index.md) | Documentation (preview: `make docs`) |
| [`pkg/narrative/`](pkg/narrative/) | Embeddable client |
| [`test/`](test/) | Unit, integration, e2e, Playwright |

---

## Documentation

Preview: **`make docs`** → http://127.0.0.1:8000. Full site:
<https://pgquery-narrative.github.io/pgquerynarrative/>

| Section | Links |
|---|---|
| **Start here** | [Docs overview](docs/index.md) · [Concepts](docs/concepts.md) · [Architecture](docs/architecture.md) · [Trust model](docs/trust-model.md) |
| **Getting started** | [Quick start](docs/getting-started/quickstart.md) · [Installation](docs/getting-started/installation.md) · [Connect Postgres](docs/getting-started/connect-postgres.md) |
| **Core workflows** | [Investigate a slow query](docs/workflows/investigate.md) · [Verify result equivalence](docs/workflows/verify-results.md) · [Regressions and applied fixes](docs/workflows/regressions.md) |
| **Integrations** | [REST API](docs/integrations/rest-api.md) · [MCP server](docs/integrations/mcp.md) · [PostgreSQL extension](docs/integrations/postgres-extension.md) |
| **Security** | [Database roles](docs/security/database-roles.md) · [Query execution safety](docs/security/query-safety.md) |
| **Deploy & operate** | [Deployment](docs/operate/deployment.md) · [Production configuration](docs/operate/production.md) · [Health and monitoring](docs/operate/monitoring.md) |
| **Reference** | [Configuration](docs/reference/configuration.md) · [API](docs/reference/api.md) · [API errors](docs/reference/api-errors.md) |
| **Develop** | [Setup](docs/development/setup.md) · [Repository architecture](docs/development/repository.md) · [Testing](docs/development/testing.md) |
| **Examples** | [Dataset](docs/examples/dataset.md) · [Case study](docs/examples/query-optimization.md) |

**Contributing & security:** [.github/CONTRIBUTING.md](.github/CONTRIBUTING.md) ·
[.github/SECURITY.md](.github/SECURITY.md) · **Changelog:** [CHANGELOG.md](CHANGELOG.md)

## Releases

| Release | Notes |
|---|---|
| **[v2.2.0](https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v2.2.0)** — current | Evidence honesty: planner cost no longer reported as a speed multiple, full-result verification at any size, repeated timing with noise detection. One breaking change to the compare cost row — see [CHANGELOG](CHANGELOG.md#220---2026-09-07) |
| [v2.1.0](https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v2.1.0) | Query Investigation: rewrite proposals, plan-based compare, result verification, regression inbox |
| [v2.0.0](https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v2.0.0) | EXPLAIN analysis, parser-based validation, 10M-row partitioned dataset |
| [v1.0.0](https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v1.0.0) | Analytics narratives over a read-only connection |

Versioning follows [SemVer](https://semver.org/), scoped to the embeddable
`pkg/narrative` client — see the
[stability table](docs/project/releases.md#pkgnarrative-api-stability).
`main` is the development branch; `stable-v2.0.0` and `stable-v1.0.0` preserve the
earlier lines.

## License

MIT. See [LICENSE](LICENSE).
