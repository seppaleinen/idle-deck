---
Title:   Idle is probed at the harness, and /health must report model reachability
Status:  accepted
Supersedes: 0003
Related: D5, D6, D7, D20, D27, I8, 0006, 0013, 0014
Source:   #11 Install, run, configure: the operator and developer surface
---

## Decision

**Idle is probed at the harness's `GET /health`. And that endpoint must report *model
reachability*, not process liveness** — which is what makes "if it answers" equivalent to the
original definition, "idle-deck can reach the model".

The *definition* of idle is unchanged and is not what this ADR revisits: idle still means
reachable, still means no platform heuristics (no macOS power/thermal/activity probes). What changes
is **where the probe goes**. It goes to the remote execution service, not to the inference server.

No platform idle heuristics are reintroduced, and idle-deck still does not host models (D20).

## Rationale

ADR 0003 said "it probes the inference server". The remote execution service contract (#8, ADR 0014)
later mapped the `IdlePolicy` probe onto the harness's `GET /health` without amending 0003, leaving
the project with an accepted ADR and a locked contract naming two different machines. #11 confirms
the substitution #8 had already made, and pins the one requirement that makes it *equivalent* rather
than merely different.

The substitution is right, for three reasons.

**One URL and one credential instead of two.** Probing the inference server directly would mean a
second endpoint in idle-deck's configuration and, plausibly, a second secret — because an inference
server usually authenticates too. idle-deck already holds a bearer token for the harness on every
single run (D27). Requiring a second credential to answer "am I idle" would be a poor trade for a
signal the harness can already produce.

**No new network dependency.** The harness is a hard dependency of every run: if it is unreachable,
nothing can execute regardless of what the probe says. Probing it adds no failure mode that the
system does not already have.

**The harness knows about the model; idle-deck does not.** D6 forbids idle-deck from naming a
model. An inference server exposes a model, a list of models, and a provider's routing rules.
Asking idle-deck to interpret any of that would put model knowledge back into the coordinator, which
is the exact inversion D6 exists to prevent. The harness already resolves role → model; asking it
"can you reach one?" is a question it can answer without idle-deck understanding the answer.

The requirement that `/health` report **reachability** rather than liveness is the load-bearing part,
and it exists because the two are genuinely different signals. A harness process can be perfectly
alive with the model endpoint down, behind a rate limit, or out of credentials. A pure liveness ping
would certify that state as *idle*, idle-deck would start a run, and the run would fail immediately —
with the probe having said everything was fine. That is precisely the failure D5 exists to prevent,
so without this requirement the substitution would quietly defeat the decision it was meant to serve.

## Alternatives considered

- **Probe the inference server directly** — rejected: a second URL, a plausible second credential,
  and a network dependency idle-deck does not otherwise have. It also contradicts the remote
  contract, which already routes the probe to `/health`.
- **A dedicated new endpoint, e.g. `GET /reachability`** — rejected as a distinction without a
  difference at one remote implementation. D13 is "minimal interfaces, one concrete implementation
  each". If a future remote needs to separate liveness from reachability, that is a v2 wire change
  (the same treatment SSE got), not a reason to add an endpoint now.
- **Treat a bare liveness ping as good enough, and rely on the first run failing** — rejected: it
  converts a cheap proactive check into a wasted attempt that burns one of two retries (D14) and
  posts noise. Retries are a scarce resource for a reason.
- **Probe nothing and dispatch on queue depth** — rejected, and it was rejected in 0003: it guesses
  at user intent instead of measuring it.

## Consequences

- **The `IdlePolicy` seam is unchanged.** One method, injectable, not platform-specific — 0003's
  actual architectural content survives untouched. What changed is the implementation behind it.
- **idle-deck's configuration loses a setting it would otherwise have needed.** No inference-server
  URL, and no inference-server credential. `IDLE_DECK_HARNESS_URL` and `IDLE_DECK_HARNESS_TOKEN` are
  the only two the idle path touches.
- **`remote-contract.md` gains a semantic requirement on an existing endpoint**: `GET /health` must
  reflect model reachability. That file states of itself that a deviation from its locked status "is a
  new decision" — this is that decision, and the contract is amended on its authority rather than
  edited in place.
- **`status` reports harness reachability as evidence of idleness** (see the operator surface), which
  makes a false "idle" visible to an operator rather than only inferable from failed runs.
- **0003 is superseded**, body untouched. Its definition is carried forward in this ADR's first
  paragraph; only the probe's location changed. This is the second real supersession in the project,
  and unlike 0007's it revises a decision's *mechanism* while leaving its *definition* intact —
  which is the harder case, because the surviving text is what a future reader will quote.
