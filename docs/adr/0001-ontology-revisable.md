---
Title:   Issue #1's ontology is a revisable draft
Status:  accepted
Supersedes: —
Related: D10, D11, 0002
Source:  #5
---

## Decision

Issue #1's ontology is a **revisable draft, not binding**. The domain model is re-derived from first
principles, not inherited from the handover.

## Rationale

A handover that mixes domain model, operational policy, and implementation choice rots. Locking #1
as the ontology would lock its confusions in — the missing idle signal, the flat `TaskItem`, the
unreconciled timeout sources. The domain model must be owned as its own artifact (0002).

## Alternatives considered

- Adopt #1's ontology verbatim — rejected: it conflates domain, policy, and implementation.
- Adopt it and amend in place — rejected: there is no baseline to amend against.

## Consequences

- The ontology is re-derived; #1 is source material, not the model.
- A kept/reworded/dropped mapping for #1 is required when the ontology is written.
