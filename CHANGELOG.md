# Changelog

## [Unreleased] - 2026-10-06

### Added
- Worker composition root (`worker/worker.go`): core pipeline with idle gate, retry/escalation,
  preemption, exponential backoff, and trigger-label lifecycle (Issue #19).
- `Worker` struct with four injected dependencies: `Queue`, `Harness`, `TrackerSink`, `IdlePolicy`.
- `NewWorker` constructor accepting `max_concurrent_jobs` (D8), `backoffInitial`, `backoffMax` (D37).
- `Run(ctx)` method implementing the full loop contract per `docs/architecture/boundaries.md` §4.
- Idle gate: probes `IdlePolicy.Idle(ctx)` before each dequeue; sleeps 1s on not-idle; error ≠ idle.
- Preemption (D16, I7): `select` on `Queue.Events` during run; higher-tier event aborts current run
  via `Harness.Abort`, `Nack(preempted)`, re-queues preempted task for retry.
- Retry/escalation (D14, ADR 0009, I6): two retries then escalate; `timeout` and `preempted`
  are retryable (D17); `non_retryable_failure` escalates immediately.
- Escalation side-effects (event-contract §6, I5): diagnostics comment via `TrackerSink.Comment`,
  `idle-needs-human` label + trigger label removal via `TrackerSink.SetLabels`,
  synthesised debug branch `debug/<task_id>` (D15) if harness did not return one.
- Exponential backoff (D37): 30s initial, capped at 10m; `Retry-After` honoured; interruptible by
  `Queue.Events` so P0 can preempt during backoff.
- Trigger label lifecycle: removes `idle-hotfix`/`idle-ready`/`idle-redo` on any terminal state;
  applies `idle-needs-human` on escalation (D29).
- Redefinition support (I9, I10): `idle-redo` creates new task with `derived_from` and fresh retry
  budget; escalated task stays `escalated`.
- `AttemptCount` method on `SQLiteQueue` for backoff calculation.
- 6 worker tests with in-process stubs covering success, trigger-label removal, retry/escalation,
  P0 preemption, lease expiry, and backoff.

### Changed
- `main.go`: `runRun` now passes configured `RetryBackoffInitial` and `RetryBackoffMax` to
  `NewWorker` (from `IDLE_DECK_RETRY_BACKOFF_INITIAL` / `IDLE_DECK_RETRY_BACKOFF_MAX`, defaults
  30s / 10m per D37).
- `store/store_test.go`: Fixed `TestLeaseExpiryRecordsTimeout` to assert the abandoned attempt is
  recorded as `timeout` outcome.

### Fixed
- Lease expiry handling: worker now correctly recovers from `ErrLeaseExpired` on Ack/Nack after
  queue reclaim, continues running (TestLeaseExpiryHandling).

### Notes
- P3 sweep trigger is tracker/ingress scope (local scheduler), not worker scope. The worker
  executes P3 tasks (role `sweep`) identically to other tiers. This is per event-contract §3 and
  ADR 0019 (D43/D44/D45).
- No cancellation surface; `aborted` outcome is reserved in ontology, not implemented.
- No global spend ceiling (deferred until remote reports usage).