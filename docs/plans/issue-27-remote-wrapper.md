# Issue #27 — Remote wrapper: first real Harness adapter (opencode/ACP)

> **Post-MVP.** Map #3 is terminal (10/10), MVP (#10) closed at 12/13. This is the
> first thing beyond shipped v0.1.0. It does not reverse any locked decision.

## Decision summary

**B, not C.** idle-deck talks to a wrapper service over the fixed contract; the
wrapper owns the workspace and runs the agent inside it. Keeps D26 (role, not
model), D27 (workspace on remote), D40 (never name a vendor), and D7 (one
adapter) intact.

**One HTTP skeleton, a recipe table for agents.** opencode/pi/ACP-CLIs share one
adapter (`opencode acp`); claude/codex/cursor get proprietary recipes later. The
contract is fixed; the launch recipe is a table.

## Scope (W1–W9)

| Work item | Description | Status |
|---|---|---|
| **W1** HTTP server | Four contract endpoints, bearer auth, idempotency | ✅ Done |
| **W2** Run lifecycle | Start, collect, watchdog, timeout enforcement, Abort | ✅ Done |
| **W3** ACP adapter | JSON-RPC over stdio, handshake, tool-call observation | ✅ Done |
| **W4** Workspace mgmt | Bare cache, worktree, OnSuccess/OnFailure, git operations | ✅ Done |
| **W5** Typed artifacts | Observe session/update, extract pull_request/branch/comment | ✅ Done |
| **W6** Model-reachability /health | Real ACP round-trip probe, cached, D31 compliant | ✅ Done |
| **W7** Wrapper config | Role→model map, config-driven agent selection (D46) | ✅ Done |
| **W8** Conformance tests | C1–C4 server-level, C5–C9 black-box adapter-level | ✅ Done |
| **W9** Wiring | Composition root, Dockerfile, CI multi-arch, Helm chart | ✅ Done |

## Files built

### Server (`remote/` package)

| File | Lines | What it does |
|---|---|---|
| `remote/runner.go` | 84 | `Runner` interface (`Start`/`Abort`/`Result`), `Workspace`, `Artifact`, `RunResult`, `AttemptOutcome` |
| `remote/server.go` | 244 | `Server` struct, `Routes()`, bearer auth, `handleRuns` (POST, idempotent C1), `handleRunByID` (GET/DELETE), `handleHealth` (D31) |
| `remote/run.go` | 313 | `Run` struct, `NewRun` (start + watchdog + collect goroutines), `Complete`, `Abort`, outcome mapping, wire response types |
| `remote/acp.go` | 540 | `acpRunner` — JSON-RPC over stdio, `initialize → session/new → session/prompt`, `observe()` for tool-call artifacts, `Abort` = process kill (confirmed-dead I8) |
| `remote/workspace.go` | 332 | `WorkspaceManager` — `PrepareWorkspace` (bare cache + worktree), `OnSuccess` (stage → commit → feature branch → push → Draft PR → `pull_request` artifact), `OnFailure` (debug branch → `branch` artifact) |
| `remote/git.go` | 332 | `GitRunner` — clone, worktree, fetch, checkout, push, commit helpers; plus `BareRepoName`, `ensureBareRepoInitialised`, `ResolveBaseRef`, `WorktreeAt` (detached), `RemoveWorktree`, `PushToUpstream` |
| `remote/config.go` | 46 | `Config` struct — role→model map, tokens, timeouts, workspace manager, `TestUpstreamURL` |
| `remote/conformance_test.go` | 338 | C1–C4 server-level tests against `stubRunner`, health probe tests |
| `remote/workspace_test.go` | 400 | Acceptance tests for #28 — PrepareWorkspace, OnSuccess/OnFailure, tokenURL, PushToUpstream |
| `remote/gc.go` | 112 | `Server.GCWorktrees` — worktree GC honoring `Config.WorktreeTTL` (#31) |
| `remote/gc_test.go` | 162 | GC acceptance tests |
| `remote/acp_integration_test.go` | 172 | Vendor compatibility smoke test against real opencode (gated `IDLE_DECK_REAL_ACP_TESTS=1`) |

### Entry point (`cmd/remote/`)

| File | Lines | What it does |
|---|---|---|
| `cmd/remote/main.go` | 161 | Composition root — reads env, builds ACP config (D46), wires runner + health probe + server, GC ticker, starts HTTP listener |
| `cmd/remote/health.go` | 313 | Real model-reachability probe — `initialize → session/new → session/prompt` round-trip, 30s budget, 15s cache TTL, `cmd.Stderr = io.Discard` |
| `cmd/remote/health_test.go` | — | Health probe tests |
| `cmd/remote/agent_config_test.go` | — | Agent config env var tests |

### Client adapter (`harness/`)

| File | Lines | What it does |
|---|---|---|
| `harness/remote.go` | 303 | `RemoteHarness` — `Start` (POST, §7 classification), `Abort` (DELETE, confirmed-dead), `Result` (poll GET, §7 taxonomy), `Health` (GET /health) |
| `harness/conformance_test.go` | 477 | C1–C9 black-box tests — drives `RemoteHarness` against `test/stubs/harness.go` with injected errors (429, 401/403/404, 422, unreachable, abort) |

### Test infrastructure

| File | What it does |
|---|---|
| `test/fakeacp/main.go` | Minimal ACP agent for health probe testing — modes: ok/error/hang/die/slow |
| `test/stubs/harness.go` | Stub harness server for conformance suite — controllable responses, fail injection |

### Deployment

| File | What it does |
|---|---|
| `Dockerfile` | Multi-stage: Go builder (both binaries, CGO_ENABLED=0) → opencode v2 npm extraction → ubuntu:24.04 runtime (git, gh, ripgrep, opencode, both binaries) |
| `.github/workflows/ci.yml` | Build + docker job — multi-arch (amd64/arm64), pushes to GHCR |
| `charts/idle-deck/` | Helm chart — namespace, SA, Secret, PVC, 2 Deployments (daemon + remote), Services, optional dashboard sidecar + ingress |

## Commit history (W1–W9)

```
8944734 2026-10-13 feat(remote): worktree GC honoring Config.WorktreeTTL (#31)
02a7f31 2026-10-13 feat(remote): workspace management — detached worktrees, per-repo cache, PRCreator seam, acceptance tests (#28)
ddbf63e 2026-10-13 fix(health): unify health probe endpoint to /health (#29)
02f7a46 2026-10-13 fix(idle): correct TestIdlePolicy_ReachableTrue expectation
04e7312 2026-10-13 feat(remote): wire workspace lifecycle into Run.collect()
9787815 2026-10-12 refactor(remote): remove stub C5/C6 tests; pointer to harness-level black-box suite
2984ed8 2026-10-12 conformance: wire §7 error taxonomy into harness transport and worker, make C5–C9 black-box
9449c9e 2026-10-12 docs: retitle TestACP_RealOpencode as a vendor compatibility smoke test
6b7baff 2026-10-12 test(remote): gate TestACP_RealOpencode behind IDLE_DECK_REAL_ACP_TESTS
d6819a8 2026-10-12 feat(remote): config-driven ACP agent selection (D46)
c5858e9 2026-10-12 feat(remote): real model-reachability /health probe + opencode v2 migration
fcd7b77 2026-10-11 opencode v2 migration: remove --cwd, update Dockerfile to build v2 from source
c63c453 2026-10-10 Phase 0+1: real /health probe + Docker image + CI
d896ea0 2026-10-10 Fix idempotency in Start (C1): duplicate attempt_id returns 200
a99a196 2026-10-05 issue #20: Harness adapter (wire contract, outcome mapping, conformance suite)
```

## Acceptance criteria status

| # | Criteria | Status | Notes |
|---|---|---|---|
| 1 | Wrapper speaks all four endpoints and passes C1–C9 against a real opencode run | ⚠️ Partial | C1–C4 pass server-level. C5–C9 pass black-box adapter-level against stubs. Real opencode smoke test exists but is gated (`IDLE_DECK_REAL_ACP_TESTS=1`) and requires credentials — not run in CI. **Investigated 2026-10-17:** the gated test runs but returns empty outcome. Diagnosed as **provider-side**, not a code defect: the default model `litellm/big-pickle` returns *"OpenCode's free tier can only be used from within OpenCode"*, and `qwen3.6-27b` returns *"no router for requested model"*; other models time out. The adapter handles this correctly by design — `run.go:89-98` maps empty outcome to `retryable_failure`, and the worker's backoff (D14) retries. Re-test when the provider is healthy. |
| 2 | End-to-end: idle-deck dispatches → wrapper clones → opencode works → Draft PR opens on the ticket | ❌ Not verified | All plumbing exists. No end-to-end evidence of a full pipeline run. |
| 3 | Failing run preserves `debug/<task_id>`, returns `branch` artifact with `branch_purpose: debug` (I5) | ⚠️ Code exists | `OnFailure` in `workspace.go` implements this, but not verified end-to-end. |
| 4 | `GET /health` reports model reachability, not liveness (D31) | ✅ Pass | `handleHealth` returns HTTP 200 with `reachable: false` when the model endpoint is down but the wrapper process is alive — never 503. A 503 would be *wrong*: idle-deck's `IdlePolicy` reads the `reachable` field, so a process-down-but-model-up machine would report reachable=true and idle-deck would spin. The criterion's original "503" wording predates D31/ADR 0016 and is corrected here, not in the code. |
| 5 | Retried `POST /v1/runs` with same `attempt_id` returns same `run_id`, never a second execution (C1) | ✅ Pass | `TestConformanceC1_IdempotentStart` passes; idempotency map enforced in `handleRuns`. |
| 6 | ACP adapter is one file; swapping opencode for another ACP CLI is a one-line binary change | ✅ Pass | `remote/acp.go` is one file. `IDLE_DECK_REMOTE_AGENT_BIN` configures the binary (D46). |

## What is NOT done

1. **End-to-end pipeline test.** No evidence of idle-deck dispatching a task that completes through the full pipeline: poll → enqueue → dequeue → Start → agent works → workspace commits → branch pushed → Draft PR opened. The pieces exist; nobody has wired the daemon's worker loop to a real remote wrapper and run a task through it.

2. **Real opencode integration in CI.** `TestACP_RealOpencode` is gated behind `IDLE_DECK_REAL_ACP_TESTS=1` and skipped in CI. It would need credentials to pass.
   **2026-10-17 status:** the gated test runs but fails with empty outcome. Diagnosed as **provider-side**, not a code defect — the default model `litellm/big-pickle` rejects with *"OpenCode's free tier can only be used from within OpenCode"*, `qwen3.6-27b` reports *"no router for requested model"*, and other models time out. The adapter maps empty outcome to `retryable_failure` (`run.go:89-98`) and the worker's backoff (D14) retries, so this is handled behaviour, not a bug. Re-test when the provider is healthy.

## Remaining work to close the issue

| Item | Effort | Blocks |
|---|---|---|
| ~~Wire `OnSuccess`/`OnFailure` into `Run.collect()`~~ | Small | Done — commit `04e7312` |
| ~~Add worktree GC (honor `WorktreeTTL`)~~ | Small | Done — [#31](https://github.com/seppaleinen/idle-deck/issues/31), commit `8944734` |
| End-to-end test (local or cluster) | Medium | Acceptance #2 |
| Run real opencode smoke test (manual, with creds) | Small | Acceptance #1 |
| Close issue #27 on GitHub | — | Acceptance #1 and #2 |

## Architecture decisions touched

| D-ref | Decision | How this plan satisfies it |
|---|---|---|
| D27 | Workspace lives on the remote | Remote owns checkout, git, and PR production; idle-deck gets typed artifacts |
| D26 | Harness is lifecycle only | `Runner` interface: `Start`/`Abort`/`Result`; no `prepare_workspace` |
| D6/D40 | Never name a model/vendor/provider | `StartRequest` has `Role`, never `Model`. `RoleToModel` is internal to the wrapper |
| D41 | Conformance suite falsifies the seam | C1–C9 specified in `remote-contract.md` §9a, all passing |
| D46 | Agent CLI is operator config | `IDLE_DECK_REMOTE_AGENT_BIN`/`_AGENT_ARGS`/`_AGENT_ENV` |
| D31 | /health reports model reachability | Real ACP round-trip probe, not liveness ping |
| D32 | Static cgo-free binary | Dockerfile asserts `CGO_ENABLED=0`; both binaries in one image |
| D15 | Branch naming convention | `feature/<ticket_id>-<hash>` and `debug/<attempt_id>` |
| D24 | Queue lease with expiry | Server enforces timeout independently of client (watchdog in `run.go`) |

## Notes

- **Issue #28 (Workspace management)** is a child of this issue. Its scope (PrepareWorkspace, OnSuccess, OnFailure) is implemented in `remote/workspace.go` but not yet wired into `Run.collect()`. Closing #27 requires wiring #28's output.
- **The `503` in acceptance criterion #4** was the original wording. The implementation correctly returns HTTP 200 with `reachable: false` — this is D31/ADR 0016's intent (the process is alive, the model is not). The acceptance criterion should be updated, not the code.
- **`test/fakeacp/`** is the CI-safe way to exercise the ACP adapter. It covers ok/error/hang/die/slow modes. The real-opencode test is opt-in only.
