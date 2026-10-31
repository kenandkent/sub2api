.PHONY: build build-embed generate wire test test-unit test-integration test-e2e test-e2e-local test-cover-integration clean-coverage clean

VERSION ?= $(shell ./scripts/resolve-version.sh)
LDFLAGS ?= -s -w -X main.Version=$(VERSION)
TEST_UNIT_FLAGS ?= -count=1
TEST_INTEGRATION_FLAGS ?= -count=1
TEST_E2E_FLAGS ?= -v -count=1 -timeout=300s

build:
	CGO_ENABLED=0 go build -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

# Requires `pnpm --dir frontend run build` from the repository root first.
build-embed:
	CGO_ENABLED=0 go build -tags embed -ldflags="$(LDFLAGS)" -trimpath -o bin/server ./cmd/server

wire:
	cd cmd/server && go generate

generate:
	go generate ./ent
	go generate ./cmd/server

test:
	go test ./...
	golangci-lint run ./...

test-unit:
	go test -tags=unit $(TEST_UNIT_FLAGS) ./...

test-integration:
	go test -tags=integration $(TEST_INTEGRATION_FLAGS) ./...

test-e2e:
	@echo "E2E requires a running disposable server; configured gateway keys can call real upstreams. See deploy/LOCAL_DEVELOPMENT.md."
	go test -tags=e2e $(TEST_E2E_FLAGS) ./internal/integration/...

test-e2e-local: test-e2e

test-cover-integration:
	go test -tags=integration -cover -coverprofile=coverage.out -count=1 -race -parallel=8 ./...
	go tool cover -func=coverage.out | tail -1
	go tool cover -html=coverage.out -o coverage.html

clean-coverage:
	rm -f coverage.out coverage.html

clean: clean-coverage
	rm -rf bin/
