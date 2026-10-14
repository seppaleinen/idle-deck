# Architecture Audit: idle-deck Go Codebase

**Audit date:** 2026-10-14  
**Baseline:** AGENTS.md (D1–D47), contract docs (ontology.md, boundaries.md, event-contract.md, remote-contract.md, operator-surface.md)  
**Scope:** All Go source files in the repo — comparison of actual code against locked invariants and contracts.  
**Method:** Read every source file; compare against D-id decisions and contract clauses; flag contradictions, duplicated logic, dead code, and silent drift.

---

## Executive Summary

The codebase is **structurally sound** — the core pipeline (poll → enqueue → dequeue → harness → ack/nack) implements the locked event contract and remote contract correctly. The conformance suite (C1–C9) validates the wire-level behavior, and the worker's preemption/escalation logic follows D14/D16/D37.

However, **seven high-severity findings** and **eleven medium-severity findings** were identified. The high-severity issues are not stylistic — they are **contract violations** or **silent drift** from decisions that were explicitly locked. The most critical:

1. **The tracker sink does not remove labels** — `SetLabels` ignores the `remove` parameter, breaking event-contract §6's guarantee that trigger labels are removed on terminal state.
2. **The remote's default config names a vendor model** — `remote/config.go` hardcodes `opencode/big-pickle`, contradicting D6/D40 ("idle-deck never names a model/provider/vendor").
3. **Dashboard actions bypass the queue lease** — `ProcessDashboardActions` writes raw SQL to the `tasks`/`attempts` tables, violating D24's lease semantics and I10's "never revive in place" invariant.
4. **`check` skips validation** — `LoadRelaxed()` is used by `check`, `run`, `serve`, but it never calls `Validate()`, so malformed URLs/repo strings pass `check` and fail only at runtime.
5. **Sweep ignores configured timeouts/budgets** — `poller.go` hardcodes `TimeoutSeconds: 3600` and `Budget: 20000`, ignoring D37's env var overrides.
6. **Three independent `/health` implementations** — `idle/idle.go`, `harness/remote.go`, and `cmd/remote/health.go` each implement `/health` differently; D31 says there is one probe.
7. **Sweep violation check is incomplete** — `handleSweepResult` checks for `pull_request`/feature-branch artifacts only on success-like outcomes; a non-succeeded sweep with a PR artifact slips through.

---

## Findings by Severity

### High Severity (Contract Violations / Silent Drift)

| # | Location | Decision / Contract | What the Code Does | Why It's a Problem |
|---|----------|---------------------|-------------------|-------------------|
| **H1** | `tracker/github.go:196–200` (`patchLabels`) | event-contract §6: "on any terminal state, remove the trigger label" | `patchLabels` sets `labels: add` and **ignores the `remove` parameter entirely** | The guarantee that `idle-hotfix`/`idle-ready`/`idle-redo` are removed on completion is not implemented — issues accumulate trigger labels, and re-ingestion triggers spuriously. |
| **H2** | `remote/config.go:34–40` (`DefaultConfig`) | D6: "idle-deck never names a model"; D40: "never names a provider/vendor"; D46: "agent CLI is operator config" | Hardcodes `RoleToModel: map[string]string{"plan": "opencode/big-pickle", ...}` | Even though the wire never carries the model name, the remote package **names a vendor model in its default config** — a direct contradiction of D6/D40's "never name a vendor" property. |
| **H3** | `tracker/poller.go:385–556` (`ProcessDashboardActions` + `processAction*`) | D24: "dequeue is a lease, never a pop"; I10: "never revive in place" | `processActionCancel` writes `state='escalated'` directly; `processActionRetry` inserts a new task via raw SQL, bypassing the `Queue` seam and the lease | A dashboard action can **cancel or retry a running task outside the lease**, racing the worker and violating the queue's ownership invariant. |
| **H4** | `config/load_relaxed.go:3–12` (`LoadRelaxed`) | operator-surface §5 step 6: "`check` prints resolved config" (implies validated) | `LoadRelaxed()` calls `fromEnvNoValidate()` + `ApplyFlags()`, but **never calls `Validate()`** | `idle-deck check` prints config including malformed `IDLE_DECK_HARNESS_URL` or invalid repo strings — it passes `check` and fails only on first HTTP call or enqueue. |
| **H5** | `tracker/poller.go:92–137` (`Sweep`) | D37: tier timeouts/budgets are configurable via `IDLE_DECK_TIER_TIMEOUT_SWEEP` / `IDLE_DECK_BUDGET_SWEEP` | Hardcodes `TimeoutSeconds: 3600` and `Budget: 20000` in the task struct | Operator overrides for sweep timeout/budget are **silently ignored** — the env vars exist in `config.go` but are never read for P3. |
| **H6** | `idle/idle.go:28–63` vs `harness/remote.go:270–304` vs `cmd/remote/health.go` | D31: "idle is probed at the harness's `GET /health`"; remote-contract §2: "/health must report model reachability" | Three independent implementations: `idle/idle.go` reads only `reachable`; `harness/remote.go` reads `reachable`+`model` but is never wired in; `cmd/remote` produces both but is a separate binary | **Three different response shapes** for the one probe D31 says exists. `idle/idle.go` would reject a response that omits `model`; `harness/remote.go`'s `Health()` is dead code. |
| **H7** | `worker/worker.go:556–639` (`handleSweepResult`) | D43: "a sweep may never open a PR or produce a feature branch — non-retryable protocol violation" | Violation check (lines 573–583) runs **only before** `fileSweepFindings`, but only on the `OutcomeSucceeded`-like path; a sweep with `OutcomeNonRetryableFailure` and a `pull_request` artifact skips the check | A sweep that fails but returns a PR artifact **proceeds to file issues**, partially undermining D43's "reporter, not author" invariant. |

---

### Medium Severity (Latent Bugs / Drift Risk / Undocumented Behavior)

| # | Location | Decision / Contract | What the Code Does | Why It's a Problem |
|---|----------|---------------------|-------------------|-------------------|
| **M1** | `tracker/github.go:176–186` (`extractRepo`) | D38: "base-URL overrides serve GitHub Enterprise" | `extractRepo` strips only `https://github.com/` and `http://github.com/` | A GHE instance at `https://git.acme.com/` would fail repo extraction — the base-URL override works for HTTP, but repo parsing assumes github.com. |
| **M2** | `worker/worker.go:322–339` (`buildRunRequest`) | remote-contract §3: `repository` has distinct `id` vs `name` fields | Sets `RepoRef.ID = task.RepositoryID` and `RepoRef.Name = task.RepositoryID` (same value) | Duplicates the owner/repo string into both fields — not a wire break (the remote accepts it), but a **contract drift** if the remote ever distinguishes them. |
| **M3** | `store/queue.go:172–188` (`tryClaim` reclaim) | D24: lease expiry consumes one retry; watermark uses RFC3339Nano, heartbeat uses Unix nanos | Reclaim UPDATE compares `started_at < ?` using `cutoff.Format(time.RFC3339Nano)`, but heartbeat/poll timestamps are stored as Unix nanos | Mixed timestamp formats are a **latent bug source** — string ordering of RFC3339Nano is lexicographically correct only if zero-padded (it is), but the inconsistency is a maintenance risk. |
| **M4** | `harness/remote.go:189–257` (`Result`) | remote-contract §5: "adapter polls internally"; §7: "429 is retryable" | Polls with a hard 24h cap; on 429/5xx, **returns immediately** with a typed error (does not loop) | Defensible (the worker re-queues at backoff), but **undocumented** — the contract says "adapter polls", and a reader might expect backoff inside `Result`. |
| **M5** | `harness/remote.go:259–266` (`mapWireArtifacts`) | — | No-op pass-through: `if len(runResp.Artifacts) == 0 { return nil }; return runResp.Artifacts` | Dead code smell — the function exists to "convert" but does nothing. Not a bug, but a **drift risk** if the wire shape changes. |
| **M6** | `harness/conformance_test.go:163–204` (C3) | remote-contract §9a C3: "server kill on timeout" | Comment admits stub "does not auto-enforce timeout_seconds"; test manually completes the run after `time.Sleep` | C3 **does not test server-side kill** — it tests outcome mapping. The conformance case is incomplete. |
| **M7** | `test/stubs/harness.go:414–449` (`handleGetRun`) | remote-contract §5: `GET /v1/runs/{id}` returns `last_activity_at`, `next_log_offset`, `logs` | Reads `X-Log-Offset` from **response headers** (server reads its own headers — a bug); ignores `?log_offset=` query param correctly but offset logic is inverted/unused | The stub **drifts from the contract shape** — the real adapter never parses `last_activity_at`/`next_log_offset`/`logs`, so the stub tests a wire that doesn't exist. |
| **M8** | `config/config.go:109` (`SweepPeriod` default) | operator-surface §4: default documented as `168h` (Go duration string) | Default literal is `"604800"` (seconds = 7 days) | Same value, **two incompatible notations** — the doc says `168h`, the code says `604800`. Config accepts both, but the mismatch is confusing. |
| **M9** | `remote/run.go:78–93` (`Run.Abort`) | remote-contract §6/§9a C9: "preempted never appears on the wire" | Sets `Outcome: OutcomePreempted` server-side; `toWireOutcome(OutcomePreempted)` returns `"cancelled"` (correct on wire) | Internal state carries `OutcomePreempted`, which **leaks if any code path serializes `Outcome` directly** — the invariant is upheld on the wire but not internally. |
| **M10** | `remote/acp.go:449–463` (`recordToolCall`) | D43: "a sweep may never produce a pull_request artifact" | Infers artifacts by **substring-matching** tool output (`"pr create"`, `"git push"`) | Fragile — a sweep that prints "I would run `git push`" or "see PR #123" produces a **false-positive artifact**, triggering the worker's protocol-violation path and dropping a legitimate sweep. |
| **M11** | `harness/remote.go:268–304` (`Health`) | D31: "/health is the idle probe" | `Health()` is **never wired into the worker** — the worker uses `idle.HarnessIdlePolicy` directly | Dead code — the harness package's `Health` method is unused. |

---

### Low Severity (Stale References / Minor Nits)

| # | Location | Issue |
|---|----------|-------|
| **L1** | `docs/plans/issue-27-remote-wrapper.md` + other plan docs | References D5 (superseded by D31) without noting supersession — will mislead the next implementer. |
| **L2** | `worker/worker.go:18–67` | `tierRank`, `defaultTimeoutForTier`, `defaultBudgetForTier` are **duplicated** in `queue/queue.go` and `store/queue.go` — not a bug (intentional per boundaries.md §4), but a drift risk if one copy changes. |
| **L3** | `config/config.go:257–269` (`Load`) | Comment says "Relaxed load without full validation" but that describes `LoadRelaxed`, not `Load` — stale comment. |

---

## Root Causes

Three patterns explain most of the findings:

1. **Contract drift after incremental changes** — H1 (`patchLabels` ignoring `remove`) and H5 (sweep hardcoding timeouts) are not design bugs; they are **features that were added without updating the corresponding sink/poller code**. The contract was locked, but the implementation lagged.

2. **Dead code from refactoring** — H6 (three `/health` implementations) and M11 (`Health()` never wired) are leftovers from **iteration on the idle probe design** (D5 → D31). The new design landed, but the old code was never removed.

3. **Dashboard actions as a backdoor** — H3 (`ProcessDashboardActions`) was added as a **debugging surface**, but it bypasses every invariant the queue exists to enforce (lease, retry count, escalation state). It is a **second ingestion path** that contradicts D28's "one ingestion, one watermark" design.

---

## Recommended Next Issue

**Title:**  
`Fix architecture audit findings: tracker label removal, remote default config, dashboard action bypass, sweep timeout drift`

**Body:**

```
## Objective

Close the seven high-severity findings from the architecture audit (docs/plans/architecture-audit.md) and the eleven medium-severity findings that are contract drift or latent bugs.

## Scope

**In scope:**
- H1: Fix `tracker/github.go` `patchLabels` to honor the `remove` parameter (event-contract §6).
- H2: Remove `RoleToModel` from `remote/config.go` `DefaultConfig` — make it operator-provided via D46's `IDLE_DECK_REMOTE_AGENT_BIN` / `_ARGS` / `_ENV`.
- H3: Remove or gate `ProcessDashboardActions` — if kept, route through `Queue` seam (no raw SQL writes to `tasks`/`attempts`).
- H4: Make `LoadRelaxed()` call `Validate()` — `check` must validate, not just print.
- H5: Read `IDLE_DECK_TIER_TIMEOUT_SWEEP` / `IDLE_DECK_BUDGET_SWEEP` in `poller.go` `Sweep()`.
- H6: Consolidate `/health` — one implementation, wired into both `idle.HarnessIdlePolicy` and the remote server.
- H7: Move sweep violation check to **after** artifact save, run on **all** outcomes (not just success-like).

**Out of scope (defer to follow-up):**
- M1 (GHE repo extraction) — requires a GHE test instance.
- M2 (RepoRef.ID vs Name) — wire-level correctness is fine; refactor when the remote distinguishes them.
- M3 (timestamp format consistency) — latent, not active.
- M4 (Result backoff) — document the behavior instead of changing it.
- M5–M11 — clean up after high-severity fixes land.

## Definition of Done

- [ ] H1–H7 fixed, tests green.
- [ ] Conformance suite (C1–C9) still passes.
- [ ] `go test ./...` green.
- [ ] AGENTS.md D-ids cited in commit messages for each fix (e.g., "Fix H1: patchLabels remove param (D29, event-contract §6)").
```

---

## Remaining Risks

- **Dashboard actions** — If H3 is fixed by removing `ProcessDashboardActions`, operators lose a debugging surface. The follow-up should provide a **read-only** dashboard that observes the queue without writing.
- **GHE support** — M1 is a latent bug for operators using GitHub Enterprise. The fix requires a test instance or a stub that emulates GHE's URL shape.
- **Timestamp consistency** — M3 is a maintenance risk. The fix is to standardize on one format (RFC3339Nano or Unix nanos) across all tables.

---

**Audit completed by:** Researcher subagent  
**Handover payload:** `/Users/seppa/workspace/idle-deck/docs/plans/architecture-audit.md`
