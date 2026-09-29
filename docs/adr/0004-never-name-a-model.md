---
Title:   idle-deck never names a model
Status:  accepted
Supersedes: —
Related: D6, D7, 0006, 0005
Source:  #3 charting
---

## Decision

idle-deck never names a model. It passes a **role** (`plan` / `do` / `sweep`) plus a budget to the
harness. The harness decides the model. The tier→role mapping is owned by idle-deck: `P0→do`,
`P1→plan`, `P2→do`, `P3→sweep`.

## Rationale

Kills the Brain-vs-Worker box from issue #1. Model choice is an adapter concern: which model is the
best planner changes monthly, and idle-deck must not be pinned to it. The tier→role mapping is the
same responsibility split one level up — idle-deck owns the *kind* of work, not the *vendor* of the
work.

## Alternatives considered

- Tier→model mapping table (as in #1) — rejected: couples idle-deck to specific models.
- Role as a stored field on the task — rejected: a `P2` task with role `plan` would be representable,
  a legal-but-meaningless state.

## Consequences

- The remote harness contract (#8) carries a `role` field, never a model name.
- The ontology derives `role` from `tier` (invariant I11).
