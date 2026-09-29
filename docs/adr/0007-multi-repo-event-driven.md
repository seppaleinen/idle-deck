---
Title:   Multi-repo, event-driven, deny-by-default allowlist
Status:  accepted
Supersedes: —
Related: D12, I1
Source:  #3 charting
---

## Decision

Multi-repo, **event-driven**. The repository a task targets comes from the webhook's own origin.
Deny-by-default **allowlist**.

## Rationale

The repository field is derived, not configured per task. The allowlist is a safety rail, not a
feature: it is what makes "which repos may this daemon touch" a real, checkable invariant (I1)
rather than string-matching against a config list.

## Alternatives considered

- Single-repo MVP — rejected at charting (Q9 chose multi-repo via webhook origin).
- Trust webhook origin as-is, no allowlist — rejected: risky if the service is ever exposed or
  webhooks are misrouted.

## Consequences

- `Repository` is a first-class ontology entity with `allowlisted` as a field (I1).
- The event contract (#9) must carry the origin repo and gate on the allowlist at ingestion.
