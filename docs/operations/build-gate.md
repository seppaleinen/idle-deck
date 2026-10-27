# Build Gate — Pre-rebase Verification

<!--
Docs for Issue #58: Pre-rebase build-verification script (post-mortem improvement #1)
D-refs: D48, D32, D34, D9
ADR 0017 (artifact): CGO_ENABLED=0, static cgo-free binary
-->

## What it does

The build gate runs `go build ./...`, `go vet ./...`, and `go test -count=1 ./...` before a push.
It catches merge artifacts introduced during conflict resolution — e.g., the syntax error in
`store/queue.go` that appeared in commit `328c991` and required fix-up commit `9dd8326` to repair.

Per the post-mortem, this prevents syntax errors and broken code from being committed during a
rebase. Mid-rebase commits are individually valid; the failure mode is *merge artifacts*, which
only exist at push time. Therefore the gate uses a **pre-push** hook, not a pre-commit hook.

## Files

| File | Role |
|---|---|
| `scripts/check-rebase-clean.sh` | The build gate script. Executable, portable, assertions-first. |
| `scripts/git-hooks/pre-push` | Tracked template for the git pre-push hook. Copy to `.git/hooks/pre-push` to enable the gate. |
| `docs/operations/build-gate.md` | This document. |

## Why pre-push, not pre-commit

- Pre-commit runs per-commit during a rebase. Mid-rebase commits are often incomplete merge
  resolution scaffolding and are expected to fail.
- Pre-push runs once per `git push`, after the entire rebase is complete. The failure mode this
  gate targets — merge artifacts, stray whitespace, missing newlines — only exists when the
  branch is a coherent whole.
- The hook is **opt-in per developer** (git hooks live in `.git/hooks/` in the working tree, not
  the repository). This is a speed/convenience layer, not a replacement for CI. CI already gates
  server-side (`go build`, `go vet`, `go test`, D-id check, smoke test — operator-surface §10).

## Install

This is a per-developer opt-in.

```bash
# Clone the repo, then:
cd idle-deck

# Copy the hook template to your local git hooks directory
cp scripts/git-hooks/pre-push .git/hooks/pre-push
chmod +x .git/hooks/pre-push

# Verify the script is executable
chmod +x scripts/check-rebase-clean.sh

# Test the gate locally (should pass on main)
scripts/check-rebase-clean.sh
```

The hook template is tracked in `scripts/git-hooks/pre-push`. Git hooks live in
`.git/hooks/` in the working tree and are **not committed** by design. Install the hook
locally; CI remains the authoritative gate. If `core.hooksPath` is set, place the hook
there instead.

## Workflow integration

1. **Local development**: `git rebase`/`git merge` as normal.
2. **Before `git push`**: the pre-push hook runs automatically. If a merge artifact broke
   compilation or tests, the push is blocked with a non-zero exit and an explanatory message.
3. **Fix and retry**: resolve the errors, amend the commit if needed, and push again.

### Example failure

```
pre-push: running build gate...
idle-deck build gate — 2026-10-27T09:15:00Z
Go 1.27.3 (CGO_ENABLED=0) • repo /Users/seppa/workspace/idle-deck
→ go build ./...
store/queue.go:201:7: expected declaration, found 'if'
ERROR: go build ./... failed
```

The merge introduced a missing newline — exactly the class of bug this gate catches.

## Script contract

`scripts/check-rebase-clean.sh` guarantees:

- **CGO_ENABLED=0 asserted** (ADR 0017, D32). The script exports `CGO_ENABLED=0` and verifies
  `go env CGO_ENABLED == 0` before any build step. The gate must not depend on a clean
  environment — a dependency that reintroduces cgo would silently break `go install` for users.
- **Go 1.27 required** (D9, ADR 0010). The script aborts if `go version < 1.27`.
- **Repository-root invariant-free invocation**. The script `cd`s to the repository root
  derived from its own location, so it can be invoked from any directory.
- **Sequential steps with early exit**:
  1. `go build -v ./...` — proves it compiles.
  2. `go vet ./...` — cheap static analysis.
  3. `go test -count=1 ./...` — full suite, no test-cache skip, verifies regression-freedom.
- **Exit 0 on success, non-zero on any failure**. Git hooks interpret non-zero as "block push".

## Regression-test guidance

When you change the gate, prove it still works:

1. Verify it passes on current `main`:
   ```bash
   scripts/check-rebase-clean.sh
   ```
2. Introduce a controlled failure — e.g., add a syntax error to `main.go` — commit locally,
   run the script, confirm it exits non-zero. Revert the change.
   ```bash
   git checkout -- main.go
   ```
3. Verify the pre-push hook wraps the script correctly:
   ```bash
   .git/hooks/pre-push
   # Should print "pre-push: running build gate..." and then the script output.
   ```
4. Check D-id citations: `grep -oh 'D[0-9]\+'` must resolve. The document cites D48, D32,
   D34, D9 — all defined in `AGENTS.md`.

## D-refs

- **D48**: Draft PRs only, merge on explicit user instruction. The build gate is a quality gate,
  not a merge gate. It never authorises a merge.
- **D32 / ADR 0017**: One static cgo-free binary, `go install`. The script asserts
  `CGO_ENABLED=0` so the gate itself cannot mask a cgo dependency.
- **D34**: MVP configuration is env vars + CLI flags only — no YAML. The script reads no
  configuration file; it inherits the process environment, consistent with D34's env-first
  configuration posture for scripts. Environment variables are not logged or redacted by the
  script.
- **D9 / ADR 0010**: Go 1.27. The script enforces the required toolchain version.

## Limitations

- **Opt-in**: git hooks are local. New clones will not have the hook unless the developer
  copies it. CI remains the authoritative gate.
- **Local only**: The script uses `go test ./...` which requires the local module cache and
  the same Go version as the CI environment. This is intentional — it catches *local*
  environment drift.
- **Does not replace CI**: CI runs D-id check, smoke tests, and the conformance suite C1–C9.
  The pre-push hook is a developer speed tool. A green local gate is necessary but not
  sufficient.

## Related

- [`docs/operations/operator-surface.md`](../operations/operator-surface.md) §9–10: local
  development, `make dev`/`make test`/`make build`, CI gates.
- [`docs/adr/0017-the-artifact.md`](../adr/0017-the-artifact.md) — static cgo-free binary, build
  gates.
- Issue #58 — Pre-rebase build-verification script (post-mortem improvement #1).
- Post-mortem from architecture audit completion (#37–#46): merge artifacts in commit
  `328c991` → fix-up `9dd8326`.
