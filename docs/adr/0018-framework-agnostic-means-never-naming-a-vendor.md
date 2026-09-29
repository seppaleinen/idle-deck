---
Title:   "Framework-agnostic" means never naming a model, provider, or vendor — and the seam is proved by a conformance suite, not a second adapter
Status:  accepted
Supersedes: —
Related: D6, D7, D13, D26, D27, I4, I5, I8, 0004, 0005, 0008, 0013, 0014
Source:  #12 Is framework-agnostic honest with one adapter?
---

## Decision

Two decisions, from one question.

**D40 — the word means what one adapter can prove.** "Framework-agnostic" is a claim that
**idle-deck names no model, provider, or vendor**, and nothing more. It is enforced by the locked
types: `RunRequest` has a `role`, a budget, a timeout, and repository/ticket refs, and **no field a
model name could go in**. The MVP makes **no claim** that a second, structurally different harness
implementation can be dropped in — that claim is deferred, and saying so is part of the decision.

**D41 — the seam is falsified by a conformance suite, and the second adapter waits behind a
trigger.** The MVP harness adapter ships **together with a named conformance suite**: a fixed set of
cases every harness adapter must pass, written against the wire contract rather than against one
implementation. The MVP ships **one** adapter ([D7](0005-one-harness-adapter.md),
[ADR 0005](0005-one-harness-adapter.md)). A second adapter is added when a **trigger** fires, not
speculatively:

1. A second execution model is genuinely wanted — a harness that cannot be reached as a remote JSON/HTTP
   service; **or**
2. the conformance suite has been run against the real remote service and a case turned out to be
   **unfalsifiable** — i.e. the contract cannot be tested from the client side, so a second
   implementation is the only way to test it; **or**
3. a second operator needs a harness the first remote service cannot serve.

## Rationale

**The ticket asked the wrong question, because two later decisions had already moved the answer.**
#12 was written before #8 and asked whether to ship a local-subprocess adapter alongside the remote
one. #8 settled that **the workspace lives on the remote** ([D27](0014-remote-execution-service-contract.md),
[ADR 0014](0014-remote-execution-service-contract.md)): the remote owns checkout, git, and Draft PR
production. A local-subprocess adapter must therefore have *idle-deck* clone the repo, run the agent,
push a branch, and open a PR — which is not twice the cost of one adapter, it is a **reversal of a
locked decision** and a re-opening of the exact question #8 spent itself closing. The ticket's own
escape hatch ("if two, this becomes a backlog item") priced that correctly and did not know it.

**"Framework-agnostic" was carrying two claims, and only one is provable now.** (a) *provider-agnostic*
— idle-deck never names a model or vendor — is **proved by construction**, because the locked
`RunRequest` has nowhere to put a name. (b) *harness-pluggable* — any harness can be dropped in — is
**not provable with one adapter**, and the ticket's worry is correct: an interface validated against
exactly one implementation is an interface guessed at. Keeping the word at its broad reading means
the README promises something the code has never been asked to deliver.

**The guess is in the contract, not the three methods.** ADR 0013's `Start` / `Abort` / `Result` is
three methods over types the ontology already locked. What is genuinely unproven is the **wire
contract underneath**: idempotency by `attempt_id`, server-enforced timeout, confirmed-dead `Abort`,
the failure-code taxonomy, artifacts-as-data. A local-subprocess adapter shares **none** of that — no
idempotency server, no server-side timeout, no remote session id, no bearer/TLS — so it would not
have stress-tested the part most likely to be wrong. A conformance suite aimed at the contract does,
at a fraction of the cost, and it is the same instrument ADR 0008 already pointed at when it said a
second implementation "will reveal a mis-designed interface": make that falsifiable *now*, while the
contract is still cheap to change.

**Pi, opencode, claude, and cursor-agent are not a second adapter — they are a category error, and
worth writing down.** Those are **vendors of agent execution**. A *harness*, in this project's sense,
is the remote service that **owns the workspace**. `pi` would run *inside* the remote: which CLI it
wraps is the remote's business, exactly as [D6](0004-never-name-a-model.md) says idle-deck never names
a model. So "should idle-deck support pi" is not a question this project has, and recording the
reasoning stops it being re-opened every time a new agent CLI ships. The same applies to a second
remote: a second **remote service** is a second adapter; a second **CLI inside one remote** is not.

**Why deferring is not dodging.** The trigger list is the load-bearing half of D41. A second adapter
that is added "sometime" is a promise with no owner; a second adapter gated on three named conditions
is a decision the project has already scheduled for itself. And the deferral is honest about the
limit: the MVP proves its *contract* is testable, and does **not** prove a second `Harness`
implementation compiles against `Start`/`Abort`/`Result`. That gap is real, named, and closed by
trigger 2 if the suite ever shows it cannot be closed any other way.

## Alternatives considered

- **Ship two adapters in the MVP** (remote HTTP + local subprocess, per the ticket) — rejected: it
  reverses [D27](0014-remote-execution-service-contract.md)/ADR 0014, re-opens workspace ownership,
  and shares none of the contract properties most likely to be wrong. Cost is not "double", it is
  "reopen a settled lock".
- **Ship a second *remote* adapter** (a different execution service, same wire shape) — rejected for
  the MVP for the ordinary reason: two implementations of a contract you have never run against a real
  server is two guesses, not one guess plus a check.
- **Keep the broad claim and accept the debt** ("framework-agnostic, trust me") — rejected: the word
  is the project's headline, and a headline that outruns the evidence is the failure mode the decision
  record exists to prevent. D6 is the pattern to copy: it claims the thing it can prove.
- **Conformance suite with no trigger list** ("a second adapter may be added when useful") —
  rejected: unowned and unfalsifiable, which is the same defect as a broad claim.
- **Spec-only, no adapter** — rejected, and unchanged: [ADR 0005](0005-one-harness-adapter.md) already
  settled that the harness is the point.

## Consequences

- **The conformance suite is a required MVP deliverable**, not a nice-to-have, and it is written
  against the wire contract, so it survives the second adapter. Named cases, all client-observable:
  - `Start` is idempotent by `attempt_id` — a retried `Start` returns the **same** `run_id` and starts
    no second execution.
  - `Abort` returns only once the remote has **confirmed** the run is dead, or it was already
    terminal.
  - A run exceeding `timeout_seconds` is killed **server-side** — a client that stops polling must not
    leave a billed run alive.
  - A terminal result **always** carries typed artifacts; `branch_purpose` appears only on
    `kind = branch` ([I4](0014-remote-execution-service-contract.md), [I5](../ontology.md)).
  - `429` with `Retry-After` maps `retryable_failure`; `401`/`403`/`404` map
    `non_retryable_failure`; `timeout` maps `timeout`; wire `cancelled` maps to `preempted` **only
    client-side**, because only the worker knows the abort reason ([I7](../ontology.md), [I8](../ontology.md)).
  - An unreachable service maps `retryable_failure`, never a hang and never a silent success.
- **SSE moves off the #12 hook.** It was documented as the v2 progress path pending #12; it is
  pending a **second adapter**, and nothing else.
- **`docs/architecture/remote-contract.md` is where the suite is specified** — the cases are properties
  of that contract, so they live in it. The `Harness` interface in
  [`boundaries.md`](../architecture/boundaries.md) is unchanged: three methods, and D41 does not add a
  fourth.
- **The claim appears in the README narrowed**, with the deferral stated, so no reader over-reads it.
- **#10 owns building the suite.** Until it passes against a real remote service, the project's
  evidence for the seam is the contract plus the stub ([D38](../operations/operator-surface.md)) — and
  the stub proves the *adapter* works, not that a second implementation would fit.
