---
Title:   Exactly one harness adapter in the MVP
Status:  accepted
Supersedes: —
Related: D7, 0004, 0006
Source:  #3 charting
---

## Decision

MVP ships **exactly one** harness adapter, talking to a remote execution service over JSON/HTTP.

## Rationale

Deepest cut available. The point is the interface — adapter *count* is not a feature. Whether
"framework-agnostic" is honest with exactly one adapter is a named open question
(#12), not something to pre-solve by building a second adapter before the first is proven.

## Alternatives considered

- Ship two adapters (one remote HTTP, one local subprocess) — rejected for MVP: it would stress-test
  the seam early, but at double the cost before the seam exists.
- Zero adapters, spec-only — rejected: the harness is the whole point.

## Consequences

- One concrete harness in the MVP; the remote contract (#8) is the one interface to get right.
- #12 decides whether "framework-agnostic" is a promise the code can keep.
