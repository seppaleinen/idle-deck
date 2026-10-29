# Summary

Adds a global spend ceiling that stops new work when cumulative remote-reported token usage exceeds a configured budget.

# Changes

## Core feature
- **Remote contract** (`docs/architecture/remote-contract.md`): `RunResult` gains optional `Usage { PromptTokens, CompletionTokens, TotalTokens }`; conformance suite gains C10 (usage present + absent).
- **Harness types** (`harness/types.go`, `harness/remote.go`): `Usage` struct + `Valid()` method, `MaxTokensPerRun = 2_000_000` constant; parse usage from terminal responses.
- **Remote runner** (`remote/runner.go`, `remote/run.go`, `remote/acp.go`): `Usage` on `RunResult`; wire shape `runResponse` includes usage; ACP adapter extracts usage from agent responses.
- **Configuration** (`config/config.go`, `config/flags.go`): `GlobalBudgetTokens` field + validation (0–1B, 0 disables); `--global-budget-tokens` flag + env var.
- **Store migrations** (`store/store.go`): v12 `spend_ledger` table; v13 `spend_reservations` table with CHECK constraints.
- **Queue spend API** (`store/queue.go`): `RecordSpend`, `CumulativeSpend`, `TryReserveSpend`, `ReleaseReservation`.
- **Worker enforcement** (`worker/worker.go`): `SpendStore` interface; `canDispatch` with atomic reservation + ceiling gate; reservation released on success/failure/preemption.
- **CLI visibility** (`main.go`): `status --json` includes `global_spend { remaining, exhausted }`.
- **Test stubs** (`test/stubs/harness.go`): Stub returns usage in terminal response.
- **Tests** (`worker/worker_test.go`, `test/e2e/pipeline_test.go`): 7 budget tests (enforcement, exhaustion, zero budget, nil usage, reservation release on failure/preemption, oversized usage rejection); constructor calls updated.

## D-refs
- **D37**: Budget is a generation-token ceiling; global ceiling is aggregate
- **D27**: Workspace on remote — remote reports usage from inside the run
- **D41**: Conformance suite falsifies the seam — C10 proves usage reporting works
- **D33**: Automatic forward-only migrations at startup
- **D48/D51**: Draft PRs only, merge on explicit instruction

# Testing

- All existing tests pass (`go test ./...`)
- 7 new worker budget tests cover:
  - Ceiling enforcement blocks dispatch when budget exhausted
  - Zero budget (disabled) allows all work
  - Nil usage from remote leaves ceiling inactive (backward compatible)
  - Reservation released on run failure
  - Reservation released on P0 preemption
  - Oversized `TotalTokens` (>2M) rejected by validation
  - `status --json` reports `remaining` and `exhausted` correctly
- Conformance test C10 verifies usage present + absent cases

# Breaking Changes

None. Default `GlobalBudgetTokens=0` disables the ceiling; remotes not reporting `Usage` leave it inactive.

# Migration

Automatic forward-only migrations at startup (D33): v12 adds `spend_ledger`, v13 adds `spend_reservations`. No manual steps.