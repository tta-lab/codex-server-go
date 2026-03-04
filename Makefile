.PHONY: generate vet test build ci clean

generate:
	python3 cmd/codegen/main.py

vet:
	go vet ./...

test:
	go test ./...

build:
	go build ./...

ci: generate vet test build
	@echo "CI checks passed"

# Verify generated files are up-to-date (used in CI)
check-generate: generate
	@if [ -n "$$(git diff --name-only)" ]; then \
		echo "Generated files are out of date. Run 'make generate' and commit."; \
		git diff --stat; \
		exit 1; \
	fi
