# ParentBot developer entry points.
# Every target must work on a clean checkout with `make setup` run first.

SHELL := powershell.exe
.DEFAULT_GOAL := help

BIN_DIR := bin
API_BIN := $(BIN_DIR)\api.exe
WORKER_BIN := $(BIN_DIR)\worker.exe
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: help
help: ## Show available targets
	@Select-String -Path Makefile -Pattern '^[a-zA-Z_-]+:.*?## .*$$' | ForEach-Object { $$_.Line -replace ':.*?## ', '  ' } | Sort-Object

.PHONY: setup
setup: ## Install Go dependencies
	go mod download

.PHONY: tidy
tidy: ## Tidy and verify module dependencies
	go mod tidy

.PHONY: fmt
fmt: ## Format all Go files
	gofmt -s -w .

.PHONY: lint
lint: ## Run go vet
	go vet ./...

.PHONY: fmtcheck
fmtcheck: ## Fail if any file is unformatted (used in CI)
	$bad = gofmt -s -l . ; if ($bad) { Write-Output "Unformatted files:"; Write-Output $bad ; exit 1 }

.PHONY: test
test: ## Run unit tests with race detector and coverage
	go test -race "-coverprofile=coverage.out" "-covermode=atomic" ./...

.PHONY: cover
cover: test ## Open the coverage report in a browser
	go tool cover -html=coverage.out

.PHONY: build
build: $(API_BIN) $(WORKER_BIN) ## Build both binaries

$(API_BIN): $(shell go list -f '{{ join .GoFiles " " }}' ./cmd/api)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(API_BIN) ./cmd/api

$(WORKER_BIN): $(shell go list -f '{{ join .GoFiles " " }}' ./cmd/worker)
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(WORKER_BIN) ./cmd/worker

.PHONY: run-api
run-api: ## Run the API server locally
	go run ./cmd/api

.PHONY: run-worker
run-worker: ## Run the worker locally
	go run ./cmd/worker

.PHONY: up
up: ## Start Postgres and Redis in Docker
	docker compose up -d

.PHONY: down
down: ## Stop local dependencies
	docker compose down

.PHONY: logs
logs: ## Tail dependency logs
	docker compose logs -f

.PHONY: psql
psql: ## Open a psql shell against the local database
	docker compose exec postgres psql -U parentbot -d parentbot

.PHONY: clean
clean: ## Remove build artifacts
	Remove-Item -Recurse -Force $(BIN_DIR) -ErrorAction SilentlyContinue ; Remove-Item -Force coverage.out -ErrorAction SilentlyContinue
