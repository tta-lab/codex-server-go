.PHONY: help generate fmt vet lint test build ci check-generate

help:
	@echo "Available commands:"
	@echo "  make generate       - Generate Go types from JSON Schema"
	@echo "  make fmt            - Format code with gofmt"
	@echo "  make vet            - Run go vet"
	@echo "  make lint           - Run golangci-lint (if installed)"
	@echo "  make test           - Run tests"
	@echo "  make build          - Build all packages"
	@echo "  make ci             - Run all CI checks (fmt, generate, vet, lint, test, build)"
	@echo "  make check-generate - Verify generated files are up-to-date"

generate:
	go run ./cmd/codegen

fmt:
	@gofmt -w -s .

vet:
	go vet ./...

lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		echo "golangci-lint not installed, skipping. Install: https://golangci-lint.run/usage/install/"; \
	fi

test:
	go test ./...

build:
	go build ./...

ci: fmt generate vet lint test build
	@echo "CI checks passed"

# Verify generated files are up-to-date (used in CI)
check-generate:
	@if [ -n "$$(git diff --name-only)" ]; then \
		echo "Generated files are out of date. Run 'make generate' and commit."; \
		git diff --stat; \
		exit 1; \
	fi
