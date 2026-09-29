---
Title:   Tracker split: inbound parser and outbound writer are separate interfaces
Status:  accepted
Supersedes: —
Related: D25, D13, I1, I4, 0008
Source:  #6 Architecture boundaries
---

## Decision

The tracker seam is **two interfaces, one concrete implementation**: `TrackerSource` (webhook →
derived, allowlist-checked `Task`; no side effects) and `TrackerSink` (comment/label writes; no
parsing). The ingress handler — core app code, not an adapter — calls `Parse` then `Queue.Enqueue`.
PR production is **not** a tracker method; a succeeded P2 attempt's `pull_request` artifact is the
harness's to produce (I4).

## Rationale

#1's `BaseTracker` fused an inbound parser with a write-capable client. Inbound is webhook-driven
and returns a `Task`; outbound is fire-and-forget side effects. Fusing them couples a parser to a
write client it never needs, makes each side hostage to the other's test setup (an HTTP responder
for parsing, a payload fabricator for writing), and blurs *what must never do* (a parser that writes
comments is a bug factory).

The interest in *preventing premature generality* (D13) is about implementation count, not interface
count: one GitHub type implements both interfaces, so the split costs nothing while sharpening each
contract. The split also fixes where I1's allowlist gate lives — at ingestion, in the parser that
sees the origin repo — so the trust boundary is an invariant, not a convention (ADR 0007).

## Alternatives considered

- One `Tracker` interface with both Parse and Comment/SetLabels — rejected: couples the read and
  write lifecycles, muddies testability, and lets a consumer hold a write client it never needs.
- Tracker performs the enqueue itself (Parse → Enqueue internally) — rejected: the tracker is then
  coupled to the queue seam; the core ingress is the meeting point and remains the only place that
  composes seams.
- TrackerSink opens the draft PR — rejected: the ontology binds the `pull_request` artifact to the
  harness on return (I4); a tracker-side PR would double the PR producers and undercut I4's enforcer.

## Consequences

- Read path is testable without an HTTP client; write path is testable without a parser.
- I1's gate is enforced by `TrackerSource.Parse`, at ingestion.
- `TriggeredBy.dedupe_key` is filled by `Parse`; redelivery idempotency is answerable from stored
  data in the event-contract ticket (#9).
- If the workspace-ownership decision (#8) ever lands on a fully local workspace, the seam change
  is one added method on TrackerSink, not a redesign.