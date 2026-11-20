GO ?= go

.PHONY: all build test race full test-full race-full fuzz cover bench bench-compare bench-server bench-inproc vuln fmt fmt-check vet check compat tidy clean help

FUZZTIME ?= 10s
BENCHCOUNT ?= 1

all: check test ## format check, vet and the fast tests

build: ## compile every package of the main module and of sedimentd
	$(GO) build ./...
	cd cmd/sedimentd && $(GO) build -o /dev/null .

test: ## fast tests of the main module and of sedimentd (-short, no kill -9)
	$(GO) test -short ./...
	cd cmd/sedimentd && $(GO) test -short ./...

race: ## fast tests with the race detector (-short)
	$(GO) test -short -race ./...
	cd cmd/sedimentd && $(GO) test -short -race ./...

test-full: ## all the tests of the main module, also the slow ones and kill -9
	$(GO) test -count=1 ./...
	cd cmd/sedimentd && $(GO) test -count=1 ./...

race-full: ## all the tests with the race detector
	$(GO) test -race -count=1 ./...
	cd cmd/sedimentd && $(GO) test -race -count=1 ./...

fuzz: ## every fuzz target for FUZZTIME each (default 10s)
	$(GO) test ./internal/sstable -run '^$$' -fuzz '^FuzzBlockIter$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/sstable -run '^$$' -fuzz '^FuzzReader$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/sstable -run '^$$' -fuzz '^FuzzCommonPrefixLength$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/merge -run '^$$' -fuzz '^FuzzIterator$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/batch -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/manifest -run '^$$' -fuzz '^FuzzDecode$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/ikey -run '^$$' -fuzz '^FuzzCompare$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/ikey -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./resp -run '^$$' -fuzz '^FuzzReadCommand$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./resp -run '^$$' -fuzz '^FuzzReadValue$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./resp -run '^$$' -fuzz '^FuzzPending$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./server/acl -run '^$$' -fuzz '^FuzzParse$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0
	$(GO) test ./internal/skiplist -run '^$$' -fuzz '^FuzzList$$' -fuzztime $(FUZZTIME) -fuzzminimizetime=0

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

bench-server: ## server benchmarks, with and without pipelining
	$(GO) test -run '^$$' -bench Benchmark -benchtime=2s -benchmem ./server

bench-inproc: ## server cost per command without the network, with a pipeline of 16
	$(GO) test -run '^$$' -bench Inproc -benchtime=2000000x -benchmem ./server

vuln: ## check the dependencies and the code for known vulnerabilities
	$(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd compat && $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd cmd/sedimentd && $(GO) run golang.org/x/vuln/cmd/govulncheck@latest ./...

fmt: ## format all Go files (every module)
	gofmt -w .

fmt-check: ## fail if any file is not formatted
	@out="$$(gofmt -l .)"; if [ -n "$$out" ]; then echo "not formatted:"; echo "$$out"; exit 1; fi

vet: ## go vet on the three modules
	$(GO) vet ./...
	cd compat && $(GO) vet ./...
	cd cmd/sedimentd && $(GO) vet ./...

check: fmt-check vet ## formatting and static checks

compat: ## compatibility tests against goleveldb (separate module)
	cd compat && $(GO) test -count=1 ./...

tidy: ## go mod tidy on the three modules
	$(GO) mod tidy
	cd compat && $(GO) mod tidy
	cd cmd/sedimentd && $(GO) mod tidy

clean:
	rm -f coverage.out
	$(GO) clean -testcache

help: ## list targets
	@grep -E '^[a-z-]+:.*##' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-10s %s\n", $$1, $$2}'
