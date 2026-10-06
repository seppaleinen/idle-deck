# PR Description: Issue #19 — Worker Composition Root Implementation

## Summary
Implements the worker composition root — the core pipeline that stitches the three adapter seams
(Queue, Harness, TrackerSink) with IdlePolicy into a running daemon with idle gating, retry/escalation,
preemption, exponential backoff, and trigger-label lifecycle.

## Changes

### New files
- `worker/worker.go` — Worker struct, `NewWorker` constructor, `Run(ctx)`, and all loop methods:
  `runLoop`, `waitIdle`, `buildRunRequest`, `waitForResult`, `handleResult`, `escalate`,
  `ensureDebugBranch`, `triggerLabelForEvent`, `setEscalationLabels`, `removeTriggerLabel`,
  `calculateBackoff`, `queueEventsTimer`, `eventsNotify`, `buildDiagnosticsBody`,
  `mapHarnessArtifacts`.
- `worker/worker_test.go` — 6 tests with in-process stubs:
  `TestSuccessP1`, `TestTriggerLabelRemovedOnSuccess`, `TestRetryTwiceThenEscalate`,
  `TestPreemptionP0OverP2`, `TestLeaseExpiryHandling`, `TestBackoffApplied`.

### Modified files
- `store/queue.go` — Added `AttemptCount` method for backoff calculation.
- `store/store_test.go` — Fixed `TestLeaseExpiryRecordsTimeout` to assert attempt outcome is
  `timeout` (was not asserting correctly).
- `main.go` — `runRun` now passes `cfg.RetryBackoffInitial` and `cfg.RetryBackoffMax` to
  `NewWorker` (previously hardcoded defaults).

## Decisions cited
- **D8** — `max_concurrent_jobs: 1` (worker-side constructor param)
- **D14** — Two automated retries then escalate (ADR 0009)
- **D15** — `debug/<task_id>` branch naming (I5 enforcer)
- **D16** — P0 preempts running P2 (D16, I7)
- **D17** — Timeout is retryable; preempted consumes no timeout
- **D24** — Lease semantics: expiry records `timeout`, re-queues, consumes one retry (ADR 0011)
- **D29** — Label vocabulary (`idle-hotfix`, `idle-ready`, `idle-redo`, `idle-needs-human`)
- **D37** — Budget/timeouts/backoff (exponential 30s → 10m, `Retry-After` honoured)
- **I5** — Worker enforces debug branch at escalation
- **I6** — Retry policy owned by worker
- **I7** — Preempted is retryable, no timeout consumed
- **I9** — `idle-redo` → `derived_from`, fresh retry budget
- **I10** — Escalated stays escalated
- **I11** — Role derived from tier (P0/P2→`do`, P1→`plan`, P3→`sweep`)

## Testing
All tests pass:
```
go test ./... -race -count=1    # 68 tests pass
go vet ./...                    # clean
gofmt -l .                      # clean
```

Test coverage includes:
- Success path (P1, P2) with trigger label removal on terminal state (event-contract §6)
- Retry twice then escalate with diagnostics comment, `idle-needs-human` label, debug branch
- P0 preemption of running P2 (abort → Nack preempted → re-queue P2 → start P0)
- Lease expiry handling (records `timeout`, re-queues, worker recovers)
- Exponential backoff applied between retries (interruptible by Queue.Events)

## Known limitations
- **P3 sweep is not a worker concern.** The sweep trigger is a local scheduler tick
  (event-contract §3, ADR 0019 D43/D44/D45). The worker executes a P3 task identically to
  other tiers (role `sweep`), but the scheduler that enqueues it lives in the tracker/ingress
  layer, not here.
- No cancellation surface (`aborted` outcome is reserved in ontology, not implemented).
- No global spend ceiling (deferred until remote reports usage, per operator-surface.md §8).

## Verification
- CI gates: `go build ./...`, `go vet ./...`, `go test ./... -race`, `gofmt -l .` all pass.
- D-id verification: all cited `D<n>` and `I<n>` resolve in `AGENTS.md` and `docs/ontology.md`.