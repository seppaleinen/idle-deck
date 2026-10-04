# idle-deck architectural boundaries

The interfaces between idle-deck's moving parts, and the contract each one promises. This document
is the resolution of [Architecture boundaries #6](https://github.com/seppaleinen/idle-deck/issues/6)
and the direct input to [MVP backlog #10](https://github.com/seppaleinen/idle-deck/issues/10).

**Status:** locked by #6 against the [domain ontology](../ontology.md) and the decision record
([D1–D41](../../AGENTS.md), [ADRs](../adr/README.md)).

---

## The shape in one paragraph

idle-deck is an orchestration daemon with **three adapter seams** — `Tracker`, `Queue`, `Harness` —
plus the **worker loop** (the core composition root, not an adapter) and an **`IdlePolicy`**
dependency owned by the worker. Each adapter has **exactly one** concrete implementation in the MVP
(D13, [ADR 0008](../adr/0008-minimal-interfaces-one-implementation.md)): GitHub, SQLite, one remote
HTTP execution service. A `Base*` prefix names nothing in this codebase; interfaces are named by
behaviour (D18).

```
  GitHub poll ──▶ TrackerSource.Parse ──▶ ingress ──▶ Queue.Enqueue
                                                        │
  worker loop ─── Queue.Dequeue (lease) ──▶ Harness.Start ──▶ Harness.Result
                     │  ▲                                      │
                     ▼  │                                      ▼
                 Queue.Ack/Nack                          (role + artifacts)
                     │
                     ▼
             TrackerSink.Comment / SetLabels
```

---

## The seams

### 1. `Tracker` — split as `TrackerSource` + `TrackerSink`

#1's `BaseTracker` fused webhook parsing with outbound writes. The boundary is **split into two
interfaces**, implemented by **one** GitHub struct in the MVP (D13 is about implementation count, not
interface count). Decision: [ADR 0012](../adr/0012-tracker-split.md).

```go
// TrackerSource turns an inbound tracker event into a derived, allowlist-checked Task.
// It is a pure parser: no queue access, no side effects.
type TrackerSource interface {
    Parse(ctx context.Context, raw []byte) (Task, error)
}

// TrackerSink writes diagnostics and labels back onto the tracker.
// It is the only write path out of idle-deck to the tracker.
type TrackerSink interface {
    Comment(ctx context.Context, ref TrackerRef, body string) error
    SetLabels(ctx context.Context, ref TrackerRef, add, remove []string) error
    OpenIssue(ctx context.Context, repo string, title string, body string, labels []string) error
}
```

**Invariants / lifecycle rules**

- `Parse` enforces **I1** — the originating `Repository` must be `allowlisted`; otherwise it returns
  a non-retryable ingestion error. The allowlist gate lives here, at ingestion, never later.
- `Parse` fills `TriggeredBy.dedupe_key` from the observed resource, so ingestion is idempotent from
  stored data. #9 settled the shape: a **stable resource key** (`github:issue:<repo>:<number>:<trigger>`),
  because polling observes state rather than events, so re-observation is the normal case. There is no
  delivery id and none is needed.
- The **ingress handler** (core app code, not an adapter) calls `Parse` then `Queue.Enqueue`. The
  tracker never touches the queue; the two seams meet only in the core.
- `Parse` must **never** write to the tracker (no comments, no labels, no PRs).
- `TrackerSink` must **never** parse events. It is fire-and-forget from the worker's perspective:
  a failed `Comment` is logged and retried by the worker's retry policy, not by the sink.
- **PR production is not a tracker method.** A succeeded P2 attempt has a `pull_request` artifact
  (I4), enforcer = harness adapter on return — the harness returns the artifact, and opening the PR
  happens harness-side — permanently, since #8 settled that the workspace is remote (D27/ADR 0014).
- MVP implementation: **one GitHub type** satisfies both interfaces (methods `ParseHandler`,
  `CommentIssue`, `SetIssueLabels` wired to the GitHub API).

### 2. `Queue` — lease semantics, ack/nack, preemption signal

The queue orders work and **leases** it. The lease is the whole fault-tolerance story: a daemon
crash must never strand a task (D14). Decision: [ADR 0011](../adr/0011-queue-lease.md).

```go
type Queue interface {
    // Enqueue adds a task (idempotent per dedupe_key).
    Enqueue(ctx context.Context, task Task) error
    // Dequeue blocks until work is available and atomically claims the highest-tier
    // item (FIFO within tier). Returns a lease with an expiry.
    Dequeue(ctx context.Context) (Lease, error)
    // Ack marks the leased task's current attempt succeeded (I2 satisfied via worker).
    Ack(ctx context.Context, lease Lease, attemptID string) error
    // Nack records the attempt outcome and lets retry policy decide (I6).
    Nack(ctx context.Context, lease Lease, attemptID string, outcome AttemptOutcome) error
    // Events returns a stream of enqueue notifications carrying the tier, so a busy
    // worker can learn that higher-priority work arrived (preemption, D16/I7).
    Events(ctx context.Context) (<-chan QueueEvent, error)
    // Release returns the lease without recording an outcome (only valid before the
    // attempt is started).
    Release(ctx context.Context, lease Lease) error
}
```

**Lease contract (the highest-consequence question in the map)**

- `Dequeue` promises: **exactly one holder per item at any time**, atomically claimed (one SQLite
  transaction in the MVP). Ordering promise: **tier-descending, FIFO within tier**.
  `priority_score` is a derived sort key only, never authoritative (ontology).
- The lease carries an **expiry**. The worker must `Ack` or `Nack` (or `Release` before starting)
  before it expires.
- **Expiry without ack = the attempt died with its worker.** The queue records the abandoned
  attempt's outcome as **`timeout`**, re-queues the task, and the retry budget consumes one retry —
  exactly like any other retryable failure (D17). After two such crashes, I6 escalates. There is no
  "lost task" path and no infinite loop.
- A preempted run that delivers a final `Nack(preempted)` is **I7**: no timeout consumed, retryable,
  no burn of the retry budget for the timeout itself.
- `max_concurrent_jobs: 1` is a **worker-side constructor parameter**, not a queue property. The
  lease is per-item, so running N workers is N loops over the same seam.
- **Preemption (D16):** the worker selects on `Events`. When an event shows a tier above the running
  tier, the worker: `Harness.Abort(run)` → `Nack(lease, preempted)` → `Dequeue` the higher item →
  start it. With N workers the broadcast lets the lowest-priority running worker preempt itself.
- **Enqueue is idempotent per `dedupe_key`** — the concrete queue ignores a task whose key it has
  already seen. Settled by #9: the key is a stable resource key, so re-observing a condition is free.
- MVP implementation: **SQLite** with an `attempts` and `tasks` table, a transaction to claim the
  lease, and an in-process notification channel on `Enqueue`.

### 3. `Harness` — execution lifecycle only

The harness is a **remote execution service** (D7, D20; one JSON/HTTP adapter). Its interface is
**pure lifecycle**: start, abort, collect. No workspace preparation, no local git operations — the
original `BaseHarness` bundled two incompatible architectures into one name, and the split is what
let #8 settle workspace ownership without redesigning the seam. Decision:
[ADR 0013](../adr/0013-harness-lifecycle.md).

```go
type Harness interface {
    // Start begins execution of a run and returns a session id usable for abort (I8).
    Start(ctx context.Context, req RunRequest) (RunID, error)
    // Abort cancels a running session. It returns only when the remote side confirms
    // cancellation (or has already completed).
    Abort(ctx context.Context, id RunID) error
    // Result blocks until the run is terminal and returns its outcome and artifacts.
    Result(ctx context.Context, id RunID) (RunResult, error)
}
```

```go
type RunRequest struct {
	Role      string // plan | do | sweep, derived from task tier (D6, I11). Never a model name.
	Prompt    string
	Budget    int
	Timeout   time.Duration
	RepoRef   Repository // identity + allowlisted URL
	TicketRef TrackerRef
	AttemptID string
	Findings  []Finding   // populated by the sweep agent; forwarded to TrackerSink.OpenIssue
}

type RunResult struct {
	Outcome   AttemptOutcome // succeeded | retryable_failure | non_retryable_failure | timeout | preempted
	Artifacts []Artifact     // pull_request | branch | comment | report | log
}

// Finding is a structured finding from a P3 sweep, describing a code health
// marker or other concern. The sweep agent collects these; idle-deck files one
// issue per finding via TrackerSink.OpenIssue.
type Finding struct {
	Title       string
	Description string
	Category    string   // e.g. "stale-TODO", "coverage-gap", "dependency-drift"
	Severity    string   // optional: "low" | "medium" | "high"
}
```

**Invariants / lifecycle rules**

- `RunRequest` carries a **role, never a model** (D6): the harness decides the model.
- `Start` returns a `RunID` immediately; the attempt's `remote_session_id` is set from it (I8).
  `Abort` is the **cancellation contract**: a running attempt is abortable via its session id (I8).
- `Result` is **terminal and single-shot**; the worker calls it exactly once. It returns artifacts as
  **data** — the harness does not reach back into idle-deck and idle-deck does not read the remote
  filesystem. *Who physically produces the branch / opens the PR* was #8's workspace-ownership
  decision, and it answered **the harness, remotely** (D27/ADR 0014): the remote owns checkout, git,
  and Draft PR production. This interface foreclosed neither answer; it now forecloses the local one.
- **`preserve_debug_state` is not a harness method.** I5's enforcer is already "Worker, at
  escalation": the worker produces the `branch` artifact with `branch_purpose = debug` from what the
  harness returned, not by asking the harness to do git.
- **Never-do:** no `prepare_workspace`, no `dispatch`-with-filesystem, no model names, no polling
  API on the worker side beyond `Result`.
- **"Framework-agnostic" means this interface names no model, provider, or vendor** — a property of
  `RunRequest`'s *shape*, since it has no field a name could occupy. The MVP makes **no** claim that a
  second, structurally different implementation drops in; that is deferred behind a trigger list
  (D40/D41, [ADR 0018](../adr/0018-framework-agnostic-means-never-naming-a-vendor.md)). The MVP
  adapter instead ships with the **conformance suite** specified in
  [`remote-contract.md`](remote-contract.md) §9a — nine client-observable cases against the wire
  contract, which is what a second adapter would have to satisfy. D41 adds **no method** to this
  interface; the suite tests the contract underneath it, not the three methods.
- **A second agent CLI is not a second adapter.** `pi`, `opencode`, `claude`, `cursor-agent` are
  vendors of agent execution and run *inside* the remote, which owns the workspace (D6, D27). A second
  *remote service* would be a second adapter; a second CLI behind one remote is not.

### 4. Worker loop — composition root, `max_concurrent_jobs` parameter

Not an adapter: the **core** that owns the pipeline. It does not have an interface to implement; it
has a constructor and a run method.

```go
type Worker struct {
    queue    Queue
    harness  Harness
    sink     TrackerSink
    policy   IdlePolicy
    maxConcurrent int // MVP: 1 (D8). The value does not change the seam.
}

func (w *Worker) Run(ctx context.Context) error
```

**Loop contract**

- Gate: before each `Dequeue`, consult `IdlePolicy.Idle(ctx)`; while not idle, sleep and re-probe
  (D5, [ADR 0003](../adr/0003-idle-is-a-probe.md)). No platform heuristics.
- Then: `Dequeue` → `Harness.Start` → run → `Harness.Result` → `Ack` on success, `Nack` with the
  outcome on failure. Retry policy (two retries, then escalate — D14, [ADR 0009](../adr/0009-two-retries-then-escalate.md)) decides what `Nack` does next.
- While a run is in flight, `select` on `Queue.Events` for preemption (D16).
- At escalation: create the `branch` debug artifact (I5) from the run result, comment diagnostics
  and apply `needs-human` via `TrackerSink` (D14).
- `max_concurrent_jobs` is enforced by the worker starting at most that many run loops; nothing else
  in the system knows or cares.

### 5. `IdlePolicy` — one method, injectable (ADR 0003 consequence)

```go
type IdlePolicy interface {
    Idle(ctx context.Context) (bool, error)
}
```

- MVP implementation probes the inference server: if it answers, the engine is considered idle (D5).
- The worker depends on it via the constructor (see above); it is not an adapter seam and ships no
  second implementation.

---

## What each seam must never do — the negative list

| Seam | Must never |
|---|---|
| `TrackerSource` | Write to the tracker; touch the queue; enforce anything but I1. |
| `TrackerSink` | Parse events; own retry policy (worker owns retries). |
| `Queue` | Name models; run the task; decide policy (I6 retry policy lives in the worker); lose a task on lease expiry. |
| `Harness` | Touch idle-deck's local filesystem through the interface; accept model names (D6); assume a local workspace (settled: the workspace is remote, D27). |
| Worker | Enforce any invariant via a queue that does not lease; run multiple tasks against one lease. |
| `IdlePolicy` | Fake idle when the probe fails (error ≠ idle). |

---

## What the MVP ships (per seam)

| Seam | MVP implementation | Backlog ticket |
|---|---|---|
| Tracker | One GitHub type: `ParseHandler`, `CommentIssue`, `SetIssueLabels` | #10 (contract: [`event-contract.md`](event-contract.md)) |
| Queue | SQLite, transactional lease + in-process event channel | #10 |
| Harness | One remote JSON/HTTP adapter | #10 (contract: [`remote-contract.md`](remote-contract.md)) |
| Worker | Go daemon, one worker loop (`max_concurrent_jobs: 1`) | #10 |
| IdlePolicy | Probe against the inference server | #10 |

## Cross-references

- Domain model: [`docs/ontology.md`](../ontology.md) — entities, invariants I1–I11
- Adapter count / minimal interfaces: [ADR 0008](../adr/0008-minimal-interfaces-one-implementation.md)
- Retry/escalation: [ADR 0009](../adr/0009-two-retries-then-escalate.md)
- Remote harness: [ADR 0005](../adr/0005-one-harness-adapter.md), [ADR 0006](../adr/0006-models-on-a-separate-server.md)
- Remote contract (workspace ownership, locked): [`remote-contract.md`](remote-contract.md), [ADR 0014](../adr/0014-remote-execution-service-contract.md)
- Event contract (delivery, triggers, tier derivation, label vocabulary, locked): [`event-contract.md`](event-contract.md), [ADR 0015](../adr/0015-github-event-contract.md)