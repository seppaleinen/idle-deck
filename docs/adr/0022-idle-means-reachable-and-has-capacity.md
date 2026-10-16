---
Title:   Idle is probed at the harness, and /health must report model reachability AND model capacity
Status:  accepted
Supersedes: 0016
Related: D49, D31, D5, D6, D7, D20, D27, I8, 0006, 0013, 0014
Source:   The D31 capacity amendment: idle-deck on a local GPU cluster, where background runs compete with interactive sessions for model capacity
---

## Decision

**Idle is probed at the harness's `GET /health`, and that endpoint must report two things:
that idle-deck can *reach* the model, and that the model has *capacity* right now.** Idle
means **reachable AND not busy**, not merely reachable.

The probe's *location* is unchanged and is not what this ADR revisits — it still goes to the
remote execution service's `/health`, never to the inference server, and never via a platform
liveness or thermal heuristic (D31's mechanism, D5's prohibition, both carried forward). What
changes is the **definition**: 0016 said idle still meant *reachable*, and 0003's definition
("idle-deck can reach the model") is now a *necessary but not sufficient* condition. A model that
idle-deck can reach but is saturated by another workload is **not** idle.

The capacity signal is a **`busy` field on the existing `/health` response** (not a new
endpoint, not a separate probe), reported by the remote as its own knowledge of whether it is
occupied. The daemon gates dispatch on it **behind an opt-in knob**,
`IDLE_DECK_YIELD_WHEN_BUSY`, whose **default is `false`** — so existing deployments observe
exactly 0016's reachable-only behaviour, and no operator who did not ask for capacity gating
changes behaviour. When the knob is on, a `busy: true` answer makes the probe report *not idle*,
so the worker re-probes on its existing cadence instead of starting a run that would queue
behind (or steal GPU from) an interactive session.

This is an **additive amendment to D31, not a reversal**: reachability remains load-bearing and
is checked first (an unreachable model is not idle regardless of `busy`); the capacity field
refines the definition from the *sufficient* side. D31 is marked superseded, and its mechanism
survives intact in this ADR.

## Rationale

D31 answered "where does the probe go" and "what must `/health` report to make *reachable* mean
*can run*." It did not answer a different question that a local GPU cluster raises: the model can
be reached, and idle-deck can start a run, but the run will **compete for the same scarce resource
(an interactive session is using) — the accelerator — and slow both of them down.** Reachability
is a *correctness* property (will the run even work); capacity is a *courtesy* property (will the
run be a good neighbour). 0016's definition — "reachable ⇒ idle" — conflates the two, and on a
shared workstation the conflated definition is the bug: idle-deck certifies the machine as free
while an interactive agent is generating, and the background run lands exactly when the operator
most wants it not to.

The capacity signal must come from the remote, for the same reasons the reachability signal
does (0016's "one URL, one credential" argument, restated): idle-deck does not host models (D20)
and names no model, provider, or vendor (D6, D40), so it has **no direct view of model load**.
The remote is the one component that knows its own run state — which of its runs are executing,
on which accelerator — so it is the only honest source of "am I busy right now." A probe that
idles on *reachability only* would need a second network path (or platform heuristics, which D5
bans) to learn load; routing it through the same `/health` that already exists keeps the
credential count, the endpoint count, and the dependency surface all at one.

The gate is **opt-in and default-off** because the two answers to "should a busy model block a
queued run?" are legitimately operator-dependent, and the *safe* default is the one that changes
nothing:

- On a machine where background runs and interactive sessions share a GPU, the operator wants
  yield-when-busy **on** — the whole point is to not steal capacity.
- On a machine where the remote is dedicated to idle-deck (no competing interactive load),
  yield-when-busy **off** is correct — a "busy" answer would then just mean "one of my own runs
  is in flight," which the single-slot scheduler (D8, `max_concurrent_jobs: 1`) already
  serializes, and gating on it would make the daemon idle-wait on its own work.

A default of `false` therefore cannot make a dedicated-remote deployment slower, while an
operator on a shared cluster flips one env var to opt in. The knob is the decision the operator
makes; the contract change (the `busy` field) is the machinery that makes the decision
*possible*, and it is load-bearing: the field must exist and be emitted before the gate can read
it.

## Alternatives considered

**A — Extend `/health` with a `busy` field (chosen).** The capacity answer rides the same
endpoint, same credential, same probe as reachability. idle-deck's `IdlePolicy` decodes one
response and combines the two fields. Minimal surface: no new endpoint, no new credential, no
second dependency. The remote reports `busy` from its own run state, which it already holds.
Chosen.

**B — A separate capacity probe (e.g. `GET /capacity`).** A distinct endpoint for a distinct
question. Rejected for the same reason 0016 rejected a dedicated `/reachability` endpoint: a
distinction without a difference at one remote implementation, at the cost of a second URL and
(plausibly) a second credential, and a second call the worker must make and reconcile per probe.
D13 is "minimal interfaces, one concrete implementation each"; if a future remote genuinely needs
to separate *liveness* from *capacity* in a way one field cannot express, that is a v2 wire
change, not a reason to add an endpoint now. (The same treatment `/health` gave `SSE`.)

**C — No gating at all (keep 0016's reachable-only definition).** Rejected *for the shared-
cluster case only*: it is exactly the behaviour that leaves an interactive session competing with
a background run. It remains the **default** (via the knob), which is why this is an additive
amendment rather than a reversal — C survives as the opt-out, and A is available to the operator
who needs it. The amendment does not force anyone onto A; it makes A *available*.

A fourth option was considered and dismissed during drafting: **a load *metric* (e.g. a 0–1
utilization number) rather than a boolean.** Rejected as a distinction without a difference for
the MVP's single-slot scheduler, which can only act on a binary "go / don't go" — a float would
need a threshold operator to tune, a second policy knob, and a reason to change meaning with
every model's reporting. A boolean `busy` is the minimal shape that answers the question the
worker asks, "may I start?" and is what the conformance suite can pin without guessing a
threshold. If a future remote reports load in a way a boolean cannot capture, that is a named
trigger for widening the field, not a reason to ship a float now.

## Consequences

- **D31 is superseded**, mechanism intact. 0016's probe *location* (the harness's `/health`),
  its *reachability-not-liveness* requirement, and its *no-platform-heuristics* prohibition all
  survive and are restated here; only the *definition* moved from "reachable" to "reachable AND
  not busy." This is the same shape as 0003 → 0016: a decision's mechanism is preserved while the
  part a future reader will quote ("what does idle *mean*") changes, so the old ADR is superseded
  rather than quietly amended in place.
- **`remote-contract.md` gains a field on an existing endpoint**: `GET /health` now also returns
  `busy: bool`. The endpoint table and the reachability note are amended on this ADR's authority;
  the contract file states of itself that a deviation "is a new decision," and this is that
  decision.
- **The producer must emit the field.** The remote server reports `busy` from its own run state
  (a running run ⇒ busy). This is client-observable and is pinned by the conformance suite: a
  harness whose `/health` omits `busy` (or emits a wrong value) fails the health case. The field
  is part of the wire, so it is a conformance property, not an implementation detail.
- **The gate is opt-in.** `IDLE_DECK_YIELD_WHEN_BUSY` (default `false`) is the single knob.
  Default-off preserves 0016's reachable-only behaviour for every existing deployment; on, a
  `busy: true` answer makes the probe report not-idle. `status`/`check` report the field so a
  false "idle" (reachable but busy) is visible to an operator rather than only inferable from
  a stalled interactive session.
- **`IdlePolicy`'s seam is unchanged** — one method, `Idle(ctx) (bool, error)`, injectable,
  not platform-specific. The `HarnessIdlePolicy` implementation behind it now decodes two fields
  instead of one and combines them; nothing upstream of the worker changes.
- **What `busy` does and does not mean.** `busy` is the remote's own knowledge of *its*
  occupancy — the runs *it* is executing. It is a *courtesy* signal, not a correctness one:
  an unreachable model is not idle (reachability, checked first), and a reachable-but-busy
  model *is* idle under the default configuration and not-idle when the knob is on. The remote
  cannot see model contention it did not cause (e.g. an external session the remote never
  scheduled), so `busy` is the strongest signal *this* system can honestly produce, and the ADR
  records that limit rather than pretending the field is a complete model-load meter.
- **The conformance suite's health case is extended** to assert `busy` is present and that a
  running run flips it to `true`, so the wire field cannot be dropped or renamed without a
  visible test failure. This is D41's instrument: the seam is falsified by a case, not by a
  second implementation.
