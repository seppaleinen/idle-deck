---
Title:   Minimal interfaces, one concrete implementation each
Status:  accepted
Supersedes: —
Related: D13, 0007, 0005
Source:  #3 charting
---

## Decision

Minimal interfaces, **one concrete implementation each**: GitHub tracker, SQLite queue, one remote
harness.

## Rationale

Prevents premature generality while keeping the seams real. The point of an interface is to have a
boundary, not to have implementations. A single implementation per seam is the honest minimum: it
keeps the seam honest (a second implementation will reveal a mis-designed interface) without funding
a zoo.

## Alternatives considered

- Multiple implementations per seam — rejected: premature generality; a startup MVP would be writing
  Redis, RabbitMQ, and Dragonfly front-ends on day one.
- No interfaces, concrete structs — rejected: the boundaries are the point of the exercise.

## Consequences

- The architecture-boundaries ticket (#6) defines exactly four seams: tracker, queue, harness,
  worker.
- The backlog (#10) builds one implementation each.
