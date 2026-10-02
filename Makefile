# modharbor — development tasks.
#
# Every recipe uses $(GO) so `make GO=go1.24.0 build` works.
# Run `make` or `make help` for the target list.

.DEFAULT_GOAL := help

GO      ?= go
GOFMT   ?= gofmt
BINARY  ?= modharbor
CMD     ?= ./cmd/modharbor
BIN_DIR ?= bin
OUTPUT  ?= $(BIN_DIR)/$(BINARY)

MODULE := github.com/MohammadMD1383/modharbor
VERSION_PKG := $(MODULE)/internal/version

# Version comes from the nearest git tag. Outside a repository (a tarball, a
# CI cache, this file before `git init`) every git call fails silently and the
# fallbacks keep `make build` working.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)

LDFLAGS ?= -s -w \
	-X $(VERSION_PKG).Version=$(VERSION) \
	-X $(VERSION_PKG).Commit=$(COMMIT) \
	-X $(VERSION_PKG).Date=$(DATE)

# Only the project's own sources; testdata is fixture data.
SRC_DIRS := cmd internal

.PHONY: all build install test race vet fmt fmt-check lint cover tidy check \
	clean snapshot release help

all: build ## Build the modharbor binary into bin/

build: ## Build bin/modharbor with version, commit and date stamped in
	@mkdir -p $(BIN_DIR)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $(OUTPUT) $(CMD)

install: ## Install modharbor into GOPATH/bin
	$(GO) install -trimpath -ldflags '$(LDFLAGS)' $(CMD)

test: ## Run the test suite
	$(GO) test ./...

race: ## Run the test suite with the race detector
	$(GO) test -race ./...

vet: ## Run go vet
	$(GO) vet ./...

fmt: ## Rewrite sources with gofmt -w
	$(GOFMT) -w $(SRC_DIRS)

fmt-check: ## Fail if any source needs gofmt
	@out="$$($(GOFMT) -l $(SRC_DIRS))"; \
	if [ -n "$$out" ]; then \
		echo "gofmt: these files need formatting:"; \
		echo "$$out"; \
		echo "run: make fmt"; \
		exit 1; \
	fi
	@echo "gofmt: clean"

lint: ## Run golangci-lint, falling back to vet + fmt-check
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed; falling back to go vet + gofmt"; \
		$(MAKE) vet; \
		$(MAKE) fmt-check; \
	fi

cover: ## Write coverage.out and print the total
	$(GO) test -covermode=atomic -coverprofile=coverage.out ./...
	@$(GO) tool cover -func=coverage.out | tail -1

tidy: ## Tidy go.mod and go.sum
	$(GO) mod tidy

check: fmt-check vet test ## Everything CI runs locally

clean: ## Remove build output, goreleaser output and coverage files
	rm -rf $(BIN_DIR) dist coverage.out coverage.html

snapshot: ## Build a snapshot with goreleaser without publishing
	goreleaser release --snapshot --clean

release: ## Publish a release with goreleaser (requires a v* tag)
	goreleaser release --clean

help: ## List the available targets
	@printf 'modharbor — development targets\n\n'
	@awk 'BEGIN {FS = ":.*##"} /^[a-zA-Z_-]+:.*?##/ { printf "  %-12s %s\n", $$1, $$2 }' $(MAKEFILE_LIST)
	@printf '\nOverridable: GO, BINARY, BIN_DIR, VERSION, COMMIT, DATE, LDFLAGS\n'
	@printf 'Example: make build VERSION=1.2.3\n'