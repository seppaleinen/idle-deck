---
Title:   GitHub event contract: idle-deck polls, labels trigger tiers, dedupe is a stable resource key
Status:  accepted
Supersedes: 0007
Related: D12, D13, D14, D16, D17, D19, D22, D23, D24, D25, D27, I1, I9, I10, I11, 0011, 0012, 0014
Source:  #9 GitHub event contract
---

## Decision

The idle-deck ↔ GitHub contract ([`docs/architecture/event-contract.md`](../architecture/event-contract.md))
is locked. Its spine — the parts that would ripple through other decisions if changed:

1. **idle-deck polls; it has no inbound surface.** No webhook receiver, no listening socket, no
   tunnel, and therefore no signature scheme to verify. Default tick 60s, with conditional requests
   so a quiet tick costs nothing.
2. **The configured repository list is the allowlist**, and `allowlisted` is still checked at
   ingestion, so I1 keeps a real enforcer rather than becoming a property of configuration. This
   replaces 0007's "the repository comes from the webhook's own origin" with intent unchanged.
3. **One watermark per repository**, seeded at first observation, advanced to each tick's *start* so
   windows overlap. A fresh install replays no history; a daemon returning after a week catches up a
   week. Adapter state, not a domain entity.
4. **Every tier has a trigger.** P0 `idle-hotfix` · P1 issue creation (bots excluded) · P2
   `idle-ready` · P3 a local scheduler · redefinition `idle-redo`. The ontology's two unreachable
   tiers are reachable.
5. **Dedupe is a stable resource key**, `github:issue:<repo>:<number>:<trigger>`, inheriting the
   queue's existing per-key idempotency (D24). Level-triggered reads observe *state*, so
   re-observation is the normal case and is free.
6. **The label vocabulary is four names**, all `idle-`-prefixed: `idle-hotfix`, `idle-ready`,
   `idle-redo`, `idle-needs-human` (D29). The prefix is a collision-safety property, not a style.
7. **idle-deck owns the trigger label's lifecycle** — removed on any terminal state,
   `idle-needs-human` added on escalation. Nothing idle-deck writes can re-trigger it, because the
   only label it writes is not a trigger.
8. **The prompt is the issue's title and body, verbatim.** Structure travels in `RunRequest` fields;
   intent travels in the prompt, because `prompt` *is* the task's identity.
9. **Closing an issue does nothing.** No cancellation, deliberately.

## Rationale

#9 is where "most of the integration risk lives", and the risk turned out to be concentrated in one
choice: how work arrives. Every other question on the ticket — idempotency, loop prevention, the
event set, the shape of the allowlist — is downstream of it, which is why delivery was the first
question asked rather than one of six.

Polling wins on four counts. **Scope:** D4 (single-user, one machine) and D19 (CLI only) make a public
endpoint a liability with no upside. **Trust:** it inverts 0007's mechanism for the better —
idle-deck *names* the repositories it reads, so deny-by-default becomes a config list rather than a
filter on traffic from anyone on the internet. **Durability:** a level-triggered read is resumable, so
a daemon that was down for a week finds a week of work; GitHub does not replay webhooks, so the
webhook path loses exactly the events that happened while you were asleep — the wrong failure for a
tool whose premise is that it works while you are not looking. **Value:** the latency is worthless
here. A 60s floor sits beside a 15-minute P1 and a 3-hour P2, and 60s amply serves P0 preemption
(D16). A token was required either way (60/hour unauthenticated versus 5,000/hour authenticated), so
webhooks would have added a second credential and a third failure mode for no gain.

Tiers followed from one observation: **P0 must arrive mid-run** (its only behaviour is preemption), so
it must be something a human applies from wherever they are — a label. **P3 is not a reaction to
anything** — it is idle-deck manufacturing work for its own repository when there is idle capacity, so
it belongs to a local scheduler and to nothing on GitHub. P1 and P2 then fall out as #1 had them, with
P1's one addition being the exclusion of bot-authored issues.

Redefinition needed a home because escalation is terminal (I10) and recovery is a *new* task with a
fresh retry budget (I9). A label keeps it on the single ingestion path, and it doubles as the honest
answer to "run it again" — which is why the dedupe key can be a stable resource key instead of an edge
id. That single choice removes a whole class of machinery: no delivery ids, no redelivery handling, no
"was this label ever removed" state machine, because re-observing a condition is simply free.

## Alternatives considered

- **Webhook receiver on a public endpoint** (ngrok/cloudflared/`gh webhook forward`) — rejected on all
  four counts above. Not deferred: a future webhook adapter is a second `TrackerSource`
  implementation with its own idempotency regime, which is the right answer for a hosted multi-user
  idle-deck — the thing D4 defers.
- **Hybrid (webhook if configured, else poll)** — rejected: two ingestion paths, two idempotency
  regimes, and one contract to keep straight, for a latency that buys nothing at this timescale.
- **Edge keying via the `labeled` event id** (`GET /repos/{o}/{r}/issues/{n}/events` does expose an id
  and a timestamp) — rejected: an extra call per candidate issue per tick plus a removal-tracking state
  machine, to buy the ability to re-apply the same label, which `idle-redo` states more plainly.
- **A GitHub App instead of a fine-grained PAT** — rejected for the MVP: it buys per-repo scoping we
  already get from the PAT plus an installation flow D4 defers. Right answer the moment idle-deck is
  installed for other people.
- **Bare label names** (`ready`, `hotfix`) — rejected: a repository already using `ready` in its own
  workflow would silently start spawning agent runs. Hijacking a human's label vocabulary is the worst
  available failure for a tool that writes labels back.
- **A configurable prompt template** — rejected: it makes task identity a function of configuration,
  and one template bug would corrupt every task's identity at once.
- **Close = cancel** — rejected, and it is the one that costs something. `Release` exists in the Queue
  interface for a leased-but-unstarted task, so the *mechanics* were cheap; the blocker is the
  domain. `TaskState` has nowhere to put "dropped", and the ontology states that changing a closed
  value set is a migration. Silently deleting the row is worse — it destroys the provenance that makes
  escalation worth having. The honest cost is recorded instead: you can burn a 3-hour P2 by closing
  its issue.
- **CLI enqueue for P0 / redo** — deferred to the operator surface (#11): a second ingestion path
  with its own provenance story, buying only a tier override.

## Consequences

- **ADR 0007 is superseded.** Its deny-by-default intent and its `Repository` entity survive
  unchanged; its "repository comes from the webhook's own origin" mechanism does not. D30 is the
  table-tier form, taken from this ADR.
- The ontology's open question "P0 and P3 have no trigger" is closed. Every tier in `TaskTier` now has
  a producer.
- The allowlist's justification changes shape: it is no longer "not just string-matching against a
  config list" (0007), it is "a config list, still checked at ingestion as a modelled invariant" (I1).
- The tracker adapter spec in [#10](https://github.com/seppaleinen/idle-deck/issues/10) becomes
  concrete: four queries per repository per tick, a cursor table, one ingestion path, four literal
  label strings.
- `TriggeredBy.event_type` gains a closed vocabulary (`issue.opened`,
  `issue.labeled:<label>`, `schedule.sweep`) and its `dedupe_key` is the stable resource key — the
  "delivery id" wording inherited from the webhook assumption is corrected.
- Two ADR-tier seams are now load-bearing in a new way: ADR 0012 (the tracker split) is what lets a
  future webhook adapter arrive without touching the writer, and ADR 0011 (the queue lease) is what
  makes per-key idempotency free.
- **Read ADR 0012 with this one.** 0012's *decision* — two interfaces, one implementation, inbound
  parse separated from outbound write — is unchanged and not superseded. Only its illustrative
  mechanism ("webhook → Task") is now wrong: 0012 said what the inbound side is *for*, this ADR
  decides what delivers it. 0012's body stays as written.
- **Still open, now sharpened:** sweep *content* (the trigger is settled; the job is a configured
  placeholder prompt), cost/budget policy, secrets lifecycle, live cancellation, label colours and
  install-time creation (#11), a CLI tier override (#11), and whether `since` composes with `labels`
  (verify in the #10 spec — correctness does not depend on it, only cost).
