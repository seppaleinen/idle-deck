# Issue #19 — Worker (composition root)

## Status
Plan written. Awaiting approval gate.

## Dependencies (all satisfied)
- #15 scaffold, #14 config, #17 storage/queue, #16 stubs, #18 tracker adapter, #20 harness adapter, #22 idle policy

## What

The worker is the **composition root** — not an adapter. It owns the pipeline:

```
Queue.Dequeue → Harness.Start → Harness.Result → Ack/Nack
```

Per `docs/architecture/boundaries.md` §4, it has a constructor and a `Run(ctx)` method, no interface.

## Files to create

| File | Purpose |
|------|---------|
| `worker/worker.go` | `Worker` struct + `Run(ctx)` method |
| `worker/worker_test.go` | Tests with stub Queue, Harness, TrackerSink, IdlePolicy |

## Worker struct

```go
type Worker struct {
    queue       Queue
    harness     Harness
    sink        TrackerSink
    policy      IdlePolicy
    maxConcurrent int
    log         *slog.Logger
}
```

## Run loop (boundaries.md §4 loop contract)

1. **Idle gate** — before each `Dequeue`, call `policy.Idle(ctx)`. While not idle, sleep (1s) and re-probe. Never fake-idle on probe failure (error ≠ idle).
2. **Dequeue** — blocks until work is available; returns a `Lease` with expiry.
3. **Start** — `harness.Start(ctx, RunRequest)` with role from `RoleForTier(tier)`, prompt verbatim, budget, timeout.
4. **Result** — `harness.Result(ctx, id)` blocks until terminal.
5. **Ack/Nack** — on success: `queue.Ack(lease, attemptID)`. On failure: `queue.Nack(lease, attemptID, outcome)`.
6. **Preemption** — while a run is in flight, `select` on `Queue.Events` for higher-tier arrivals (D16).
7. **Escalation** — after 2 retries, create debug branch artifact (I5), comment diagnostics + apply `needs-human` label via `TrackerSink` (D14).

## Retry policy (D14, ADR 0009)

- Two automated retries, then escalate.
- `OutcomeRetryableFailure` → retry.
- `OutcomeTimeout` → retryable (D17).
- `OutcomeNonRetryableFailure` → escalate immediately.
- `OutcomePreempted` → normal retry/escalation (D16).
- `OutcomeSucceeded` → Ack, push feature branch, open Draft PR (D21).

## Concurrency (D8)

- `max_concurrent_jobs: 1` for the MVP.
- Enforced by the worker starting at most that many run loops; nothing else knows or cares.

## Done-when gates

1. `Worker` struct compiles with all four dependencies injected via constructor
2. `Run(ctx)` implements the full loop contract from boundaries.md §4
3. Idle gate: not idle → sleep + re-probe (never starts a run)
4. Success path: Dequeue → Start → Result → Ack (no escalation)
5. Retry path: retryable failure → retry (up to 2), then escalate
6. Escalation path: after 2 retries → debug branch artifact + comment + `needs-human` label
7. Preemption: higher-tier event during run → abort current, start new
8. `go test ./...` green (build, vet, all tests)

## Notes

- The worker depends on `Queue`, `Harness`, `TrackerSink`, `IdlePolicy` via constructor — no global state, no adapter seam.
- `max_concurrent_jobs` is a constructor parameter; the value does not change the seam.
- The worker owns retry policy (I6); the queue does not decide.
- `TrackerSink` is fire-and-forget from the worker's perspective: a failed `Comment` is logged and retried by the worker's retry policy, not by the sink.
- Uses `slog` for structured logging (JSON format per config).
- The `main.go` `runRun` function will be updated to construct the worker and call `Run()`.