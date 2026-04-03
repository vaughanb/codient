# Codient — Go CLI (module: codient, main: ./cmd/codient)
GO      ?= go
BIN_DIR := bin
EXE     := $(shell $(GO) env GOEXE)
BIN     := $(BIN_DIR)/codient$(EXE)

.PHONY: all help build install clean test test-short test-race test-integration vet fmt mod-tidy check run

all: build

help:
	@echo "Targets:"
	@echo "  make / make all     Build $(BIN)"
	@echo "  make build          Same as all"
	@echo "  make install        go install ./cmd/codient"
	@echo "  make run ARGS='…'   go run ./cmd/codient -- …"
	@echo "  make test           go test ./..."
	@echo "  make test-short     go test -short ./..."
	@echo "  make test-race      go test -race ./..."
	@echo "  make test-integration  live API tests (-tags=integration, sets CODIENT_INTEGRATION=1)"
	@echo "  make vet            go vet ./..."
	@echo "  make fmt            go fmt ./..."
	@echo "  make mod-tidy       go mod tidy"
	@echo "  make check          vet + test"
	@echo "  make clean          remove $(BIN_DIR)/"

build:
	$(GO) build -o $(BIN) ./cmd/codient

install:
	$(GO) install ./cmd/codient

clean:
	$(RM) -r $(BIN_DIR)

test:
	$(GO) test ./...

test-short:
	$(GO) test -short ./...

test-race:
	$(GO) test -race ./...

test-integration:
	CODIENT_INTEGRATION=1 $(GO) test -tags=integration -count=1 ./...

vet:
	$(GO) vet ./...

fmt:
	$(GO) fmt ./...

mod-tidy:
	$(GO) mod tidy

check: vet test

run:
	$(GO) run ./cmd/codient -- $(ARGS)
