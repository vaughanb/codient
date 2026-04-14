# Codient — Go CLI (module: codient, main: ./cmd/codient)
GO      ?= go
BIN_DIR := bin
EXE     := $(shell $(GO) env GOEXE)
BIN     := $(BIN_DIR)/codient$(EXE)

.PHONY: all help build install clean test test-unit test-short test-race test-integration test-integration-strict vet fmt mod-tidy check lint govulncheck run release major minor patch

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
	@echo "  make lint           golangci-lint run (requires golangci-lint on PATH)"
	@echo "  make govulncheck    vulnerability scan on dependencies (go run)"
	@echo "  make check          vet + test-unit (no live integration; safe for CI)"
	@echo "  make clean          remove $(BIN_DIR)/"
	@echo "  make release [patch]      bump version, commit, tag, push (default: patch)"
	@echo "  make release minor        bump minor version"
	@echo "  make release major        bump major version"

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

lint:
	golangci-lint run ./...

govulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

check: vet test-unit

run:
	$(GO) run ./cmd/codient -- $(ARGS)

VERSION_FILE := internal/codientcli/version.go
CUR_VERSION   = $(shell sed -n 's/.*Version = "\(.*\)"/\1/p' $(VERSION_FILE))

ifneq ($(filter major,$(MAKECMDGOALS)),)
  BUMP = major
else ifneq ($(filter minor,$(MAKECMDGOALS)),)
  BUMP = minor
else
  BUMP = patch
endif

major minor patch:
	@:

release:
	@CUR="$(CUR_VERSION)"; \
	MAJOR=$$(echo "$$CUR" | cut -d. -f1); \
	MINOR=$$(echo "$$CUR" | cut -d. -f2); \
	PATCH=$$(echo "$$CUR" | cut -d. -f3); \
	case "$(BUMP)" in \
		major) MAJOR=$$((MAJOR + 1)); MINOR=0; PATCH=0 ;; \
		minor) MINOR=$$((MINOR + 1)); PATCH=0 ;; \
		patch) PATCH=$$((PATCH + 1)) ;; \
		*) echo "error: BUMP must be major, minor, or patch"; exit 1 ;; \
	esac; \
	NEXT="$$MAJOR.$$MINOR.$$PATCH"; \
	TAG="v$$NEXT"; \
	echo "Bumping version: $$CUR -> $$NEXT ($$TAG)"; \
	sed -i.bak 's/const Version = ".*"/const Version = "'$$NEXT'"/' $(VERSION_FILE) && rm -f $(VERSION_FILE).bak; \
	git add $(VERSION_FILE); \
	git commit -m "release $$TAG"; \
	git tag "$$TAG"; \
	git push origin HEAD "$$TAG"; \
	echo "Released $$TAG"
