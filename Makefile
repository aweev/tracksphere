# TrackSphere — developer shortcuts.
# On Windows run these through Git Bash/WSL, or use scripts/dev.ps1 instead.
SHELL := /bin/sh

GO       ?= go
DB_PORT  ?= 5433
DB_URL   ?= postgres://tracksphere:tracksphere@localhost:$(DB_PORT)/tracksphere?sslmode=disable
APP_URL  ?= postgres://tracksphere_app:tracksphere_app@localhost:$(DB_PORT)/tracksphere?sslmode=disable
PG_CONTAINER ?= tracksphere-pg

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

# ── Database ────────────────────────────────────────────────────────────
.PHONY: db-up
db-up: ## Start local Postgres (docker, port 5433)
	docker run -d --name $(PG_CONTAINER) \
		-e POSTGRES_USER=tracksphere -e POSTGRES_PASSWORD=tracksphere \
		-e POSTGRES_DB=tracksphere -p $(DB_PORT):5432 postgres:16

.PHONY: db-down
db-down: ## Stop and remove local Postgres (data is lost)
	docker rm -f $(PG_CONTAINER)

.PHONY: db-reset
db-reset: ## Drop and recreate the public schema
	docker exec $(PG_CONTAINER) psql -U tracksphere -d tracksphere \
		-c "DROP SCHEMA public CASCADE; CREATE SCHEMA public; GRANT ALL ON SCHEMA public TO tracksphere;"

.PHONY: migrate
migrate: ## Apply embedded migrations (as owner)
	TRACKSPHERE_DATABASE_URL="$(DB_URL)" TRACKSPHERE_MIGRATIONS_URL="$(DB_URL)" \
	TRACKSPHERE_SECRET_KEY=x TRACKSPHERE_CARRIER_WEBHOOK_SECRET=x $(GO) run ./cmd/migrate

.PHONY: seed
seed: ## Load demo tenant/shipments (use ARGS=--reset to rebuild)
	TRACKSPHERE_DATABASE_URL="$(APP_URL)" TRACKSPHERE_MIGRATIONS_URL="$(DB_URL)" \
	TRACKSPHERE_SECRET_KEY=local-dev-secret-key-change-me-000000000000000000 \
	TRACKSPHERE_CARRIER_WEBHOOK_SECRET=dev-carrier-webhook-secret \
	$(GO) run ./cmd/seed $(ARGS)

.PHONY: rls-test
rls-test: ## Prove tenant isolation (runs as the restricted app role)
	docker exec -i $(PG_CONTAINER) psql -U tracksphere_app -d tracksphere \
		-v ON_ERROR_STOP=1 < scripts/rls_test.sql

# ── Backend ─────────────────────────────────────────────────────────────
.PHONY: build
build: ## Build all Go binaries into ./bin
	$(GO) build -o bin/ ./cmd/...

.PHONY: test
test: ## Run Go unit tests
	$(GO) test ./... -count=1

.PHONY: vet
vet: ## Static analysis
	$(GO) vet ./...

.PHONY: fmt
fmt: ## Format Go code
	$(GO) fmt ./...

.PHONY: api
api: ## Run the API server (loads .env)
	$(GO) run ./cmd/api

.PHONY: worker
worker: ## Run the background worker (loads .env)
	$(GO) run ./cmd/worker

# ── Frontend ────────────────────────────────────────────────────────────
.PHONY: web-install
web-install: ## Install web dependencies
	cd web && npm install

.PHONY: web-dev
web-dev: ## Run Next.js dev server (port 3100, proxies /api)
	cd web && npm run dev -- -p 3100

.PHONY: web-build
web-build: ## Typecheck + production build of the web app
	cd web && npm run build

# ── Verification ────────────────────────────────────────────────────────
.PHONY: check
check: vet test web-build ## Everything CI runs
	@echo "check: OK"

.PHONY: e2e
e2e: ## Full end-to-end proof against a running API
	pwsh -File scripts/e2e.ps1

# ── Containers ──────────────────────────────────────────────────────────
.PHONY: compose-up
compose-up: ## Build + start the whole stack (deploy/.env required)
	cd deploy && docker compose up -d --build

.PHONY: compose-logs
compose-logs: ## Tail stack logs
	cd deploy && docker compose logs -f --tail=100

.PHONY: compose-down
compose-down: ## Stop the stack (keeps the volume)
	cd deploy && docker compose down