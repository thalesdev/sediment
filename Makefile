GO ?= go

.PHONY: all build test race full test-full race-full fuzz cover bench bench-compare fmt fmt-check vet check compat tidy clean help

FUZZTIME ?= 10s
BENCHCOUNT ?= 1

all: check test ## format check, vet and the fast tests

build: ## compile every package of the main module
	$(GO) build ./...

test: ## fast tests of the main module (-short, no kill -9)
	$(GO) test -short ./...

race: ## fast tests with the race detector (-short)
	$(GO) test -short -race ./...

test-full: ## all the tests of the main module, also the slow ones and kill -9
	$(GO) test -count=1 ./...

race-full: ## all the tests with the race detector
	$(GO) test -race -count=1 ./...

fuzz: ## every fuzz target for FUZZTIME each (default 10s)
	$(GO) test ./internal/sstable -run '^$$' -fuzz '^FuzzBlockIter$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/sstable -run '^$$' -fuzz '^FuzzReader$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/sstable -run '^$$' -fuzz '^FuzzCommonPrefixLength$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/merge -run '^$$' -fuzz '^FuzzIterator$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/batch -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/manifest -run '^$$' -fuzz '^FuzzDecode$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/ikey -run '^$$' -fuzz '^FuzzCompare$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/ikey -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0

full: check test-full race-full compat fuzz ## everything: checks, all tests, race, compat and fuzz

cover: ## unit tests with coverage report in coverage.out
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -n 1

bench: ## run benchmarks
	$(GO) test -run '^$$' -bench . -benchmem ./...

bench-compare: ## Shale against goleveldb with the db_bench workloads, slow (BENCHCOUNT, default 1)
	cd compat && $(GO) test -run '^$$' -bench '^Benchmark(FillRandom|Overwrite|ReadRandom|FillRandomParallel)$$' -benchtime=1000000x -count=$(BENCHCOUNT)
	cd compat && $(GO) test -run '^$$' -bench '^BenchmarkFillSync$$' -benchtime=1000x -count=$(BENCHCOUNT)
	cd compat && $(GO) test -run '^$$' -bench '^BenchmarkFillSyncParallel$$' -benchtime=4000x -count=$(BENCHCOUNT)

fmt: ## format all Go files (main module and compat)
	gofmt -w .

fmt-check: ## fail if any file is not formatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

vet: ## go vet on both modules
	$(GO) vet ./...
	cd compat && $(GO) vet ./...

check: fmt-check vet ## formatting and static checks

compat: ## compatibility tests against goleveldb (separate module)
	cd compat && $(GO) test -count=1 ./...

tidy: ## go mod tidy on both modules
	$(GO) mod tidy
	cd compat && $(GO) mod tidy

clean:
	rm -f coverage.out
	$(GO) clean -testcache

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
