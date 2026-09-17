# npmplus-docker-sync - development tasks
BINARY      := npmplus-docker-sync
PKG         := ./...
VERSION     ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT      ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE        ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS     := -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)
IMAGE       ?= ghcr.io/ventumphoenix/npmplus-docker-sync
NPM_IMAGE      ?= jc21/nginx-proxy-manager:latest
NPMPLUS_IMAGE  ?= ghcr.io/zoeyvid/npmplus:latest

.DEFAULT_GOAL := help

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'

.PHONY: build
build: ## Build the static binary
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BINARY) .

.PHONY: run
run: ## Run locally (reads .env if present)
	@set -a; [ -f .env ] && . ./.env; set +a; go run .

.PHONY: test
test: ## Run the unit tests
	go test -count=1 $(PKG)

.PHONY: cover
cover: ## Run tests and write coverage.html
	go test -race -covermode=atomic -coverpkg=$(PKG) -coverprofile=coverage.out $(PKG)
	go tool cover -func=coverage.out | tail -n 1
	go tool cover -html=coverage.out -o coverage.html

.PHONY: integration
integration: ## Run the integration tests against a real NPM (needs Docker)
	go test -tags integration -count=1 -timeout 30m -v ./test/integration/

.PHONY: integration-npmplus
integration-npmplus: ## Run the integration tests against NPMplus
	NPM_IMAGE=$(NPMPLUS_IMAGE) go test -tags integration -count=1 -timeout 30m -v ./test/integration/

.PHONY: fuzz
fuzz: ## Run the label parser fuzz targets for a minute each
	go test ./internal/docker/ -run=XXX -fuzz=FuzzParse -fuzztime=60s
	go test ./internal/docker/ -run=XXX -fuzz=FuzzSplitLabel -fuzztime=60s

.PHONY: bench
bench: ## Run benchmarks
	go test -bench=. -benchmem -run=^$$ $(PKG)

.PHONY: lint
lint: ## Run golangci-lint (https://golangci-lint.run)
	golangci-lint run

.PHONY: fmt
fmt: ## Format the code
	gofmt -s -w .
	go mod tidy

.PHONY: vet
vet: ## Run go vet
	go vet $(PKG)

.PHONY: schemas
schemas: ## Re-vendor the NPM/NPMplus request schemas used by the contract test
	python3 scripts/vendor-schemas.py

.PHONY: vuln
vuln: ## Scan dependencies for known vulnerabilities
	./scripts/govulncheck.sh

.PHONY: check
check: fmt vet lint test ## Everything CI runs

.PHONY: docker
docker: ## Build the container image
	docker build \
		--build-arg VERSION=$(VERSION) \
		--build-arg COMMIT=$(COMMIT) \
		--build-arg DATE=$(DATE) \
		-t $(IMAGE):$(VERSION) -t $(IMAGE):latest .

.PHONY: clean
clean: ## Remove build artefacts
	rm -f $(BINARY) coverage.out coverage.html
	rm -rf dist/
