FROM node:22.12-alpine AS frontend-build
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm ci --silent
COPY frontend/ .
RUN npm run build

FROM golang:1.26.6-alpine AS go-build
WORKDIR /app
RUN apk add --no-cache git build-base
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=frontend-build /frontend/dist ./frontend/dist
RUN go install goa.design/goa/v3/cmd/goa@v3.24.1
RUN goa gen github.com/pgquerynarrative/pgquerynarrative/api/design
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o /out/server ./cmd/server
RUN CGO_ENABLED=0 go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1

FROM alpine:3.21
RUN apk add --no-cache ca-certificates wget postgresql-client && \
    adduser -D -u 1000 appuser
WORKDIR /app
COPY --from=go-build /out/server /app/bin/server
COPY --from=go-build /go/bin/migrate /app/bin/migrate
COPY --from=frontend-build /frontend/dist /app/frontend/dist
COPY app/db/migrations /app/app/db/migrations
# Optional seed data, used only when PGQUERYNARRATIVE_SEED=true (see entrypoint).
COPY tools/db/seed.sql /app/tools/db/seed.sql
COPY tools/docker/entrypoint.sh /app/tools/docker/entrypoint.sh
RUN chmod +x /app/tools/docker/entrypoint.sh && chown -R appuser:appuser /app

ENV PGQUERYNARRATIVE_HOST=0.0.0.0
ENV PGQUERYNARRATIVE_PORT=8080

EXPOSE 8080
USER appuser:appuser
ENTRYPOINT ["/app/tools/docker/entrypoint.sh"]
