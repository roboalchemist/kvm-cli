.PHONY: all build clean test test-smoke test-unit test-integration install dev-install deps fmt lint man docs-gen release-snapshot release check help

BINARY_NAME=kvm-cli
VERSION=$(shell git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD 2>/dev/null || echo dev)
LDFLAGS=-ldflags "-s -w -X main.version=$(VERSION)"

PLATFORMS=darwin/amd64 darwin/arm64 linux/amd64 linux/arm64

all: build

build:
	@echo "Building $(BINARY_NAME) $(VERSION)..."
	go build $(LDFLAGS) -o $(BINARY_NAME) .

clean:
	rm -f $(BINARY_NAME)
	rm -rf dist/
	rm -f coverage.out
	go clean

# Cross-compile for all target platforms
build-all:
	@echo "Cross-compiling for all platforms..."
	@mkdir -p dist
	@for platform in $(PLATFORMS); do \
		GOOS=$$(echo $$platform | cut -d/ -f1); \
		GOARCH=$$(echo $$platform | cut -d/ -f2); \
		output="dist/$(BINARY_NAME)_$${GOOS}_$${GOARCH}"; \
		echo "  $$GOOS/$$GOARCH -> $$output"; \
		CGO_ENABLED=0 GOOS=$$GOOS GOARCH=$$GOARCH go build $(LDFLAGS) -o $$output . || exit 1; \
	done
	@echo "Done."

# Top-level test target = smoke tests (no credentials needed).
test: test-smoke

test-smoke:
	@if command -v go >/dev/null 2>&1; then \
		$(MAKE) --no-print-directory build; \
	else \
		echo "NOTE: go not in PATH; using pre-built ./$(BINARY_NAME) if present"; \
	fi
	@bash scripts/smoke_test.sh

# Unit tests are hermetic: integration_test.go carries a `//go:build integration`
# tag so it is never compiled here, and the scope is limited to ./pkg/... and
# ./cmd/... to keep the run fast and free of device calls.
test-unit:
	@echo "=== $(BINARY_NAME) unit tests ==="
	go test -race -coverprofile=coverage.out -covermode=atomic ./pkg/... ./cmd/...
	@if [ -s coverage.out ]; then \
		echo ""; \
		echo "=== Coverage Summary ==="; \
		go tool cover -func=coverage.out | tail -1; \
	fi

# The integration suite lives behind the `integration` build tag.
test-integration:
	@echo "=== $(BINARY_NAME) integration tests (READONLY=$${READONLY:-0}) ==="
	go test -v -tags integration -run Integration -count=1 -timeout 15m ./...

install: build
	sudo install -m 755 $(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)

dev-install: build
	sudo ln -sf $(PWD)/$(BINARY_NAME) /usr/local/bin/$(BINARY_NAME)

deps:
	go mod download
	go mod tidy

fmt:
	go fmt ./...

lint:
	golangci-lint run

# Generate man pages via cobra/doc
man:
	@echo "Generating man pages..."
	@mkdir -p docs/man
	go run ./cmd/gendocs man docs/man
	@echo "Man pages generated in docs/man/"

# Generate markdown CLI reference via cobra/doc
docs-gen:
	@echo "Generating markdown docs..."
	@mkdir -p docs/cli
	go run ./cmd/gendocs markdown docs/cli
	@echo "CLI docs generated in docs/cli/"

# Release via GoReleaser (local snapshot for testing).
release-snapshot:
	goreleaser release --snapshot --clean

# Full release (triggered by git tag, usually via CI).
release:
	goreleaser release --clean

check: fmt lint test test-unit

help:
	@echo "Available targets for $(BINARY_NAME):"
	@echo "  build            - Build the binary with version ldflags"
	@echo "  build-all        - Cross-compile for darwin/linux amd64/arm64"
	@echo "  clean            - Remove build artifacts"
	@echo "  test             - Run smoke tests (no credentials needed)"
	@echo "  test-smoke       - Run smoke tests (--help, --version, docs, completion, skill)"
	@echo "  test-unit        - Run unit tests with -race and coverage"
	@echo "  test-integration - Run integration tests (requires KVM_URL; READONLY=1 skips writes)"
	@echo "  install          - Install to /usr/local/bin/"
	@echo "  dev-install      - Symlink binary for development"
	@echo "  deps             - Download and tidy dependencies"
	@echo "  fmt              - Format Go code"
	@echo "  lint             - Run golangci-lint"
	@echo "  man              - Generate man pages via cobra/doc"
	@echo "  docs-gen         - Generate markdown CLI reference via cobra/doc"
	@echo "  release-snapshot - Local GoReleaser test build"
	@echo "  release          - Full GoReleaser release"
	@echo "  check            - Run fmt, lint, test, test-unit"
	@echo "  help             - Show this help"
