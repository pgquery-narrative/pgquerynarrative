FROM node:22.12-alpine AS frontend-build
WORKDIR /frontend
COPY frontend/package.json frontend/package-lock.json* ./
RUN npm install --silent
COPY frontend/ .
RUN npm run build

FROM golang:1.24.4-alpine

WORKDIR /app

RUN apk add --no-cache git postgresql-client wget build-base && \
    adduser -D -u 1000 appuser

COPY go.mod go.sum ./
RUN go mod download

COPY . .
COPY --from=frontend-build /frontend/dist ./frontend/dist

RUN go install goa.design/goa/v3/cmd/goa@v3.24.1
RUN goa gen github.com/pgquerynarrative/pgquerynarrative/api/design
RUN CGO_ENABLED=1 go build -ldflags="-s -w" -o bin/server ./cmd/server
RUN go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.19.1 && \
    cp /go/bin/migrate /app/bin/migrate

ENV PGQUERYNARRATIVE_HOST=0.0.0.0
ENV PGQUERYNARRATIVE_PORT=8080

COPY tools/docker/entrypoint.sh /app/tools/docker/entrypoint.sh
RUN chmod +x /app/tools/docker/entrypoint.sh && \
    chown -R appuser:appuser /app

EXPOSE 8080

USER appuser:appuser

ENTRYPOINT ["/app/tools/docker/entrypoint.sh"]
