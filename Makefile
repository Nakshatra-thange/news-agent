# Synergy developer tasks. Run `make help` for a list.

# Load local configuration if present (simple KEY=value lines, no quotes).
-include .env
export

BIN                 := bin/synergy
VERSION             ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS             := -X main.version=$(VERSION)
STATICCHECK_VERSION := v0.8.1

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show available targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-14s %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the synergy binary into bin/
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/synergy

.PHONY: run
run: build ## Build and run the API server
	./$(BIN) serve

.PHONY: test
test: ## Run unit tests
	go test ./...

.PHONY: test-race
test-race: ## Run unit tests with the race detector
	go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests with a coverage summary
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

.PHONY: fmt
fmt: ## Format all Go code
	gofmt -s -w .

.PHONY: fmt-check
fmt-check: ## Fail if any Go file is not gofmt-formatted
	@out=$$(gofmt -s -l .); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

.PHONY: vet
vet: ## Run go vet
	go vet ./...

.PHONY: staticcheck
staticcheck: ## Run staticcheck (fetched via go run; nothing to install)
	go run honnef.co/go/tools/cmd/staticcheck@$(STATICCHECK_VERSION) ./...

.PHONY: check
check: fmt-check vet staticcheck test-race ## All static checks and tests (run before committing)

.PHONY: tidy
tidy: ## Tidy go.mod/go.sum
	go mod tidy

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf bin coverage.out
