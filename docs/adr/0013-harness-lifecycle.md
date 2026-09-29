---
Title:   Harness interface is execution lifecycle only; workspace ownership deferred to #8
Status:  accepted
Supersedes: —
Related: D26, D6, D7, D20, I8, I4, I5, 0004, 0005, 0006
Source:  #6 Architecture boundaries
---

## Decision

The `Harness` interface is **`Start` / `Abort` / `Result`** — pure execution lifecycle over a remote
execution service. `RunRequest` carries a `role` (never a model name, D6), a budget, a timeout, repo
and ticket refs. `Result` returns a terminal `RunResult` with `AttemptOutcome` and `Artifact` data.
**No `prepare_workspace`, no local git operations, no `preserve_debug_state` method.** Who physically
produces a branch or opens the PR is deliberately left to the remote-contract ticket (#8); this
interface forecloses neither a remote nor a local workspace.

## Rationale

The original `BaseHarness` bundled remote HTTP dispatch with *local* filesystem operations
(`prepare_workspace`, `preserve_debug_state`) — two incompatible architectures sharing one interface
name. D7/D20 put execution on a separate server, so the interface must not pretend the harness reads
idle-deck's disk. Workspace ownership is the open question in #8; if the boundary had baked in an
answer, #8 would be decided by accident. Keeping the interface lifecycle-only makes cancellation
real (I8: a running attempt is abortable via its `remote_session_id`, which `Abort` keys on) and
keeps artifacts as *data*, so the worker enforces I4/I5 from what the harness returned without
reaching into the remote filesystem.

`preserve_debug_state` is not a harness method because I5's enforcer is already "Worker, at
escalation": the worker produces the `branch` artifact (with `branch_purpose = debug`) from the run
result. The harness is not asked to do git.

## Alternatives considered

- Keep `prepare_workspace` / `preserve_debug_state` in the interface — rejected: forces a workspace
  answer now, preempting #8; and on a remote server these methods imply filesystem access idle-deck
  does not have.
- A `Harness.Poll`/stream API instead of a blocking `Result` — rejected for MVP: one remote HTTP
  adapter, one call shape; streaming is an internal detail of the adapter's contract in #8, and
  `Result` blocking keeps the interface minimal. The interface does not foreclose a later
  streaming/events method if #8 needs it.

## Consequences

- The remote-contract ticket (#8) is the *content* under this interface: what the HTTP endpoints
  are, and — decisively — where the workspace lives.
- Harness adapter never accepts a model name (D6); role is derived from tier (I11).
- `Abort(RunID)` is the cancellation contract; preemption (D16) drives it from the worker.
- Successful P2 runs return the `pull_request` artifact (I4); escalation produces the debug branch
  artifact worker-side (I5).