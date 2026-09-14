# Quick start

Get a guided investigation running in a few minutes, with the demo dataset.

## Prerequisites

- **Docker** and Docker Compose (recommended), or
- **Local:** PostgreSQL 16+ and Go 1.26+ — see [Installation](installation.md)

## Guided demo

```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
make demo
```

`make demo` starts Postgres (with HypoPG), runs migrations, seeds **300,000 rows**
across 49 monthly partitions of `demo.sales`, starts Ollama and pulls `llama3.2` (so
Ask works locally), and starts the app — a couple of minutes total.

Open **http://localhost:8080**:

1. Click **Start guided demo**, or open **Investigate**
2. Choose **Slow dashboard query**
3. Review the plan findings (e.g. a function-wrapped date blocking partition pruning)
4. Click **Suggest rewrite** (or **Rank candidates**) — the rewrite is proposed by the
   AST engine; guided scenarios ship problem SQL only, never a prefilled answer
5. Click **Compare plans** with result verification on, and confirm equivalence is
   **VerifiedEqual**
6. Click **Generate report**

Next: [Understand plan findings](../workflows/plan-findings.md) explains what each
finding means; [Concepts](../concepts.md) gives the full vocabulary.

## Larger dataset

The default seed (300k rows) is enough for a real before/after; for the 10M-row
partition-pruning story used in the [case study](../examples/query-optimization.md), run:

```bash
make demo-bootstrap
```

(or `make seed-large-docker` against an already-running stack), then repeat from
step 2. Optional API smoke test after the stack is up: `make demo-smoke`.

## Other ways to run

=== "Docker Compose"

    ```bash
    make start-docker
    ```

    App: **http://localhost:8080**. Production-oriented images:
    [Deployment](../operate/deployment.md).

=== "Local app + existing Postgres"

    ```bash
    pg_isready
    make start-local
    ```

    Requires the one-time [Installation](installation.md) steps (setup, generate,
    build, db-init, migrate, seed).

## Connect your own database

Leaving the `demo` schema: [Connect your PostgreSQL](connect-postgres.md) and
[Trust model](../trust-model.md).

## Next steps

| Action | Link |
|---|---|
| Understand the loop | [Concepts](../concepts.md) |
| Investigate a real query | [Investigate a slow query](../workflows/investigate.md) |
| UI map | [UI overview](../workbench/ui-overview.md) |
| API flow | [REST API](../integrations/rest-api.md) |
| Optional narratives | [LLM providers](../integrations/llm.md) |
| Deploy | [Deployment](../operate/deployment.md) |
