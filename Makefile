# Codient — Go CLI (module: codient, main: ./cmd/codient)
GO      ?= go
BIN_DIR := bin
EXE     := $(shell $(GO) env GOEXE)
BIN     := $(BIN_DIR)/codient$(EXE)

.PHONY: all help build install clean test test-unit test-short test-race test-integration test-integration-strict vet fmt mod-tidy check run searxng-up searxng-down searxng-status

all: build

help:
	@echo "Targets:"
	@echo "  make / make all     Build $(BIN)"
	@echo "  make build          Same as all"
	@echo "  make install        go install ./cmd/codient"
	@echo "  make run ARGS='…'   go run ./cmd/codient -- …"
	@echo "  make test           full suite: unit + live integration (needs model + API; see test-unit for CI)"
	@echo "  make test-unit      unit tests only (go test ./...; no live LLM)"
	@echo "  make test-short     go test -short ./..."
	@echo "  make test-race      go test -race ./..."
	@echo "  make test-integration        live API tests (CODIENT_INTEGRATION=1 only)"
	@echo "  make test-integration-strict live + strict tool tests (+ CODIENT_INTEGRATION_STRICT_TOOLS=1)"
	@echo "  make vet            go vet ./..."
	@echo "  make fmt            go fmt ./..."
	@echo "  make mod-tidy       go mod tidy"
	@echo "  make check          vet + test-unit (no live integration; safe for CI)"
	@echo "  make clean          remove $(BIN_DIR)/"
	@echo ""
	@echo "SearXNG (web search):"
	@echo "  make searxng-up     start SearXNG in Docker (port SEARXNG_PORT, default 8888)"
	@echo "  make searxng-down   stop and remove the SearXNG container"
	@echo "  make searxng-status show SearXNG container status"

build:
	$(GO) build -o $(BIN) ./cmd/codient

install:
	$(GO) install ./cmd/codient

clean:
	$(RM) -r $(BIN_DIR)

# Full test run: integration-tagged tests + env for strict tools and run_command (requires configured model and server).
test: export CODIENT_INTEGRATION = 1
test: export CODIENT_INTEGRATION_STRICT_TOOLS = 1
test: export CODIENT_INTEGRATION_RUN_COMMAND = 1
test:
	$(GO) test -tags=integration -count=1 -timeout 90m ./...

test-unit:
	$(GO) test ./...

test-short:
	$(GO) test -short ./...

test-race:
	$(GO) test -race ./...

test-integration: export CODIENT_INTEGRATION = 1
test-integration:
	$(GO) test -tags=integration -count=1 ./...

test-integration-strict: export CODIENT_INTEGRATION = 1
test-integration-strict: export CODIENT_INTEGRATION_STRICT_TOOLS = 1
test-integration-strict:
	$(GO) test -tags=integration -count=1 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

mod-tidy:
	$(GO) mod tidy

check: vet test-unit

run:
	$(GO) run ./cmd/codient -- $(ARGS)

# --- SearXNG (web search backend) ---
SEARXNG_PORT    ?= 8888
SEARXNG_COMPOSE := docker/searxng/docker-compose.yml

searxng-up: export SEARXNG_PORT := $(SEARXNG_PORT)
searxng-up:
	docker compose -f $(SEARXNG_COMPOSE) up -d

searxng-down:
	docker compose -f $(SEARXNG_COMPOSE) down

searxng-status:
	@docker ps --filter name=codient-searxng --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}"
