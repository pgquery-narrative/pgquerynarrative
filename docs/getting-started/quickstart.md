# Quick start

Minimal steps to run PgQueryNarrative.

## Prerequisites

- **Docker:** Docker and Docker Compose, or  
- **Local:** PostgreSQL 16+ and Go 1.25+ (see [Installation](installation.md) for full prerequisites).

## Docker

```bash
git clone <repository-url>
cd pgquerynarrative
make start-docker
```

Uses root [docker-compose.yml](../../docker-compose.yml) (PostgreSQL + app). App at **http://localhost:8080**. For production image and Compose: [Deployment](../reference/deployment.md).

For multi-database setups, configure `DATABASE_CONNECTIONS_JSON` and `DATABASE_DEFAULT_CONNECTION_ID` before starting (see [Configuration – Multiple database connections](../configuration.md#multiple-database-connections)).

## Local PostgreSQL

```bash
git clone <repository-url>
cd pgquerynarrative
pg_isready   # ensure Postgres is running
make start-local
```

Requires [Installation](installation.md) steps (setup, generate, build, db-init, migrate, seed) to have been run once.

## Next steps

| Action | Link |
|--------|------|
| **Web UI (React SPA)** | http://localhost:8080 — query editor, saved queries, reports, export |
| **CLI** | `make cli CMD='query "SELECT * FROM demo.sales LIMIT 5"'` — [CLI usage](../usage/cli-usage.md) |
| **API** | [API example](../api/examples.md) |
| **Reports** | Configure an [LLM](llm-setup.md) for narrative generation |

## See also

- [Installation](installation.md) · [LLM setup](llm-setup.md) · [Configuration](../configuration.md) · [Documentation index](../README.md)
