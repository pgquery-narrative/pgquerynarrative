# PostgreSQL with the pqn extension installed and ready.
#
# Run it and pqn is created in $POSTGRES_DB on first start. Nothing to install and nothing to
# configure: pg_stat_statements is preloaded, and the extension's roles and ledger are made by
# tools/docker/pqn-init.sh. Build from the repository root (make build-pqn-image):
#
#   docker build -f tools/docker/postgres-pqn.Dockerfile -t pqn-postgres:18 .
#   docker run -d -p 127.0.0.1:5432:5432 -e POSTGRES_PASSWORD=secret -e POSTGRES_DB=app pqn-postgres:18
#
# The init script runs only when the data directory is new. For an existing database, connect
# as a superuser and run: CREATE EXTENSION pqn; SELECT pqn_api.init();
# Pinned by digest (the multi-arch index) so a build cannot change under us; update it with the tag.
ARG POSTGRES_IMAGE=postgres:18@sha256:86c951e05bf56c93d95d397747fb8820ac76cc3bedb78f43abd83eedbe3666ae
FROM ${POSTGRES_IMAGE}

COPY infra/pqn-extension/pqn.control infra/pqn-extension/pqn--1.0.sql infra/pqn-extension/pqn--1.0--1.1.sql /tmp/pqn/
RUN set -eux; \
    cp /tmp/pqn/pqn.control /tmp/pqn/pqn--*.sql "$(pg_config --sharedir)/extension/"; \
    rm -rf /tmp/pqn; \
    sample="$(pg_config --sharedir)/postgresql.conf.sample"; \
    sed -i "s/^#\?shared_preload_libraries = .*/shared_preload_libraries = 'pg_stat_statements'/" "$sample"; \
    grep -q "^shared_preload_libraries = 'pg_stat_statements'" "$sample"

COPY tools/docker/pqn-init.sh /docker-entrypoint-initdb.d/10-pqn.sh
