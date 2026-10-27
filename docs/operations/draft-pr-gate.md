# Draft PR Build Gate

## Policy

Per post-mortem improvement #4 (Issue #61) and D48:

- A Draft PR is only opened after `go build ./...` and `go test ./...` pass locally.
- This moves verification earlier in the D48 workflow so broken branches are never pushed.

## Enforcement

1. **Pre-push hook**: `scripts/check-rebase-clean.sh` runs `go build`, `go vet`, `go test` before push.
2. **CI fast-fail**: `.github/workflows/ci.yml` runs `go build ./...` before the full test suite.
3. **Documentation**: This file serves as the D48 workflow amendment.

## D-refs

- D48: Merge policy — Draft PRs opened (visible, reviewable, unlanded); merge on explicit user instruction after `go test` and conformance pass.
- Post-mortem improvement #4: Draft PR build gate enforcement (Issue #61)

Related issues:
- #58: Pre-rebase build-verification script
- #61: Draft PR build gate enforcement
