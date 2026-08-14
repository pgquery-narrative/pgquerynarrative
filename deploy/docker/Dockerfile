# Multi-stage production build for PgQueryNarrative server.
# Build context: repository root (docker compose -f deploy/docker/docker-compose.yml build).

# ---- Builder ----
FROM golang:1.25.13-alpine AS builder
WORKDIR /app

RUN apk add --no-cache git build-base

# Dependencies first for better layer caching
COPY go.mod go.sum ./
RUN go mod download

# Migrate CLI (postgres driver); build before copying app so layer is reusable
RUN CGO_ENABLED=0 go build -tags postgres -ldflags="-s -w" -o /migrate \
	github.com/golang-migrate/migrate/v4/cmd/migrate

# Application
COPY api api/
COPY app app/
COPY cmd cmd/
COPY pkg pkg/
COPY web web/
RUN go install goa.design/goa/v3/cmd/goa@latest
RUN goa gen github.com/pgquerynarrative/pgquerynarrative/api/design
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o bin/server ./cmd/server

# ---- Runtime ----
FROM alpine:3.20
RUN apk add --no-cache ca-certificates postgresql-client && \
    adduser -D -u 1000 pgquerynarrative

WORKDIR /app

# Application binary and migrations
COPY --from=builder /app/bin/server bin/server
COPY --from=builder /migrate bin/migrate
COPY --from=builder /app/app/db/migrations migrations/

# Entrypoint and optional seed
COPY deploy/docker/entrypoint.sh tools/docker/entrypoint.sh
COPY tools/db/seed.sql tools/db/seed.sql
RUN chmod +x tools/docker/entrypoint.sh && \
    chown -R pgquerynarrative:pgquerynarrative /app

ENV PGQUERYNARRATIVE_HOST=0.0.0.0
ENV PGQUERYNARRATIVE_PORT=8080

EXPOSE 8080

USER pgquerynarrative:pgquerynarrative

ENTRYPOINT ["/app/tools/docker/entrypoint.sh"]
