# ── Local development ──────────────────────────────────────────────────────────

run:  ## Run server locally (requires local postgres)
	cd server && go run ./cmd/easy_upload

fmt:  ## Format Go source
	cd server && gofmt -w $$(find . -name '*.go' -type f | sort)

# ── Docker dev environment ────────────────────────────────────────────────────

dev-up:  ## Start dev stack (postgres + MinIO + server)
	docker compose up --build -d postgres minio minio-init migrate server

dev-up-client:  ## Start full dev stack including Vite dev server
	docker compose --profile client up --build -d

dev-down:  ## Stop dev stack
	docker compose down

minio-console:  ## Open MinIO console in browser
	xdg-open http://localhost:9001

# ── Testing ────────────────────────────────────────────────────────────────────

test-unit:  ## Run Go unit tests (no external deps needed)
	cd server && go test -short ./...

test-integration:  ## Run integration tests with real postgres + MinIO (requires Docker)
	docker compose -f docker-compose.test.yml up -d
	@echo "Waiting for services to be ready..."; sleep 5
	cd server && \
	  DATABASE_TEST_URL="postgres://easy:easy@localhost:5433/easy_upload_test?sslmode=disable" \
	  S3_TEST_ENDPOINT="http://localhost:9002" \
	  S3_TEST_BUCKET="test-bucket" \
	  S3_TEST_ACCESS_KEY="testkey" \
	  S3_TEST_SECRET_KEY="testsecret" \
	  go test ./... -timeout 120s -v
	docker compose -f docker-compose.test.yml down

test-client:  ## Run client tests
	cd client && npm test -- --run

test-all: test-unit test-client  ## Run all unit + client tests (no Docker needed)

lint:  ## Run static analysis
	cd server && go vet ./...
	cd client && npm run lint

# ── Client ─────────────────────────────────────────────────────────────────────

client-install:  ## Install client npm dependencies
	cd client && npm install

client-dev:  ## Start Vite dev server
	cd client && npm run dev

client-build:  ## Build client for production
	cd client && npm run build

client-test:  ## Run client tests
	cd client && npm run test

# ── Docs ───────────────────────────────────────────────────────────────────────

docs:
	@echo "Docs:"
	@echo "  docs/idea.md"
	@echo "  docs/technical_spec_v1.md"
	@echo "  docs/client_integration_guide.md"
	@echo "  docs/roadmap.md"

# Legacy aliases
test:
	cd server && go test ./...

vet:
	cd server && go vet ./...
