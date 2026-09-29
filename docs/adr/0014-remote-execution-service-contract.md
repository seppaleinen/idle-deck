---
Title:   Remote execution service contract: workspace is remote, contract is typed and pull-based
Status:  accepted
Supersedes: —
Related: D7, D20, D26, D24, D21, D17, I4, I5, I7, I8, 0005, 0006, 0013
Source:  #8 Remote execution service contract
---

## Decision

The idle-deck ↔ remote execution service wire contract ([`docs/architecture/remote-contract.md`](../architecture/remote-contract.md))
is locked. Its spine — the parts that would ripple through other decisions if changed:

1. **The workspace lives on the remote.** The remote owns the checkout, the working tree, git
   operations, and Draft PR production. idle-deck never touches the remote filesystem through the
   interface (`Harness` stays lifecycle-only, ADR 0013) and receives artifacts as **typed data**.
2. **Typed results.** Server outcome vocabulary is `completed | failed | timed_out | cancelled`;
   artifacts use the locked `ArtifactKind` set. idle-deck never parses prose; I4/I5 are checkable
   structurally.
3. **Idempotency by `attempt_id`.** `Start` is idempotent keyed on `TaskAttempt.id`; a retried Start
   returns the existing `run_id` (200), never a second execution.
4. **Server-enforced timeout.** The remote kills its own run at `timeout_seconds` even if idle-deck
   died — no stranded billing.
5. **Cancellation = `DELETE /runs/{id}`**, confirmed dead before `204`.
6. **Progress by status polling** inside the adapter (SSE is the documented v2 path, not MVP).
7. **Bearer token + TLS, zero inbound calls.** The remote holds its own repo-scoped GitHub token.
8. `preempted` **never appears on the wire**; the server only says `cancelled`, and the adapter
   steers it client-side (only the worker knows the abort reason, I7).

## Rationale

Workspace ownership was the unresolved root of #1's `BaseHarness` contradiction: remote HTTP dispatch
and *local* filesystem ops were fused in one interface. D7/D20 (one remote execution service, models
on a separate server) already put execution off the workstation; D26 kept the interface free of an
answer so this contract could give one without redesigning the seam. Remote ownership follows from the
locked map: the worker has no git/fs capability at all, I4's enforcer is the harness adapter on
return, and D21's branch/PR guarantees are remote-side git. The rest of the spine exists to make the
ticket's core fear false: a 3-hour P2 must be provably not hung (polling), a dead daemon must not
leave a billed run orbiting (server timeout), and D14's two-retries-then-escalate must never
double-execute work (idempotency by `attempt_id`).

## Alternatives considered

- **Local workspace** (idle-deck's daemon runs the agent, remote is only a model endpoint) —
  rejected: would re-add the local execution seam #6 removed, contradict D20's "idle-deck
  orchestrates; it does not host inference", and force idle-deck to own git/fs (which the boundaries
  doc's worker explicitly lacks).
- **SSE streaming for progress** — deferred to a v2 adapter (see #12); polling keeps MVP client
  shape minimal and the locked interface does not foreclose SSE.
- **Cooperative-only cancellation** (server polls a flag) — weaker than `DELETE`; **client-renewed
  lease** — inverts the queue's "never strand a task" principle by stranding a *paid* run on a dead
  daemon. Both rejected in favor of `DELETE` + server-side timeout.
- **Opaque result text** — rejected: the remote is the artifact's author; forcing idle-deck to
  reparse its own output is how cross-system vocabularies drift.

## Consequences

- The harness adapter spec in [#10] becomes concrete against this contract.
- I8's `remote_session_id` = remote `run_id`.
- D14/D21/D24 hold together: retries are idempotent at the wire, outcomes land on the locked
  `AttemptOutcome` mapping, and lease-expiry re-queues never re-execute.
- Fog 1 (harness workspace & repo sandboxing) graduates: sandbox ownership is the remote's obligation
  (confine to the checked-out repo); the mechanism is the remote's implementation.
- 429/budget *policy*, secrets lifecycle, and the exact failure-code list remain open (fog onto #11
  and the #10 adapter spec) — the contract pins the taxonomy, not the schedule.