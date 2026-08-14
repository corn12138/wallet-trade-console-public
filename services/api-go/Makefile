.PHONY: tidy vet test build run docker lint

GO ?= go
BIN ?= bin/api
PKG := ./...

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet $(PKG)

test:
	$(GO) test -race -count=1 $(PKG)

build:
	$(GO) build -o $(BIN) ./cmd/api

run:
	$(GO) run ./cmd/api

# Requires golangci-lint installed: https://golangci-lint.run/welcome/install
lint:
	golangci-lint run

docker:
	docker build -f Dockerfile -t wallet-trade-console/api-go:dev ../..
