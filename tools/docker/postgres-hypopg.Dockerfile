# PostgreSQL with hypopg for planner-backed index cost projection.
#
# hypopg lets the planner cost a hypothetical index without building it, which
# is what separates a real projection from the labeled heuristic fallback in
# app/queryrunner/hypopg.go. No base image ships it, so build from source.
#
# Migration 000050 runs CREATE EXTENSION IF NOT EXISTS hypopg and 000051 grants
# execute rights to the analytical role, so no manual step is needed once the
# binary is present.
ARG POSTGRES_IMAGE=postgres:16-alpine
FROM ${POSTGRES_IMAGE}

ARG HYPOPG_VERSION=1.4.3

# Alpine bases already carry the server headers and PGXS; Debian bases need the
# matching postgresql-server-dev package.
RUN set -eux; \
    if command -v apk >/dev/null 2>&1; then \
        apk add --no-cache --virtual .hypopg-build build-base git; \
    else \
        apt-get update; \
        apt-get install -y --no-install-recommends \
            build-essential git "postgresql-server-dev-${PG_MAJOR}"; \
    fi; \
    git clone --depth 1 --branch "${HYPOPG_VERSION}" \
        https://github.com/HypoPG/hypopg.git /tmp/hypopg; \
    make -C /tmp/hypopg with_llvm=no; \
    make -C /tmp/hypopg with_llvm=no install; \
    rm -rf /tmp/hypopg; \
    if command -v apk >/dev/null 2>&1; then \
        apk del .hypopg-build; \
    else \
        apt-get purge -y --auto-remove \
            build-essential git "postgresql-server-dev-${PG_MAJOR}"; \
        rm -rf /var/lib/apt/lists/*; \
    fi; \
    test -f "$(pg_config --pkglibdir)/hypopg.so"
