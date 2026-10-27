# PR Overlap Detector

<!-- 
Docs for Issue #59: Overlap-detector for audit PRs (post-mortem improvement #2)
D-refs: D48, D28
Post-mortem: architecture audit completion (#37–#46)
-->

## Purpose

The overlap detector prevents merge conflicts during rebase by identifying file/line-range overlaps between two PR branches *before* they occur. It is a post-mortem improvement from the architecture audit completion, where PR #55 (fix/39-queue-events-fan-out) and PR #56 (fix/45-dead-code-sweep) modified overlapping lines in `store/queue.go`, requiring multiple fix-up commits.

Per D48, this tool is used in pre-merge audit; it does not merge branches. Per D28, the event contract applies to multi-repo workflows — the detector works on any branch pair, even across repos when checked out locally.

## Script

**File:** `scripts/detect-pr-overlap.sh`

**Constraints:**
- Bash-only, no new dependencies (D48: draft PR only, no toolchain changes)
- Exits non-zero on overlaps, zero otherwise
- Takes two branch names + optional base; outputs overlapping files with line ranges

### Usage

```bash
./scripts/detect-pr-overlap.sh <branch1> <branch2> [base]
```

**Arguments:**
- `branch1` — First branch/PR (e.g., `origin/fix/39-queue-events-fan-out`)
- `branch2` — Second branch/PR (e.g., `origin/fix/45-dead-code-sweep`)
- `base` — Optional base commit (defaults to `git merge-base` of the two branches)

**Example:**
```bash
./scripts/detect-pr-overlap.sh origin/fix/39-queue-events-fan-out origin/fix/45-dead-code-sweep
```

**Output:**
```
Detecting overlaps: origin/fix/39-queue-events-fan-out vs origin/fix/45-dead-code-sweep (base: 5b9d5c0c)
OVERLAP: store/queue.go
  origin/fix/39-queue-events-fan-out: lines 186-193 (base)
  origin/fix/45-dead-code-sweep: lines 191-198 (base)
  INTERSECTION: lines 191-193

Overlaps found: 1 file(s)
```

Exit code 1 indicates overlaps detected; exit code 0 indicates no overlaps.

### Algorithm

1. Determine base commit (argument or `git merge-base`)
2. Get changed files for each branch vs base
3. For common files, extract `@@` hunk headers from `git diff`
4. Parse old-side line ranges from hunk headers
5. Check for range intersections
6. Report overlapping files with line ranges and intersection

The detector operates on the *base file* line numbers, not the branch tip line numbers, which makes overlaps comparable regardless of intervening changes.

## Integration

### Pre-rebase checklist

Add to your rebase workflow:

```bash
# Before rebasing branch A onto branch B
./scripts/detect-pr-overlap.sh branch-a branch-b
if [ $? -ne 0 ]; then
  echo "Overlap detected — review changes before rebasing"
  # Optional: open PRs for human review
fi
```

### CI Gate (optional)

The script can be added to CI for PR creation workflows:

```yaml
# Example GitHub Actions step
- name: Check PR overlap
  run: |
    ./scripts/detect-pr-overlap.sh "${{ github.base_ref }}" "${{ github.head_ref }}"
```

## Regression Test Cases

### PR #55 / PR #56

During the architecture audit, PR #55 (fix/39-queue-events-fan-out) and PR #56 (fix/45-dead-code-sweep) caused rebase conflicts:

- **Common file:** `store/queue.go`
- **Base:** `5b9d5c0c157172ff5c52051ac5800493f95c3d51`
- **Branch #55 change:** lines 186-193 (claim scan SQL rewrite with tier ordering)
- **Branch #56 change:** lines 191-198 (claim scan SQL rewrite with rankingSQL variable)
- **Intersection:** lines 191-193

**Verification:**
```bash
./scripts/detect-pr-overlap.sh origin/fix/39-queue-events-fan-out origin/fix/45-dead-code-sweep
# Expected: OVERLAP detected for store/queue.go, exit code 1
```

Both branches also modify `store/store.go` and `worker/worker.go` with *identical* changes (the timeout-sentinel fix committed independently). The detector correctly reports these as overlaps; they are not conflicts as the changes match byte-for-byte.

### No-overlap case

Test with branches that modify disjoint files:

```bash
./scripts/detect-pr-overlap.sh fix/39-queue-events-fan-out fix/40-ghe-branch-artifact-uris
# Expected: No overlapping line ranges found, exit code 0
```

## Limitations

- **Line-range only:** Detects line-range overlaps, not semantic conflicts where different changes happen on separate lines in the same function. For deep semantic overlap, use `git merge-tree` or manual review.
- **Base-relative:** Reports overlaps relative to the base commit. If the base is not the true common ancestor, results may be misleading.
- **No content comparison:** Does not detect when two branches make *identical* changes to overlapping lines (this is a non-conflict — git merges cleanly). For identical-change detection, compare patches.
- **Bash-only:** Parsing git diff with bash is brittle for exotic diff formats (binary files, renames). For production-critical detection, consider a language-native parser.

## D-refs

- **D48:** Draft PRs only, merge on explicit user instruction. The detector is a pre-merge audit tool; it does not authorise merges.
- **D28:** Event contract — multi-repo, config-driven polling. The detector works on any branches checked out locally, supporting the event-contract workflow where repos are configured, not inbound.

## Related

- [`docs/operations/build-gate.md`](./build-gate.md) — Pre-rebase build verification (Issue #58)
- Issue #59 — Overlap-detector for audit PRs (post-mortem improvement #2)
- Post-mortem from architecture audit (#37–#46): merge artifacts in commit `328c991` → fix-up `9dd8326`
