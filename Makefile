GOCACHE ?= /tmp/replicon-gocache
GOMODCACHE ?= /tmp/replicon-gomodcache
GO_ENV = GOCACHE=$(GOCACHE) GOMODCACHE=$(GOMODCACHE)
BIN ?= bin/replicon
IMAGE ?= replicon:local
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS = -s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

.PHONY: test test-race test-integration test-all bench lint build fmt tidy docker-build package-release clean

test:
	$(GO_ENV) go test ./...

test-race:
	$(GO_ENV) go test -race -count=1 ./...

test-integration:
	$(GO_ENV) go test -race -tags integration -count=1 -v ./internal/replication/

test-all: lint test-race test-integration

bench:
	$(GO_ENV) go test -bench=. -benchmem -run='^$$' ./internal/replication/

lint:
	$(GO_ENV) go vet ./...
	@if command -v golangci-lint >/dev/null 2>&1; then \
		$(GO_ENV) golangci-lint run ./...; \
	else \
		echo "golangci-lint not found, skipping (install: https://golangci-lint.run/welcome/install/)"; \
	fi

build:
	mkdir -p bin
	$(GO_ENV) go build -trimpath -ldflags='$(LDFLAGS)' -o $(BIN) .

fmt:
	gofmt -w main.go $$(find internal -name '*.go' | sort)

tidy:
	$(GO_ENV) go mod tidy

docker-build:
	docker build -t $(IMAGE) .

package-release:
	bash scripts/package-release.sh

clean:
	rm -rf bin dist
