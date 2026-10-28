# TierRank Single-Source Guard

## Purpose

This guard prevents tier ranking from being defined locally instead of using the single source `queue.TierRank` / `queue.TierMetaTable` (D42, M4). Violations are caught statically via grep, without adding a Go lint dependency.

## Background

During architecture audit, the fan-out fix (#55) was authored with a local `tierRank()` closure in `store/queue.go` before PR #51 introduced `queue.TierRank` and `queue.TierMetaTable` as the single source. The mismatch was only discovered mid-rebase, requiring a fix-up commit (38945a7).

This guard ensures that all tier ranking logic derives from the single source of truth, preventing similar drift.

## What is checked

The guard `scripts/check-tierrank-single-source.sh` greps for:

1. **Local tierRank functions**: `func.*tierRank|func.*TierRank` outside `queue/`
   - Test files (`*_test.go`) are excluded to allow test stubs.
   - Local implementations must be replaced with calls to `queue.TierRank()`.

2. **Hardcoded CASE tier WHEN SQL**: Hardcoded `CASE tier WHEN` SQL literals outside `queue/`
   - Test files are excluded to allow test expectations.
   - Production code must use `queue.TierOrderingSQL()` instead.

## Usage

Run locally:

```bash
scripts/check-tierrank-single-source.sh
```

Exits with code 0 if clean, 1 if violations are found with file/line references.

## CI Integration

The guard runs in CI after the D-id checker:

```yaml
- name: TierRank single-source lint
  run: scripts/check-tierrank-single-source.sh
```

## Fixing violations

If the guard fails:

1. Replace local `tierRank` functions with `queue.TierRank(tier)` calls.
2. Replace hardcoded `CASE tier WHEN` SQL with `queue.TierOrderingSQL()`.
3. Re-run the guard to confirm.

## Related decisions

- **D42**: TierMetaTable single source (via PR #51 / M4)
- **D18**: Go interface naming conventions
- **M4**: Single source for tier metadata
- **M3**: Tier metadata table initialization
- **L7**: Tier ordering in claim scan
