BINARY_EXT := $(if $(filter Windows_NT,$(OS)),.exe,)
BINARY := trasloco$(BINARY_EXT)
MODULE := github.com/HarnageaGabriel/trasloco
VERSION ?= dev
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || printf unknown)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -s -w -X $(MODULE)/internal/version.Version=$(VERSION) -X $(MODULE)/internal/version.Commit=$(COMMIT) -X $(MODULE)/internal/version.Date=$(DATE)

.PHONY: build test lint vuln cover tidy fmt

build:
	mkdir -p bin
	go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/trasloco

test:
	go test -race ./...

lint:
	golangci-lint run ./...

vuln:
	govulncheck ./...

cover:
	go test -race -covermode=atomic -coverprofile=coverage.out ./...

tidy:
	go mod tidy

fmt:
	gofmt -w $$(find cmd internal schemas -type f -name '*.go')

.PHONY: fuzz bench docs docs-check cover-floors

FUZZTIME ?= 60s

fuzz:
	sh scripts/fuzz.sh $(FUZZTIME)

bench:
	go test -run '^$$' -bench . -benchmem -count=6 ./... | tee bench.txt

docs:
	go run ./tools/docsgen -out docs/diagnostics

docs-check:
	go run ./tools/docsgen -out docs/diagnostics -check

cover-floors:
	sh scripts/cover-floors.sh
