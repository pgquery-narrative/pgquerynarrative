-- pg_stat_statements (B9): requires shared_preload_libraries=pg_stat_statements and a Postgres restart.
-- See docker-compose.yml and docs/reference/operations.md.

CREATE EXTENSION IF NOT EXISTS pg_stat_statements;

GRANT pg_read_all_stats TO pgquerynarrative_readonly;
