.PHONY: setup tidy generate build build-mcp run test test-unit test-features test-integration test-e2e test-playwright test-playwright-oidc test-load-smoke test-frontend lint fmt migrate migrate-docker migrate-cycle-docker db-security-verify-docker seed seed-large seed-large-docker seed-nyc seed-nyc-docker postgres-up postgres-recreate dev dev-stop dev-watch dev-build dev-teardown docker-up docker-down docker-logs db-init db-init-docker start start-docker start-local stop cli cli-shell changelog build-release linkedin-social-help linkedin-social-draft pilot-acceptance pilot-report

GO ?= go
GOLANGCI_LINT ?= golangci-lint
GOA ?= goa
GOA_VERSION ?= v3.24.1
# Use a user-writable module cache to avoid permission issues with system GOMODCACHE (e.g. root-owned ~/go/pkg/mod).
GOMODCACHE ?= $(HOME)/.gomodcache
export GOMODCACHE

DB_URL ?= postgres://pgquerynarrative_app:pgquerynarrative_app@localhost:5432/pgquerynarrative?sslmode=disable

# Row count for `make seed-large` / `make seed-large-docker`. Override: ROWS=5000000
ROWS ?= 10000000

# NYC TLC months for `make seed-nyc` / `make seed-nyc-docker`. Override: MONTHS=2024-01
MONTHS ?= 2024-01,2024-02,2024-03
NYC_VENV ?= ./tools/db/.venv-nyc
# Superuser URL for COPY into opendata (Docker publishes 5432).
NYC_DB_URL ?= postgres://postgres:postgres@localhost:5432/pgquerynarrative?sslmode=disable

# Docker-internal URL for migrate-docker (golang container on the compose network).
DOCKER_MIGRATE_DB_URL ?= postgres://postgres:postgres@postgres:5432/pgquerynarrative?sslmode=disable

# ============================================================================
# SIMPLIFIED COMMANDS - Start here!
# ============================================================================

# Start: choose Docker or local PostgreSQL explicitly
start:
	@echo "Choose how to run PgQueryNarrative:"
	@echo ""
	@echo "  make start-docker   Use Docker (PostgreSQL + app in containers)"
	@echo "  make start-local    Use local PostgreSQL (app runs on host)"
	@echo ""
	@echo "Then open http://localhost:8080"
	@exit 1

# Start with Docker (PostgreSQL + app in containers)
start-docker:
	@$(MAKE) docker-start

# Start with local PostgreSQL (app runs on host; requires Postgres already running)
start-local:
	@$(MAKE) local-start

# Stop everything
stop:
	@echo "🛑 Stopping PgQueryNarrative..."
	@if docker ps | grep -q pgquerynarrative; then \
		docker compose down; \
	else \
		echo "No Docker containers running"; \
	fi
	@echo "✅ Stopped"

# ============================================================================
# Docker-based startup
# ============================================================================

docker-start:
	@echo "📦 Setting up with Docker..."
	@echo ""
	@echo "Step 1: Starting PostgreSQL..."
	@docker compose up -d postgres || (echo "❌ Failed to start PostgreSQL. Is Docker running?" && exit 1)
	@echo "⏳ Waiting for PostgreSQL to be ready..."
	@timeout=60; \
	while [ $$timeout -gt 0 ]; do \
		if docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; then \
			echo "✅ PostgreSQL is ready!"; \
			break; \
		fi; \
		sleep 1; \
		timeout=$$((timeout - 1)); \
	done; \
	if [ $$timeout -eq 0 ]; then \
		echo "❌ PostgreSQL failed to start"; \
		exit 1; \
	fi
	@echo ""
	@echo "Step 2: Setting up database..."
	@$(MAKE) db-init || true
	@echo ""
	@echo "Step 3: Running migrations..."
	@docker compose run --rm --entrypoint /app/bin/migrate app -path /app/app/db/migrations -database "postgres://postgres:postgres@postgres:5432/pgquerynarrative?sslmode=disable" up
	@docker compose exec -T postgres psql -U postgres -d pgquerynarrative -c "ALTER ROLE pgquerynarrative_readonly SET default_transaction_read_only = on;" 2>/dev/null || true
	@echo ""
	@echo "Step 4: Seeding demo data..."
	@docker compose exec -T postgres psql -U postgres -d pgquerynarrative -f - < tools/db/seed.sql || echo "⚠️  Seed data already exists or database not accessible"
	@echo ""
	@echo "Step 5: Building application image (Docker; avoids host CGO/macOS SDK issues)..."
	@docker compose build app
	@echo ""
	@echo "Step 6: Starting application..."
	@echo "✅ PgQueryNarrative is starting!"
	@echo ""
	@echo "🌐 Open http://localhost:8080 (React UI + API)"
	@echo "📊 API: curl http://localhost:8080/api/v1/queries/saved"
	@echo ""
	@echo "Press Ctrl+C to stop"
	@echo ""
	@docker compose up app

# ============================================================================
# Local PostgreSQL startup (no Docker)
# ============================================================================

# URL for local PostgreSQL (app user). Set DATABASE_URL or DB_URL to override.
LOCAL_DB_URL ?= postgres://pgquerynarrative_app:pgquerynarrative_app@localhost:5432/pgquerynarrative?sslmode=disable

local-start:
	@echo "💻 Setting up with local PostgreSQL..."
	@echo ""
	@echo "Step 1: Checking PostgreSQL connection..."
	@pg_isready -h localhost -p 5432 >/dev/null 2>&1 || (echo "❌ PostgreSQL not running. Start it with: brew services start postgresql@18" && exit 1)
	@echo "✅ PostgreSQL is ready!"
	@echo ""
	@echo "Step 2: Setting up database..."
	@$(MAKE) db-init || true
	@echo ""
	@echo "Step 3: Running migrations..."
	@DB_URL="$${DB_URL:-$${DATABASE_URL:-$(LOCAL_DB_URL)}}"; $(MAKE) migrate
	@echo ""
	@echo "Step 4: Seeding demo data..."
	@DB_URL="$${DB_URL:-$${DATABASE_URL:-$(LOCAL_DB_URL)}}"; $(MAKE) seed
	@echo ""
	@echo "Step 5: Building application..."
	@$(MAKE) generate build
	@echo ""
	@echo "Step 6: Starting application..."
	@echo "✅ PgQueryNarrative is starting!"
	@echo ""
	@echo "🌐 Open http://localhost:8080 (React UI + API)"
	@echo "📊 API: curl http://localhost:8080/api/v1/queries/saved"
	@echo ""
	@echo "Press Ctrl+C to stop"
	@echo ""
	@$(MAKE) run

# ============================================================================
# Development commands
# ============================================================================

setup:
	@echo "📥 Installing dependencies..."
	$(GO) mod download
	$(GO) mod tidy
	@echo "✅ Dependencies installed"

tidy:
	@mkdir -p "$(GOMODCACHE)"
	$(GO) mod tidy

# Generate: goa -> gen/ (ephemeral), then patch and sync to api/gen/ (committed). App imports only api/gen/.
generate:
	@echo "🔧 Generating API code..."
	@export PATH="$$($(GO) env GOPATH)/bin:$$PATH"; \
	if ! command -v goa >/dev/null 2>&1; then \
		echo "Installing Goa..."; \
		$(GO) install goa.design/goa/v3/cmd/goa@$(GOA_VERSION); \
	fi && \
	$(GO) generate ./... && \
	goa gen github.com/pgquerynarrative/pgquerynarrative/api/design
	@sh ./tools/fix-gen-metrics-validator.sh
	@sh ./tools/copy-gen-to-api-gen.sh
	@echo "✅ Code generated"

build-frontend:
	@echo "🔨 Building frontend..."
	@cd frontend && npm ci && npm run build
	@echo "✅ Frontend built: frontend/dist/"

# Server version ldflags (set VERSION for release build).
SERVER_LDFLAGS :=
ifneq ($(VERSION),)
  SERVER_LDFLAGS = -ldflags "-X main.Version=$(VERSION)"
endif
build:
	@echo "🔨 Building application..."
	@$(MAKE) build-frontend
	$(GO) build $(SERVER_LDFLAGS) -o bin/server ./cmd/server
	@echo "✅ Build complete: bin/server"

# Build MCP server for Claude / Cursor (stdio transport). Set VERSION for ldflags (e.g. VERSION=1.0.0 make build-mcp).
MCP_LDFLAGS :=
ifneq ($(VERSION),)
  MCP_LDFLAGS = -ldflags "-X main.Version=$(VERSION)"
endif
build-mcp:
	$(GO) build $(MCP_LDFLAGS) -o bin/mcp-server ./cmd/mcp-server
	@echo "✅ MCP server: bin/mcp-server"

# Release build: native server + MCP + migrate binaries and checksums.
# Multi-arch release assets are built in .github/workflows/release.yml (CGO per platform).
VERSION ?=
build-release:
	@mkdir -p bin
	@native_os=$$($(GO) env GOOS); native_arch=$$($(GO) env GOARCH); \
	echo "Building release binaries for $$native_os/$$native_arch..."; \
	CGO_ENABLED=1 $(GO) build $(SERVER_LDFLAGS) -o bin/pgquerynarrative-server-$$native_os-$$native_arch ./cmd/server; \
	CGO_ENABLED=0 $(GO) build $(MCP_LDFLAGS) -o bin/pgquerynarrative-mcp-$$native_os-$$native_arch ./cmd/mcp-server; \
	CGO_ENABLED=0 $(GO) build -tags postgres -ldflags "-s -w" -o bin/pgquerynarrative-migrate-$$native_os-$$native_arch \
		github.com/golang-migrate/migrate/v4/cmd/migrate; \
	(cd bin && sha256sum pgquerynarrative-* > checksums.txt)
	@echo "✅ Release binaries (server, mcp, migrate) in bin/ (VERSION=$(VERSION))"

run:
	$(GO) run ./cmd/server

# ============================================================================
# Testing
# ============================================================================

test: test-unit test-integration

test-unit:
	@echo "🧪 Running unit tests..."
	$(GO) test ./test/unit/... ./app/queryrunner/... ./cmd/server/... ./pkg/narrative/... ./app/embedding/... ./app/config/... -v

# No-op target so "make test-unit # comment" does not fail when shell passes # as a target.
\#:
	@true

# Alias for test-unit (same scope: unit + cmd/server). Use "make test-unit -run <TestName>" for a single test.
test-features: test-unit

test-integration:
	@echo "🧪 Running integration tests..."
	DOCKER_API_VERSION=1.44 $(GO) test ./test/integration/... -v

test-e2e:
	@echo "🧪 Running E2E tests..."
	DOCKER_API_VERSION=1.44 $(GO) test ./test/e2e/... -v

test-playwright:
	@chmod +x ./tools/e2e/run-playwright.sh
	@PLAYWRIGHT_OIDC=0 ./tools/e2e/run-playwright.sh

test-playwright-oidc:
	@chmod +x ./tools/e2e/run-playwright.sh
	@PLAYWRIGHT_OIDC=1 ./tools/e2e/run-playwright.sh

test-load-smoke:
	@chmod +x ./test/load/smoke.sh
	@./test/load/smoke.sh

test-frontend:
	@echo "🧪 Running frontend unit tests..."
	cd frontend && npm ci && npm test

pilot-report:
	@chmod +x ./tools/ops/pilot_report.sh
	@./tools/ops/pilot_report.sh

# ============================================================================
# Code quality
# ============================================================================

lint:
	@if command -v $(GOLANGCI_LINT) >/dev/null 2>&1; then \
		$(GOLANGCI_LINT) run; \
	else \
		echo "⚠️  golangci-lint not installed. Install with: go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest"; \
	fi

fmt:
	@echo "🎨 Formatting code..."
	$(GO) fmt ./...
	@echo "✅ Code formatted"

# Internal pilot acceptance: migrations, readonly checks, integration suite, build + HTTP smoke.
pilot-acceptance:
	@chmod +x ./tools/ops/pilot_acceptance.sh
	@DOCKER_API_VERSION=1.44 ./tools/ops/pilot_acceptance.sh

# ============================================================================
# Database operations
# ============================================================================

migrate:
	@DB_URL="$${DB_URL:-$${DATABASE_URL:-$(LOCAL_DB_URL)}}"; \
	if [ -z "$$DB_URL" ]; then \
		echo "❌ DB_URL or DATABASE_URL not set. Using default..."; \
		DB_URL="$(LOCAL_DB_URL)"; \
	fi; \
	sh ./tools/db/migrate.sh up "$$DB_URL"

# Fix dirty migration state: make migrate-force VERSION=6 then make migrate
migrate-force:
	@DB_URL="$${DB_URL:-$${DATABASE_URL:-$(LOCAL_DB_URL)}}"; \
	if [ -z "$$DB_URL" ]; then DB_URL="$(LOCAL_DB_URL)"; fi; \
	sh ./tools/db/migrate.sh force "$(VERSION)" "$$DB_URL"

# Start the Postgres container and wait until it accepts connections.
# pg_stat_statements uses shared_preload_libraries in docker-compose.yml; that setting
# only applies on server start. After changing Postgres command/config, run:
#   make postgres-recreate && make migrate-docker
postgres-up:
	@docker compose up -d postgres
	@echo "⏳ Waiting for PostgreSQL..."
	@timeout=90; \
	while [ $$timeout -gt 0 ]; do \
		if docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; then \
			stable=1; \
			for _ in 1 2 3; do \
				sleep 1; \
				docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1 || stable=0; \
			done; \
			if [ $$stable -eq 1 ]; then \
				echo "✅ PostgreSQL is ready"; \
				break; \
			fi; \
		fi; \
		sleep 1; \
		timeout=$$((timeout - 1)); \
	done; \
	if [ $$timeout -eq 0 ]; then echo "❌ PostgreSQL failed to start"; exit 1; fi

# Recreate Postgres so shared_preload_libraries (pg_stat_statements) takes effect.
postgres-recreate:
	@echo "♻️  Recreating PostgreSQL container (required after shared_preload_libraries change)..."
	@docker compose up -d --force-recreate postgres
	@$(MAKE) postgres-up

# Run migrations without host Go/psql — uses a one-off golang container on the compose network.
# Prerequisite: docker compose up -d postgres (or: make postgres-up)
migrate-docker: postgres-up
	@$(MAKE) db-init-docker || true
	@echo "📦 Running migrations via Docker (no host Go required)..."
	@docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1 || \
		(echo "❌ Postgres not ready. Run: make postgres-up" && exit 1)
	@docker run --rm -v "$(CURDIR):/app" -w /app --network pgquerynarrative_default golang:1.24-alpine \
		sh -c 'apk add --no-cache git && go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest \
		-path ./app/db/migrations -database "$(DOCKER_MIGRATE_DB_URL)" up'
	@echo "✅ Migrations applied"

# Migration reversibility check: up -> down -all -> up via Docker (no host Go required).
# Prerequisite: docker compose up -d postgres (or: make postgres-up)
migrate-cycle-docker: postgres-up
	@echo "🔁 Verifying migration up → down → up cycle via Docker..."
	@ready=0; \
	for i in $$(seq 1 60); do \
		if docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1; then \
			ready=1; break; \
		fi; \
		sleep 1; \
	done; \
	if [ "$$ready" != "1" ]; then \
		echo "❌ Postgres not ready. Run: make postgres-up"; \
		exit 1; \
	fi
	@$(MAKE) db-init-docker || true
	@docker run --rm -v "$(CURDIR):/app" -w /app --network pgquerynarrative_default golang:1.24-alpine \
		sh -c 'apk add --no-cache git && \
		go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest -path ./app/db/migrations -database "$(DOCKER_MIGRATE_DB_URL)" up && \
		go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest -path ./app/db/migrations -database "$(DOCKER_MIGRATE_DB_URL)" down -all && \
		go run -tags postgres github.com/golang-migrate/migrate/v4/cmd/migrate@latest -path ./app/db/migrations -database "$(DOCKER_MIGRATE_DB_URL)" up'
	@echo "✅ Migration up/down/up cycle passed"

db-security-verify-docker: postgres-up
	@echo "🔒 Verifying PostgreSQL security boundary..."
	@ready=0; \
	for i in $$(seq 1 30); do \
		if docker compose exec -T postgres psql -U postgres -d pgquerynarrative -tAc "SELECT 1 FROM pg_roles WHERE rolname='pgquerynarrative_readonly'" 2>/dev/null | grep -q 1; then \
			ready=1; break; \
		fi; \
		sleep 1; \
	done; \
	if [ "$$ready" != "1" ]; then \
		echo "❌ Postgres roles not ready. Run: make postgres-up && make migrate-docker"; \
		exit 1; \
	fi
	@docker run --rm -v "$(CURDIR):/workspace" --network pgquerynarrative_default postgres:16-alpine sh -c 'DB_URL="postgres://postgres:postgres@postgres:5432/pgquerynarrative?sslmode=disable" READONLY_DB_URL="postgres://pgquerynarrative_readonly:pgquerynarrative_readonly@postgres:5432/pgquerynarrative?sslmode=disable" sh /workspace/tools/db/verify_security.sh'

seed:
	@DB_URL="$${DB_URL:-$${DATABASE_URL:-$(LOCAL_DB_URL)}}"; \
	if [ -z "$$DB_URL" ]; then \
		echo "❌ DB_URL or DATABASE_URL not set. Using default..."; \
		DB_URL="$(LOCAL_DB_URL)"; \
	fi; \
	psql "$$DB_URL" -f ./tools/db/seed.sql || echo "⚠️  Seed data already exists or database not accessible"

# Large, reproducible seed (~10M rows by default). Run after migrate (000018 partition).
# Host needs psql. No host psql/go? Use: make seed-large-docker
seed-large:
	@echo "🌱 Seeding ~$(ROWS) rows into demo.sales (this can take a few minutes)..."
	@DB_URL="$${DB_URL:-$${DATABASE_URL:-$(LOCAL_DB_URL)}}"; \
	if [ -z "$$DB_URL" ]; then \
		echo "❌ DB_URL or DATABASE_URL not set. Using default..."; \
		DB_URL="$(LOCAL_DB_URL)"; \
	fi; \
	psql "$$DB_URL" -v rows=$(ROWS) -f ./tools/db/seed-large.sql

# Docker-only seed — no host psql required. Run after: make migrate-docker
seed-large-docker: postgres-up
	@echo "🌱 Seeding ~$(ROWS) rows into demo.sales via Docker (no host psql required)..."
	@docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1 || \
		(echo "❌ Postgres not ready. Run: make postgres-up" && exit 1)
	@docker compose cp tools/db/seed-large.sql postgres:/tmp/seed-large.sql
	@docker compose exec -T postgres psql -U postgres -d pgquerynarrative \
		-v rows=$(ROWS) -f /tmp/seed-large.sql

# Ensure Python venv with pyarrow + psycopg for NYC TLC loader.
$(NYC_VENV)/bin/python:
	@echo "📦 Creating NYC loader venv at $(NYC_VENV)…"
	@python3 -m venv $(NYC_VENV)
	@$(NYC_VENV)/bin/pip install -q -r tools/db/requirements-nyc.txt

# Load real NYC Yellow Taxi open data into opendata.yellow_trips.
# Requires: migrations through 000026, reachable Postgres on NYC_DB_URL.
# Override months: make seed-nyc MONTHS=2024-01
seed-nyc: $(NYC_VENV)/bin/python
	@echo "🚕 Loading NYC TLC Yellow Taxi ($(MONTHS)) into opendata.yellow_trips…"
	@DB_URL="$${DB_URL:-$${DATABASE_URL:-$(NYC_DB_URL)}}"; \
	MONTHS="$(MONTHS)" $(NYC_VENV)/bin/python tools/db/load_nyc_taxi.py --db-url "$$DB_URL" --months "$(MONTHS)"

# Same as seed-nyc, but ensures Docker Postgres is up first (port 5432 published).
seed-nyc-docker: postgres-up $(NYC_VENV)/bin/python
	@echo "🚕 Loading NYC TLC Yellow Taxi ($(MONTHS)) via Docker Postgres…"
	@docker compose exec -T postgres pg_isready -U postgres >/dev/null 2>&1 || \
		(echo "❌ Postgres not ready. Run: make postgres-up" && exit 1)
	@MONTHS="$(MONTHS)" $(NYC_VENV)/bin/python tools/db/load_nyc_taxi.py \
		--db-url "$(NYC_DB_URL)" --months "$(MONTHS)"

db-init:
	@echo "🗄️  Initializing database..."
	@if docker ps | grep -q pgquerynarrative-postgres; then \
		$(MAKE) db-init-docker; \
	else \
		$(MAKE) local-db-init || true; \
	fi

# Docker-only DB bootstrap (never falls back to host createdb — avoids CI races
# with a shutting-down local Postgres socket on GitHub runners).
db-init-docker:
	@echo "🗄️  Initializing database (Docker)..."
	@sh ./tools/db/init.sh

# Local PostgreSQL: create database and roles (no Docker). Uses default connection
# (e.g. current user on macOS Homebrew). Requires superuser.
local-db-init:
	@echo "🗄️  Creating database and roles (local PostgreSQL)..."
	@createdb pgquerynarrative 2>/dev/null || true
	@psql -d pgquerynarrative -f infra/postgres-init/00-init.sql && echo "✅ Database and roles ready" || (echo "⚠️  Run as PostgreSQL superuser (e.g. your macOS user). If roles exist, run: make migrate seed"; exit 0)

# ============================================================================
# Docker Compose commands
# ============================================================================

dev:
	sh ./tools/dev/dev.sh

dev-stop:
	docker compose down

docker-up:
	docker compose up -d

docker-down:
	docker compose down

docker-logs:
	docker compose logs -f

dev-watch:
	@echo "👀 Starting in watch mode (auto-reload on changes)..."
	@echo "This will build and start services with file watching."
	@echo ""
	docker compose up --build --watch

dev-build:
	@echo "🔨 Rebuilding app container..."
	$(GO) build -o bin/server ./cmd/server
	docker compose up --build -d app

dev-teardown:
	@echo "🧹 Tearing down development environment..."
	docker compose down -v
	rm -rf infra/data
	@echo "✅ Development environment reset complete"

# ============================================================================
# CLI Commands (Docker-only, no browser needed)
# ============================================================================

cli:
	@echo "💻 Running CLI command..."
	@sh ./tools/docker/docker-cli.sh $(CMD)

cli-shell:
	@echo "💻 Starting interactive CLI shell..."
	@echo "Type 'pgquerynarrative help' for commands"
	@echo "Or use 'pqn' as alias"
	@echo ""
	@docker compose run --rm -it --entrypoint /bin/sh cli -l

# ============================================================================
# Changelog
# ============================================================================

changelog:
	@echo "📝 Building CHANGELOG.md from changelog/..."
	@sh ./tools/changelog/build.sh

# ============================================================================
# LinkedIn (draft → approve → post); see: go run ./cmd/linkedin-social help
# ============================================================================

linkedin-social-help:
	@$(GO) run ./cmd/linkedin-social help

linkedin-social-draft:
	@$(GO) run ./cmd/linkedin-social draft

# ============================================================================
# PostgreSQL Extension
# ============================================================================

# Docker: start Postgres, init, migrate, install extension files, create extension, seed
setup-extension-docker:
	@echo "📦 Setting up Postgres and extension (Docker)..."
	@docker compose up -d postgres
	@for i in 1 2 3 4 5 6 7 8 9 10; do docker compose exec -T postgres pg_isready -U postgres 2>/dev/null && break; sleep 2; done
	@$(MAKE) db-init
	@DB_URL="postgres://postgres:postgres@localhost:5432/pgquerynarrative?sslmode=disable" $(MAKE) migrate
	@$(MAKE) install-extension-docker
	@docker compose exec -T postgres psql -U postgres -d pgquerynarrative -c "CREATE EXTENSION IF NOT EXISTS pgquerynarrative;"
	@docker compose exec -T postgres psql -U postgres -d pgquerynarrative -f - < tools/db/seed.sql 2>/dev/null || true
	@echo "Done. Test: docker compose exec postgres psql -U postgres -d pgquerynarrative -c \"SELECT pgquerynarrative_run_query('SELECT 1 FROM demo.sales LIMIT 1', 1);\""

# Copy extension files to Postgres sharedir (local: make install-extension; Docker: make install-extension-docker)
install-extension:
	@sh ./tools/db/install-extension.sh

install-extension-docker:
	@sh ./tools/db/install-extension-docker.sh
