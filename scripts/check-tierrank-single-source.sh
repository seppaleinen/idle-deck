#!/usr/bin/env bash
# check-tierrank-single-source.sh
# Lint guard for TierRank single-source enforcement (D42, M4)
# Fails if tier ranking is defined locally instead of using queue.TierRank / queue.TierMetaTable

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
FILES="$ROOT_DIR/**/*.go"
QUEUE_DIR="$ROOT_DIR/queue"

echo "Checking for local tierRank definitions outside queue/..."
# Look for local tierRank functions
FOUND=0

# Find local tierRank function definitions outside queue/
while IFS= read -r -d '' file; do
    if [[ "$file" == *"$QUEUE_DIR"* ]]; then
        continue
    fi
    # Skip test files for tierRank to allow test stubs
    if [[ "$file" == *"_test.go" ]]; then
        continue
    fi
    if grep -qE 'func.*tierRank|func.*TierRank' "$file"; then
        while IFS= read -r line; do
            line_num=$(echo "$line" | cut -d: -f1)
            echo "FAIL: Local tierRank definition found in $file:$line_num"
            echo "  -- Offending line: $(echo "$line" | cut -d: -f2-)"
            FOUND=1
        done < <(grep -nE 'func.*tierRank|func.*TierRank' "$file")
    fi
done < <(find "$ROOT_DIR" -name "*.go" -not -path "*/queue/*" -not -path "*/.git/*" -print0)

echo "Checking for hardcoded CASE tier WHEN SQL literals outside queue/..."
# Find hardcoded CASE tier WHEN SQL
while IFS= read -r -d '' file; do
    if [[ "$file" == *"$QUEUE_DIR"* ]]; then
        continue
    fi
    # Skip test files for CASE pattern to allow test expectations
    if [[ "$file" == *"_test.go" ]]; then
        continue
    fi
    # Look for CASE tier WHEN patterns in SQL strings
    if grep -qE 'CASE tier WHEN.*THEN.*ELSE' "$file"; then
        while IFS= read -r line; do
            line_num=$(echo "$line" | cut -d: -f1)
            echo "FAIL: Hardcoded CASE tier WHEN found in $file:$line_num"
            echo "  -- Offending line: $(echo "$line" | cut -d: -f2-)"
            FOUND=1
        done < <(grep -nE 'CASE tier WHEN.*THEN.*ELSE' "$file")
    fi
done < <(find "$ROOT_DIR" -name "*.go" -not -path "*/queue/*" -not -path "*/.git/*" -print0)

if [ $FOUND -eq 1 ]; then
    echo ""
    echo "ERROR: TierRank single-source violation detected!"
    echo "All tier ranking must use queue.TierRank or queue.TierOrderingSQL()."
    echo "See docs/operations/tierrank-guard.md for details."
    exit 1
fi

echo "PASS: TierRank single-source check passed."
exit 0
