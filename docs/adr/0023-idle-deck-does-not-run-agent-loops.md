---
Title:   idle-deck does not run agent loops; the scheduler is a tick, not a driver
Status:  accepted
Supersedes: —
Related: D50, D26, D20, D6, D40, D43, D44, D45, 0013, 0019
Source:  #34 — "supplement parts of idledeck logic by using agent loops"
---

## Decision

idle-deck runs **no agent loop**. The coordinator's only loop is a scheduler
tick: poll the queue, dispatch one task per tick, wait. Any reasoning loop —
the agent thinking, calling tools, iterating — lives **inside the remote
harness**, which idle-deck observes as a terminal `RunResult`.

## Rationale

The question as asked ("supplement idle-deck logic by using agent loops") asks
the coordinator to take on the thing the harness already has. That is not a
gap to fill; it is a boundary to hold.

- **D20 / D27.** Models run on a server separate from the workstation, and the
  workspace lives on the remote. idle-deck is a coordinator: it hands the
  remote a `RunRequest` and gets back a `RunResult`. It never holds a session
  open, never drives a tool loop, never reaches into the remote filesystem.
  Giving the coordinator a reasoning loop would put it on the wrong side of
  the D20 boundary — it would start naming models and holding state that the
  harness owns.

- **D26 / ADR 0013.** `Harness` is `Start` / `Abort` / `Result` only. That
  interface is the whole point of the seam: the worker enforces I4/I5 from the
  harness's returned artifacts without reaching into the remote filesystem. An
  agent loop in idle-deck would need a workspace, a model, and a tool surface —
  all of which D26 deliberately withholds from the interface.

- **D6 / D40.** idle-deck never names a model, provider, or vendor. A reasoning
  loop is a thing that names a model. Running one would require `RunRequest` to
  carry a model, which the whole framework-agnostic property exists to forbid.

- **What idle-deck's loop actually is.** `main.go` polls the queue every
  `IDLE_DECK_POLL_INTERVAL` and dispatches one task per tick. It is stateless
  and dumb — it does not reason, it schedules. D28 makes the dev loop
  stateless by design; the scheduler tick is the same property at runtime.

- **The one LLM-driven pass that exists.** The P3 sweep (D43/D44/D45, ADR
  0019) is the only place idle-deck drives an LLM, and it is deliberately
  narrow: a configured prompt, a fixed cadence, no adaptive backoff, and it may
  only *report* findings — never open a PR, never re-ingest its own write. That
  is the only LLM-driven pass the project permits, and it runs through the
  same harness seam, not a new one.

## Alternatives considered

- **Run agent loops in idle-deck.** Rejected: duplicates the harness's
  reasoning loop, crosses the D20 boundary, and requires naming a model
  (D6/D40). The coordinator would become a second, inferior copy of the thing
  it already delegates to.
- **Let the harness run longer / more complex prompts.** Already covered and
  wanted: per-role budgets and timeouts (D37) set the ceiling, and the harness
  is free to run as many internal tool iterations as the budget allows. The
  loop is the remote's to run, idle-deck's to observe. No decision change.
- **Stream events from the harness back to idle-deck mid-run.** Deferred by
  ADR 0013's alternatives: `Result` blocking keeps the interface minimal, and
  streaming is an internal detail of the adapter's contract in #8. Not an
  agent loop, and not needed for the MVP.

## Consequences

- The scheduler remains a tick, not a driver. Nothing in idle-deck reasons
  about a model, holds a session, or inspects a workspace.
- The boundary is now explicit rather than implicit: if a future ticket wants
  idle-deck to do more than dispatch-and-observe, it must first answer D20 and
  D6, and the answer is likely "no" for the same reasons.
- Issue #34 is closed as answered, not implemented.