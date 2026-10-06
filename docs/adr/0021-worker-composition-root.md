---
Title:   Worker composition root: core pipeline with idle gate, retry/escalation, preemption, and backoff
Status:  accepted
Supersedes: —
Related: D8, D14, D15, D16, D17, D24, D29, D37, I5, I6, I7, I9, I10, I11, 0009, 0011, 0015, 0019
Source:  #19 Worker composition root implementation
---

## Decision

The **worker is the composition root**, not an adapter. It is a concrete struct with a constructor
and a `Run(ctx)` method — no interface to implement, no second implementation. It owns the
pipeline:

```
Queue.Dequeue → Harness.Start → Harness.Result → Ack/Nack
```

The worker depends on four injected dependencies via its constructor:

- `Queue` — lease-based dequeue, ack/nack, events for preemption
- `Harness` — remote execution lifecycle (`Start`, `Abort`, `Result`)
- `TrackerSink` — writes comments and labels back to the tracker
- `IdlePolicy` — idle gate before each dequeue

`max_concurrent_jobs` is a constructor parameter (D8; MVP: 1). Exponential backoff parameters
`backoffInitial` (default 30s) and `backoffMax` (default 10m) are also constructor parameters
(D37). The worker owns the retry policy (I6); the queue does not decide.

### Loop contract (per boundaries.md §4)

1. **Idle gate** — before each `Dequeue`, call `IdlePolicy.Idle(ctx)`. While not idle, sleep 1s and
   re-probe. Error on probe ≠ idle (never fake-idle).
2. **Dequeue** — blocks until work; returns a `Lease` with expiry.
3. **Start** — `harness.Start(ctx, RunRequest)` with role derived from tier (I11: P0→`do`,
   P1→`plan`, P2→`do`, P3→`sweep`), prompt verbatim, budget and timeout from task with tier
   defaults (D37), `attempt_id` from lease.
4. **Result** — `harness.Result(ctx, id)` blocks until terminal. While waiting, `select` on
   `Queue.Events` for higher-tier arrivals (preemption, D16).
5. **Ack/Nack** — on success: `queue.Ack`. On failure: `queue.Nack` with the outcome; the
   queue's retry logic decides re-queue vs escalate (I6).
6. **Escalation** — after 2 retries (D14, ADR 0009): create `branch` artifact with
   `branch_purpose = debug` named `debug/<task_id>` (D15, I5), comment diagnostics via
   `TrackerSink.Comment`, apply `idle-needs-human` and remove trigger label via
   `TrackerSink.SetLabels` (event-contract §6, D29). All fire-and-forget.

### Preemption (D16, I7)

When a higher-tier `QueueEvent` arrives during a run: `Harness.Abort` → `Nack(preempted)` → the
abandoned attempt is recorded as `preempted` (retryable, consumes no timeout), task returns to
`queued` for retry. The worker then dequeues the higher-tier task.

### Backoff (D37)

Exponential from 30s capped at 10m: `backoff = initial * 2^(attempt-1)`. Applied on retry (not
on success, not on escalation, not on preemption). `Retry-After` from the remote is honoured.
The backoff wait is interruptible by `Queue.Events` so a P0 can preempt during backoff.

### Trigger label lifecycle (event-contract §6)

On any terminal state, the worker removes the trigger label it reacted to (`idle-hotfix`,
`idle-ready`, `idle-redo` via `triggerLabelForEvent`). On escalation it additionally applies
`idle-needs-human`. P1 (issue creation) and P3 (sweep) carry no trigger label — removal is a
no-op.

### I5 enforcer

The worker is the enforcer of **I5**: an escalated task has a `branch` artifact with
`branch_purpose = debug`. If the harness did not return one, the worker synthesises it:
`debug/<task_id>` (D15). The debug branch is owned by idle-deck, not the harness
(boundaries.md §3 negative list).

### Redefinition (I9, I10)

`idle-redo` creates a new task with `derived_from` pointing at the escalated one, tier P1
default, fresh retry budget (I9). The escalated task stays `escalated` forever (I10).

## Rationale

The worker is the only component that sees the whole pipeline. The three adapter seams (`Tracker`,
`Queue`, `Harness`) are intentionally narrow and single-purpose; stitching them into a running
system with policy (idle gate, retries, preemption, backoff, escalation side-effects) is the
worker's job. Making it a concrete composition root — not an interface — avoids the `Base*`
anti-pattern (D18) and keeps the policy where it belongs: in the core, not distributed across
adapters. The four injected dependencies are the exact seam surface from boundaries.md; no global
state, no hidden dependencies.

The lease-based queue (D24, ADR 0011) makes a daemon crash a retryable failure like any other
(D17), so the retry/escalation path needs no special case. Preemption (D16) rides the same
event channel, so the worker loop has one preemption point (`select` in `waitForResult`).
Backoff (D37) is worker-owned because the retry budget is worker-owned (I6).

## Alternatives considered

- **Worker as an interface** with multiple implementations — rejected: the pipeline is the
  product's core logic; plugging in a different pipeline would be a different product, not a
  different implementation. D18 already rejected `Base*` naming.
- **Retry policy in the queue** — rejected: I6 assigns retry policy to the worker. The queue
  only records outcomes and enforces the `maxAttempts` bound.
- **Separate preemption goroutine** — rejected: a single `select` in `waitForResult` handles
  result, preemption, and context cancellation without extra concurrency.
- **Harness-owned debug branch** — rejected: boundaries.md §3 negative list explicitly forbids
  `preserve_debug_state` on the harness; the worker owns I5.
- **Backoff in the queue** — rejected: same as retry policy; backoff is a policy decision
  about *when* to re-dequeue, which the worker controls.

## Consequences

- `worker/worker.go` — ~760 lines: `Worker` struct, `NewWorker`, `Run`, `runLoop`,
  `waitIdle`, `buildRunRequest`, `waitForResult`, `handleResult`, `escalate`,
  `ensureDebugBranch`, `triggerLabelForEvent`, `setEscalationLabels`, `removeTriggerLabel`,
  `calculateBackoff`, `queueEventsTimer`, `eventsNotify`, `buildDiagnosticsBody`,
  `mapHarnessArtifacts`.
- `worker/worker_test.go` — 6 tests with in-process stubs: `TestSuccessP1`,
  `TestTriggerLabelRemovedOnSuccess`, `TestRetryTwiceThenEscalate`, `TestPreemptionP0OverP2`,
  `TestLeaseExpiryHandling`, `TestBackoffApplied`.
- `store/queue.go` — `AttemptCount` method added for backoff calculation.
- `store/store_test.go` — `TestLeaseExpiryRecordsTimeout` fixed to assert `timeout` outcome.
- `main.go` — `runRun` passes `cfg.RetryBackoffInitial` and `cfg.RetryBackoffMax` to
  `NewWorker`.
- **Known limitation**: P3 sweep is tracker/ingress scope (scheduler tick → enqueue), not
  worker scope. The worker executes a P3 task like any other (role `sweep`), but the
  trigger is a local scheduler, not a GitHub event. This is per event-contract §3 and
  ADR 0019 (D43/D44/D45).