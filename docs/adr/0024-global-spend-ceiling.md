---
Title:   Global spend ceiling — per-run usage reporting, local ledger, atomic enforcement
Status:  accepted
Supersedes: —
Related: D37, D27, D41, D33, D48, D51, 0014, 0021
Source:  #69 — "global spend ceiling"
---

## Decision

idle-deck enforces a **global spend ceiling** on cumulative remote-reported token usage.

- The remote harness **reports usage** on every terminal `RunResult` via a `Usage` struct
  (`PromptTokens`, `CompletionTokens`, `TotalTokens`). The field is optional — `nil` means
  the remote did not report usage and the ceiling is inactive for that run.
- idle-deck **records spend** in a local `spend_ledger` table (schema v12) and enforces the
  ceiling **atomically** at dispatch time using a reservation protocol (`spend_reservations`,
  schema v13) that survives concurrency and crashes.
- The ceiling is configured via `GlobalBudgetTokens` (env `IDLE_DECK_GLOBAL_BUDGET_TOKENS`,
  flag `--global-budget-tokens`), validated to `0–1_000_000_000` (0 disables). Per-run
  `TotalTokens` is capped at `MaxTokensPerRun = 2_000_000` to bound single-run impact.
- The ceiling is **gate, not quota**: it blocks new work when exhausted; in-flight runs
  complete. `status --json` exposes `global_spend { remaining, exhausted }`.

## Rationale

- **D37 / D27.** Budget is a generation-token ceiling per role; the remote owns the run and
  the workspace. Only the remote can observe actual token consumption. idle-deck must trust
  the remote's report — it cannot meter what it does not host.
- **D41 / 0014.** The remote contract already exists. Adding `Usage` to `RunResult` is a
  contract extension, not a new seam. The conformance suite gains **C10** (usage present +
  usage absent) so the property is falsifiable client-side.
- **D33.** Automatic forward-only migrations at startup mean the ledger tables appear on
  first run; no manual steps.
- **D48 / D51.** Draft PRs only; the ceiling is observable via `status --json` so a human
  can decide whether to merge when budget is near exhaustion.
- **Atomic reservation** (`TryReserveSpend` / `ReleaseReservation`) makes the gate correct
  under `max_concurrent_jobs: 1` today and preserves correctness if D8 ever changes.

## Alternatives considered

- **Remote-enforced ceiling.** Rejected: the remote serves multiple operators; a shared
  ceiling would require coordination idle-deck does not own.
- **Polling remote for usage.** Rejected: adds inbound surface and latency; the remote
  already has the number at terminal time.
- **Soft ceiling with warnings only.** Rejected: a ceiling that does not block is not a
  ceiling. The operator can set a high value and monitor `status --json`.

## Consequences

- New config surface: `GlobalBudgetTokens` (env, flag). Default `0` = disabled, preserving
  existing behaviour.
- Backward compatible: remotes that do not emit `Usage` simply leave the ceiling inactive.
  No version negotiation needed.
- Schema migrations v12/v13 are additive and forward-only; `CHECK` constraints on
  `spend_ledger` and `spend_reservations` make invalid rows impossible.
- Security: `TotalTokens` capped at `MaxTokensPerRun` prevents a single malformed report
  from exhausting the budget. Global budget upper bound (`1B`) prevents integer overflow
  and misconfiguration.
- The ledger is local SQLite — no external dependency, no network round-trip at dispatch.