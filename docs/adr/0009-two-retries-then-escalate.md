---
Title:   Two automated retries, then escalate
Status:  accepted
Supersedes: —
Related: D14, D16, D17, I6
Source:  #3 charting / #1
---

## Decision

**Two automated retries**, then escalate. On the third failure: preserve the debug branch, comment
diagnostics, apply `needs-human`, drop from queue. Non-retryable failures escalate without burning
retries.

## Rationale

From issue #1, stated without the ambiguity of the original. Retries are per-lineage-root (I9), so
redefinition never re-inherits consumed retries; the loop is bounded by a human being in it.

## Alternatives considered

- One retry — rejected: too brittle for long-running agent tasks.
- Retry forever with backoff — rejected: unbounded token spend and no human signal.
- Shared lineage budget — rejected: punishes the human who redefined the task for engaging.

## Consequences

- `retry_count` is derived (`len(attempts)`), never stored (ontology Q1/b).
- Exhausted budget ⟹ `escalated`, never terminal `failed` (I6).
- The escalation path preserves a debug branch artifact (I5).
