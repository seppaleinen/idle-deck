#!/usr/bin/env bash
# detect-pr-overlap.sh
#
# Overlap-detector for audit PRs — finds line-range overlaps between two branches.
# Issue #59 — Overlap-detector for audit PRs (post-mortem improvement #2)
# D-refs: D48, D28
#
# Usage: ./scripts/detect-pr-overlap.sh <branch1> <branch2> [base]
#   branch1   First branch name (PR branch)
#   branch2   Second branch name (PR branch)
#   base      Optional base commit (default: merge-base of branch1 and branch2)
#
# Output: overlapping files with line ranges, one per line
# Exit code: 0 if no overlaps, 1 if overlaps found
#
# Constraints: Bash-only, no new dependencies
# D48: Draft PRs only — this script is for pre-merge audit only

set -euo pipefail

usage() {
    cat <<EOF
Usage: $0 <branch1> <branch2> [base]

Detect overlapping file/line ranges between two PR branches.

Arguments:
  branch1   First branch name
  branch2   Second branch name
  base      Optional base commit (default: merge-base of branch1 and branch2)

Exit code: 0 if no overlaps found, 1 if overlaps found.

Example:
  $0 origin/fix/39-queue-events-fan-out origin/fix/45-dead-code-sweep
EOF
}

# ── Arguments ────────────────────────────────────────────────────────────────

if [ $# -lt 2 ] || [ $# -gt 3 ]; then
    usage
    exit 2
fi

BRANCH1="$1"
BRANCH2="$2"
BASE="${3:-}"

# Resolve base commit
if [ -z "$BASE" ]; then
    if ! BASE=$(git merge-base "$BRANCH1" "$BRANCH2" 2>/dev/null); then
        echo "ERROR: Could not find common base for $BRANCH1 and $BRANCH2" >&2
        exit 2
    fi
fi

# Validate refs exist
if ! git rev-parse --verify "$BRANCH1" >/dev/null 2>&1; then
    echo "ERROR: Branch $BRANCH1 not found" >&2
    exit 2
fi
if ! git rev-parse --verify "$BRANCH2" >/dev/null 2>&1; then
    echo "ERROR: Branch $BRANCH2 not found" >&2
    exit 2
fi
if ! git rev-parse --verify "$BASE" >/dev/null 2>&1; then
    echo "ERROR: Base $BASE not found" >&2
    exit 2
fi

# ── Helpers ───────────────────────────────────────────────────────────────────

# parse_hunk_old_range: extract start,count from a git diff hunk header.
# Input: "@@ -123,4 +456,7 @@ context"
# Output: "start count" (e.g., "123 4")
# Handles: pure additions (0,0), pure deletions (old_count,0), single-line (no comma).
parse_hunk_old_range() {
    local hunk_line="$1"
    local range_part
    range_part=$(printf '%s' "$hunk_line" | sed -n 's/^@@ //p' | sed -n 's/ @@.*$//p')
    # range_part: "-123,4 +456,7"
    local old_range
    old_range=$(printf '%s' "$range_part" | sed 's/.*-\([^ ]*\) .*/\1/')
    
    local old_start old_count
    
    if printf '%s' "$old_range" | grep -q ','; then
        old_start=$(printf '%s' "$old_range" | cut -d',' -f1)
        old_count=$(printf '%s' "$old_range" | cut -d',' -f2)
    else
        old_start="$old_range"
        old_count=1
    fi
    
    # Pure additions: old range is 0,0 (no lines in base to change)
    if [ "$old_start" = "0" ]; then
        echo "0 0"
        return
    fi
    
    # Pure deletions or single-line edge case
    if [ -z "$old_count" ] || [ "$old_count" = "0" ]; then
        old_count=1
    fi
    
    echo "$old_start $old_count"
}

# Check if two ranges [s1, e1] and [s2, e2] overlap (inclusive)
ranges_overlap() {
    local s1=$1 e1=$2 s2=$3 e2=$4
    [ "$s1" -le "$e2" ] && [ "$s2" -le "$e1" ]
}

# ── Main logic ────────────────────────────────────────────────────────────────

echo "Detecting overlaps: $BRANCH1 vs $BRANCH2 (base: ${BASE:0:8})" >&2

# Get changed files for each branch
mapfile -t files1 < <(git diff --name-only "$BASE" "$BRANCH1" 2>/dev/null | sort)
mapfile -t files2 < <(git diff --name-only "$BASE" "$BRANCH2" 2>/dev/null | sort)

# Handle empty file lists
if [ ${#files1[@]} -eq 0 ] || [ "${files1[0]}" = "" ]; then
    echo "No files changed in $BRANCH1 vs base — no overlaps possible." >&2
    exit 0
fi
if [ ${#files2[@]} -eq 0 ] || [ "${files2[0]}" = "" ]; then
    echo "No files changed in $BRANCH2 vs base — no overlaps possible." >&2
    exit 0
fi

# Find common files using a temp file and sort for O(n) lookup
tmp_files1=$(mktemp)
tmp_files2=$(mktemp)
trap 'rm -f "$tmp_files1" "$tmp_files2"' EXIT

printf '%s\n' "${files1[@]}" | sort > "$tmp_files1"
printf '%s\n' "${files2[@]}" | sort > "$tmp_files2"

common_files=()
while IFS= read -r f; do
    if grep -qxF "$f" "$tmp_files2"; then
        common_files+=("$f")
    fi
done < "$tmp_files1"

if [ ${#common_files[@]} -eq 0 ]; then
    echo "No common files changed — no overlaps possible."
    exit 0
fi

# ── Per-file overlap detection ────────────────────────────────────────────────

overlap_found=false
overlap_count=0

for file in "${common_files[@]}"; do
    # Extract changed line ranges for each branch.
    # We use the normal diff (with context) and parse @@ hunk headers.
    # The old-side range tells us which lines of the base file were touched.
    
    # Collect old-side ranges: "start count" per line
    ranges_b1=()
    while IFS= read -r hunk; do
        read -r s c < <(parse_hunk_old_range "$hunk")
        if [ "$s" != "0" ] || [ "$c" != "0" ]; then
            ranges_b1+=("$s $c")
        fi
    done < <(git diff "$BASE" "$BRANCH1" -- "$file" 2>/dev/null | grep '^@@' || true)
    
    ranges_b2=()
    while IFS= read -r hunk; do
        read -r s c < <(parse_hunk_old_range "$hunk")
        if [ "$s" != "0" ] || [ "$c" != "0" ]; then
            ranges_b2+=("$s $c")
        fi
    done < <(git diff "$BASE" "$BRANCH2" -- "$file" 2>/dev/null | grep '^@@' || true)
    
    # Skip if one side has no changes (shouldn't happen, but defensive)
    [ ${#ranges_b1[@]} -eq 0 ] && continue
    [ ${#ranges_b2[@]} -eq 0 ] && continue
    
    # Convert start+count to start+end (inclusive)
    file_has_overlap=false
    for r1 in "${ranges_b1[@]}"; do
        read -r s1 c1 <<< "$r1"
        e1=$((s1 + c1 - 1))
        for r2 in "${ranges_b2[@]}"; do
            read -r s2 c2 <<< "$r2"
            e2=$((s2 + c2 - 1))
            if ranges_overlap "$s1" "$e1" "$s2" "$e2"; then
                # Found overlap in this file
                overlap_found=true
                file_has_overlap=true
                overlap_count=$((overlap_count + 1))
                # Compute intersection
                overlap_start=$((s1 > s2 ? s1 : s2))
                overlap_end=$((e1 < e2 ? e1 : e2))
                echo "OVERLAP: $file"
                echo "  $BRANCH1: lines $s1-$e1 (base)"
                echo "  $BRANCH2: lines $s2-$e2 (base)"
                echo "  INTERSECTION: lines $overlap_start-$overlap_end"
                echo ""
            fi
        done
        if [ "$file_has_overlap" = true ]; then
            break
        fi
    done
done

# Cleanup
rm -f "$tmp_files1" "$tmp_files2"

# ── Summary ──────────────────────────────────────────────────────────────────

if [ "$overlap_found" = true ]; then
    echo "Overlaps found: $overlap_count file(s)" >&2
    exit 1
else
    echo "No overlapping line ranges found."
    exit 0
fi
