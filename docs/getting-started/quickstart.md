# Quick start

Get a guided **Query Investigation** running in minutes.

## Prerequisites

- **Docker** and Docker Compose (recommended), or  
- **Local:** PostgreSQL 16+ and Go 1.25+ — see [Installation](installation.md)

## Guided demo (recommended)

```bash
git clone https://github.com/pgquery-narrative/pgquerynarrative.git
cd pgquerynarrative
make demo
```

Open **http://localhost:8080**:

1. Click **Start guided demo** or open **Investigate**
2. Choose **Slow dashboard query**
3. Review plan findings (e.g. function-wrapped date → blocked partition pruning)
4. Click **Compare plans** on the range-predicate rewrite
5. Click **Generate report**

Vocabulary: [Concepts](../concepts.md). Scene timing: [Demo runbook](../DEMO_RUNBOOK.md).

For large-seed partition counts (≈10M rows, 50→1 style proof):

```bash
make demo-bootstrap
```

## Other ways to run

=== "Docker Compose"

    ```bash
    make start-docker
    ```

    App: **http://localhost:8080**. Production-oriented images: [Deployment](../reference/deployment.md).

=== "Local app + existing Postgres"

    ```bash
    pg_isready
    make start-local
    ```

    Requires [Installation](installation.md) (setup, generate, build, db-init, migrate, seed) once.

## Connect your own database

Leaving the `demo` schema: [Connect your PostgreSQL](connect-postgres.md) and [Trust model](../trust-model.md).

## Next steps

| Action | Link |
|--------|------|
| Understand the loop | [Concepts](../concepts.md) |
| UI map | [UI overview](../ui-overview.md) |
| API investigation flow | [API examples](../api/examples.md) |
| Optional narratives | [LLM setup](llm-setup.md) |
| Production | [Production — start here](../ops/PRODUCTION.md#start-here) |

## See also

[Installation](installation.md) · [Configuration](../configuration.md) · [Docs overview](../index.md)
