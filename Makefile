SHELL := /usr/bin/env bash
.SHELLFLAGS := -euo pipefail -c
.DEFAULT_GOAL := check

BINARY := bin/local-inference-router
GOVULNCHECK := golang.org/x/vuln/cmd/govulncheck@v1.8.0

.PHONY: build fmt fmt-check vet test test-race vuln check validate-example docker-build

build:
	go build -trimpath -o $(BINARY) ./cmd/local-inference-router

fmt:
	gofmt -w .

fmt-check:
	@unformatted="$$(gofmt -l .)"; \
	if [[ -n "$$unformatted" ]]; then printf 'gofmt needed:\n%s\n' "$$unformatted"; exit 1; fi; \
	printf 'gofmt: clean\n'

vet:
	go vet ./...

test:
	go test ./...

# The race detector needs cgo, which needs a C toolchain.
test-race:
	CGO_ENABLED=1 go test -race ./...

vuln:
	go run $(GOVULNCHECK) ./...

check: fmt-check vet test test-race vuln build

validate-example:
	go run ./cmd/local-inference-router validate -config examples/config.yaml

docker-build:
	docker build -t local-inference-router:dev .
