GO ?= go

.PHONY: all build test race cover bench fmt fmt-check vet check compat tidy clean help

all: check test ## format check, vet and unit tests

build: ## compile every package of the main module
	$(GO) build ./...

test: ## unit tests of the main module (no external dependencies)
	$(GO) test ./...

race: ## unit tests with the race detector
	$(GO) test -race ./...

cover: ## unit tests with coverage report in coverage.out
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

bench: ## run benchmarks
	$(GO) test -run '^$$' -bench . -benchmem ./...

fmt: ## format all Go files (main module and compat)
	gofmt -w .

fmt-check: ## fail if any file is not formatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

vet: ## go vet on both modules
	$(GO) vet ./...
	cd compat && $(GO) vet ./...

check: fmt-check vet ## formatting and static checks

compat: ## compatibility tests against goleveldb (separate module)
	cd compat && $(GO) test ./...

tidy: ## go mod tidy on both modules
	$(GO) mod tidy
	cd compat && $(GO) mod tidy

clean:
	rm -f coverage.out
	$(GO) clean -testcache

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
