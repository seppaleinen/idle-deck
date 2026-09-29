---
Title:   The ontology is a standalone document
Status:  accepted
Supersedes: —
Related: D10, D11, 0001, I1-I11
Source:  #5
---

## Decision

The ontology is a **standalone document** (`docs/ontology.md`), separated from policy and from
implementation choice.

## Rationale

Domain model, operational policy, and MVP choices have different lifetimes. Merging them is what
makes handovers rot. A standalone document can hold entities, value sets, and invariants without
dragging along retry counts or Go package names.

## Alternatives considered

- Ontology inside AGENTS.md — rejected: AGENTS.md is a decision log; the ontology is a model, with
  entities and invariants, not decisions.
- Ontology as issue #1's body — rejected: association with the handover would keep it "draft".

## Consequences

- `docs/ontology.md` is the canonical source for entities and invariants.
- Decisions that *change* the ontology are recorded as decisions (D-space), not inside the ontology.
- The ontology's own references (`I1`–`I11`) cite concepts, not decisions.
