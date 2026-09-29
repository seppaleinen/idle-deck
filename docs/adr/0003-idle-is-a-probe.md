---
Title:   Idle means: idle-deck can reach the model
Status:  superseded
Superseded by: 0016
Related: D5, D7, D20, 0006
Source:   #3 charting
---

## Decision

Idle means: **idle-deck can reach the model.** It probes the inference server; if it answers, the
engine is considered idle. No platform idle heuristics (no macOS power/thermal/activity probes).

## Rationale

Replaces hand-waving with a testable signal. "Idle compute cycles" was the project's whole pitch but
#1 had no idle signal at all. The probe is injectable, not hardcoded to a platform, and it keeps the
model server (0006) honest: reachable is the only thing that matters.

## Alternatives considered

- CPU/IO/UI-activity heuristics — rejected: platform-specific, untestable, guesses at user intent.
- Cron/triggered batches — rejected: makes "idle" an external orchestration requirement instead of a
  domain-signaled property.

## Consequences

- The worker gates on a probe against the inference server.
- The ontology names this as a *responsibility* (an `IdlePolicy` seam); the interface is the
  architecture-boundaries ticket's (#6) job.
