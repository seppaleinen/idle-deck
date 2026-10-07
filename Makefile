# idle-deck Makefile
# Convenience targets per operator-surface §10

.PHONY: dev test build vet clean

# Default target
all: build

# Run both stubs and daemon for local development
# Per operator-surface §9:
#   IDLE_DECK_POLL_INTERVAL=2s \
#   IDLE_DECK_REPOS=acme/widgets \
#   IDLE_DECK_GITHUB_API=http://127.0.0.1:9099 \
#   IDLE_DECK_HARNESS_URL=http://127.0.0.1:9098 \
#   IDLE_DECK_HARNESS_TOKEN=stub \
#   IDLE_DECK_DB=/tmp/idle-deck-dev.db \
#   make dev
dev:
	@echo "Starting tracker stub on :9099 and harness stub on :9098..."
	@go run ./cmd/stubs

# Run all tests including golden path integration test
test:
	@go test ./... -count=1 -timeout 60s

# Build all packages. CGO_ENABLED=0 is asserted, not inherited: a dependency
# that reintroduces cgo would silently break go install for users, so the
# build gate must not depend on the environment being clean (ADR 0017).
build:
	CGO_ENABLED=0 go build ./...

# Static analysis
vet:
	@go vet ./...

# Clean build artifacts
clean:
	@go clean -cache -testcache
	@rm -f idle-deck