#!/usr/bin/env bash
# check-rebase-clean.sh
#
# Pre-rebase build-verification script for idle-deck.
# Runs before push to catch merge artifacts introduced during conflict resolution.
# Issue #58 — Pre-rebase build-verification script (post-mortem improvement #1)
# D-refs: D32, D48, D34, D9
# ADR 0017: CGO_ENABLED=0 asserted, static cgo-free binary
#
# Usage: ./scripts/check-rebase-clean.sh
# Exit code 0 = clean, non-zero = failure
# Called by .git/hooks/pre-push

set -euo pipefail

# ── Environment assertions ────────────────────────────────────────────────────
# ADR 0017 (D32): The artifact must be cgo-free and built with CGO_ENABLED=0.
# Assert the flag regardless of the caller's environment — avoid silent cgo
# re-introduction by a dependency and keep the gate honest.
export CGO_ENABLED=0

# Go version gate (D9)
# Require Go 1.27 per ADR 0010
REQUIRED_GO="1.27"
CURRENT_GO="$(go version | awk '{print $3}' | sed 's/go//')"
if ! printf '%s\n' "$REQUIRED_GO" "$CURRENT_GO" | sort -V -C; then
    echo "ERROR: Go version $CURRENT_GO < $REQUIRED_GO (ADR 0010, D9)" >&2
    exit 1
fi

# ── Working directory ────────────────────────────────────────────────────────
# The script must be invokable from any directory, e.g. a CI runner.
# Default to the script's parent repository root.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

echo "idle-deck build gate — $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "Go $CURRENT_GO (CGO_ENABLED=0) • repo $REPO_ROOT"

# ── Gate 1: go build ./... ────────────────────────────────────────────────────
# Per operator-surface §10: CI gate that proves it compiles.
# Must be a full package walk, not a single target.
echo "→ go build ./..."
if ! go build -v ./...; then
    echo "ERROR: go build ./... failed" >&2
    exit 1
fi

# ── Gate 2: go vet ./... ─────────────────────────────────────────────────────
# Cheap static analysis pass, per operator-surface §10.
echo "→ go vet ./..."
if ! go vet ./...; then
    echo "ERROR: go vet ./... failed" >&2
    exit 1
fi

# ── Gate 3: go test -count=1 ./... ───────────────────────────────────────────
# Runs the full suite exactly once (no test-cache skip) to verify
# regression-free after a merge/rebase.
echo "→ go test -count=1 ./..."
if ! go test -count=1 ./...; then
    echo "ERROR: go test -count=1 ./... failed" >&2
    exit 1
fi

echo "✓ build gate passed"
exit 0
