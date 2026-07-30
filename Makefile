.PHONY: all build test vet lint run dev clean cover setup-ui build-ui

BINARY := groovearr
CMD    := ./cmd/groovearr
BUILD  := ./build

# UI build
UI_DIR := ./ui

GOPATH  := $(shell go env GOPATH)
GO      := go
GOTOOL  := $(GOPATH)/bin

# ─── UI Build ──────────────────────────────────────────────────

setup-ui:
	@echo "==> Installing UI dependencies..."
	cd $(UI_DIR) && npm ci

build-ui: setup-ui
	@echo "==> Building UI..."
	cd $(UI_DIR) && npm run build

# ─── Default ──────────────────────────────────────────────────

all: vet test build

# ─── Build ────────────────────────────────────────────────────

build: build-ui
	@echo "==> Building $(BINARY)..."
	$(GO) build -o $(BUILD)/$(BINARY) $(CMD)
	@echo "    Built: $(BUILD)/$(BINARY)"

# Force rebuild (also replays go:embed when static files change)
rebuild: build-ui
	@echo "==> Rebuilding $(BINARY) (forced)..."
	$(GO) build -a -o $(BUILD)/$(BINARY) $(CMD)
	@echo "    Rebuilt: $(BUILD)/$(BINARY)"

# ─── Test ─────────────────────────────────────────────────────

test:
	@echo "==> Running Go tests..."
	$(GO) test ./... -count=1
	@echo "==> Running UI tests..."
	cd ui && npm test

test-race:
	@echo "==> Running tests (-race)..."
	$(GO) test -race ./... -count=1

test-verbose:
	@echo "==> Running tests (verbose)..."
	$(GO) test -v ./... -count=1

cover:
	@echo "==> Running tests with coverage..."
	$(GO) test ./... -coverprofile=coverage.out
	$(GO) tool cover -func=coverage.out
	@echo "    HTML report: go tool cover -html=coverage.out"

cover-html: cover
	$(GO) tool cover -html=coverage.out -o coverage.html
	@echo "    Opened: coverage.html"

# ─── Lint / Vet ───────────────────────────────────────────────

vet:
	@echo "==> Running go vet..."
	$(GO) vet ./...

lint: vet
	@echo "==> Running lint..."
	@if command -v staticcheck >/dev/null 2>&1; then \
		staticcheck ./...; \
	elif [ -x "$(GOTOOL)/staticcheck" ]; then \
		"$(GOTOOL)/staticcheck" ./...; \
	else \
		echo "    staticcheck not installed. Run: go install honnef.co/go/tools/cmd/staticcheck@latest"; \
	fi

# ─── Run ──────────────────────────────────────────────────────

run: build
	@echo "==> Starting $(BINARY)..."
	$(BUILD)/$(BINARY)

# Development mode: rebuilds on start, verbose config, Vite dev server.
dev: build-ui
	@echo "==> Starting Vite dev server + $(BINARY)..." && \
	trap 'kill %1 2>/dev/null; echo "  Vite stopped."' EXIT INT TERM && \
	cd $(UI_DIR) && npm run dev & \
	sleep 2 && \
	$(GO) build -a -o $(BUILD)/$(BINARY) $(CMD) && \
	GROOVEARR_CONFIG=./config.json $(BUILD)/$(BINARY)

# ─── Clean ────────────────────────────────────────────────────

clean:
	@echo "==> Cleaning..."
	rm -rf $(BUILD)
	rm -rf $(UI_DIR)/dist
	rm -rf $(UI_DIR)/node_modules
	rm -f coverage.out coverage.html
	@echo "    Done."

# ─── Docker ───────────────────────────────────────────────────

docker-setup:
	@echo "==> Generating slskd.yml from .env..."
	@sh scripts/generate-slskd-config.sh
	@echo "    Run 'make docker-restart' to apply."

docker-build:
	@echo "==> Building Docker image..."
	docker compose build

docker-up:
	@echo "==> Starting services..."
	docker compose up -d
	@echo "    groovearr:    http://localhost:8008"
	@echo "    slskd:        http://localhost:5030"
	@echo "    prowlarr:     http://localhost:9696"
	@echo "    qbittorrent:  http://localhost:8080"
	@echo "    flaresolverr: http://localhost:8191"

docker-down:
	@echo "==> Stopping services..."
	docker compose down

docker-logs:
	docker compose logs -f

docker-restart: docker-build docker-up
	@echo "    Rebuilt and restarted."
	@echo "    groovearr:    http://localhost:8008"
	@echo "    slskd:        http://localhost:5030"
	@echo "    prowlarr:     http://localhost:9696"
	@echo "    qbittorrent:  http://localhost:8080"
	@echo "    flaresolverr: http://localhost:8191"

# ─── Help ─────────────────────────────────────────────────────

help:
	@echo "Groovearr — Build & Test Automation"
	@echo ""
	@echo "Usage:"
	@echo "  make              run vet + test + build (default)"
	@echo "  make build        build binary"
	@echo "  make rebuild      force full rebuild (picks up go:embed changes)"
	@echo "  make test         run all tests"
	@echo "  make test-race    run tests with race detector"
	@echo "  make cover        run tests with coverage report"
	@echo "  make vet          static analysis (go vet)"
	@echo "  make lint         vet + staticcheck (if installed)"
	@echo "  make run          build and run"
	@echo "  make dev          force-rebuild and run (for frontend changes)"
	@echo "  make clean        remove build artifacts"
	@echo "  make docker-build  build Docker image"
	@echo "  make docker-up     start full stack"
	@echo "  make docker-down   stop services"
	@echo "  make docker-logs   tail all container logs"
	@echo "  make docker-restart  rebuild + restart (quick test cycle)"
