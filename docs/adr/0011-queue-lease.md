---
Title:   Queue lease semantics: dequeue is a lease, never a pop
Status:  accepted
Supersedes: —
Related: D14, D16, D17, I6, I7, 0009
Source:  #6 Architecture boundaries
---

## Decision

`Queue.Dequeue` returns a **lease with an expiry**, not a destructive pop. The worker must `Ack` or
`Nack` (or `Release` before starting) before the lease expires. **Expiry without ack is recorded as
`timeout` on the abandoned attempt and the task is re-queued, consuming one retry.** There is no
"lost task" path and no infinite loop; after the retry budget is exhausted, I6 escalates as usual.

`max_concurrent_jobs` is a worker-side parameter, not a queue property. The lease is per-item, so
the same seam supports N workers without change.

## Rationale

A daemon crash mid-run must not strand a task (D14). With a destructive pop, a crash loses the task
— recoverable only by manual re-entry. With a lease, a crash becomes a retryable failure like any
other (D17: a killed run is retryable), and the retry/escalation path needs no special case. The
ontology already reserved the obligation (I8: an attempt in `running` is abortable) but the *mechanism*
was the map's highest-consequence unknown; this decision answers it: the lease is the mechanism.

Recording expiry as `timeout` specifically (rather than inventing a new outcome) keeps the
`AttemptOutcome` value set closed (only five members) and treats a supervised kill as
indistinguishable from a time-out at the boundary — which is honest: at the queue's edge they are.

## Alternatives considered

- Destructive pop — rejected: a daemon crash loses the task; contradicts D14's "never strand".
  Choosing it would have forced a manual recovery path and a "lost task" concept nowhere in the
  ontology.
- Permanent claim (lease never expires until ack/nack) — rejected: a daemon crash would lease the
  task forever, stranding it — the same failure as the pop, with extra steps.
- New `AttemptOutcome.abandoned` member — rejected: the ontology's five outcomes already cover the
  distinction (timeout); an unreachable/unused enum member is a state nobody has thought about
  (the same reasoning that cut `aborted` from the value set).

## Consequences

- Concrete SQLite queue implements Dequeue as one transaction claiming the highest-tier item with a
  lease deadline; expiry is detected on read/re-claim.
- Retry policy reads `timeout` from an abandoned attempt exactly like a supervised kill
  (D17, [ADR 0009](../adr/0009-two-retries-then-escalate.md)).
- The worker loop owns `max_concurrent_jobs`; N workers share the same Queue seam.
- Preemption (D16) rides the event channel bound to this seam, not on a second lease mechanism
  (see 0013 for the harness side, and the boundaries document).