# idle-deck ↔ GitHub: event contract

What makes a GitHub issue a running idle-deck task, and what idle-deck writes back onto the issue.

**Status:** locked by [GitHub event contract
#9](https://github.com/seppaleinen/idle-deck/issues/9) — binding for the MVP tracker adapter. Decision
record: [ADR 0015](../adr/0015-github-event-contract.md) (**D28** spine, **D29** vocabulary, **D30**
supersedes D12).

Every label string in this document is literal. Where a name appears here, it appears in
`AGENTS.md` D29, in the backlog, and in tests — the three are the same vocabulary, not three
descriptions of one.

---

## 0. Delivery — the root answer

**idle-deck polls the GitHub REST API on an interval. It has no inbound network surface at all.**

There is no webhook receiver, no listening socket, no tunnel, and therefore **no signature scheme to
verify**. The ticket asked whether the daemon is a public HTTPS endpoint or polls; polling dissolves
the question rather than answering it, which is the better outcome: there is no secret whose rotation
or leak is a security concern, and nothing on the internet can reach the daemon.

What replaces edge-triggered events is a set of **state queries** run per allowlisted repository on
every tick (default **60s**, per GitHub's own `X-Poll-Interval` guidance for event-style endpoints).

The properties this buys, in the order they mattered:

1. **Deny-by-default becomes a config list, not a filter on hostile traffic** (D30). idle-deck names
   the repositories it reads. Nobody else can introduce one.
2. **Level-triggered is resumable.** A daemon that was down for a week returns and finds a week of
   work. GitHub does not replay webhooks, so the webhook path silently loses exactly the events that
   happened while you were asleep — which, for a tool whose entire premise is "it works while I'm not
   looking," is the wrong failure.
3. **A credential was required either way.** The unauthenticated REST limit is 60 requests/hour;
   authenticated is 5,000/hour. A token is not optional, so the webhook path would have added a
   *second* credential (the secret) and a third failure mode (the tunnel) for no gain.
4. **The latency is worthless here.** A 60s floor sits beside a 15-minute P1 and a 3-hour P2 (D17).
   Sub-second triggering buys nothing when the work it triggers runs for hours, and 60s is ample for
   P0 preemption (D16).

**Cost.** Per repository per tick: four queries (§8), mostly returning nothing. At the default 60s
across five repositories that is ~1,200 requests/hour against a 5,000/hour budget. Conditional
requests cut it much lower — a tick that finds nothing changed returns `304`, which **does not consume
primary rate limit** — so the cadence is not a tax. Two secondary limits are respected by
construction: 900 points/minute (a `GET` is 1, a write is 5) and 500 content-generating requests/hour
(comments and label writes).

## 1. The allowlist — where I1 is enforced

**The configured repository list *is* the allowlist.** `Repository.allowlisted` is still read and
checked at ingestion, so I1 keeps a real enforcer instead of becoming an emergent property of
configuration.

The check is deliberately redundant. The poller only ever queries repositories that were configured,
so the gate cannot fail in normal operation — and it is still there because the case it protects
against is real: a repository removed from configuration while a task is still in flight. I1 is a
rule with an enforcer, not a coincidence.

This **supersedes D12's** "the repository comes from the webhook's own origin" clause. The intent —
multi-repo, deny-by-default, explicit trust — is unchanged; the origin-of-inbound-traffic mechanism
was one implementation of it, and polling is a better one (**D30**).

## 2. The watermark

One cursor per repository, held in the tracker adapter's own SQLite state.

**It is seeded at first observation** — the moment a repository is added, or the daemon's first tick
over a configured repository. A fresh install therefore replays no history. Afterwards every tick
queries `since=<watermark>`, so a daemon returning after a week picks up a week of changes and ignores
the untouched backlog.

The mechanics look wrong at first glance and are not:

- `since` filters on **`updated_at`**, not `created_at`. That is sound: an issue created after the
  watermark has `updated_at ≥ created_at`, so it necessarily falls inside the window. "Newly filed" is
  then `created_at > watermark`, checked client-side.
- Results are paged **ascending by `updated`** (`sort=updated&direction=asc`, `per_page=100`,
  following `Link: rel="next"`), so the cursor advances monotonically instead of skipping items when
  a window exceeds one page. The watermark is only advanced **after pagination completes**.
- The watermark advances to the tick's **start** timestamp, not its end. Consecutive ticks therefore
  *overlap* by design, and the overlap is free because dedupe is idempotent (§5). Advancing to the
  end would risk skipping an issue that changed mid-tick.
- A **failed** tick — network error, 5xx, rate limited — does not advance the watermark.

**The cursor is adapter state, not a domain entity.** The ontology deliberately has no `Event`
entity, and `TriggeredBy` records what caused a task that *exists* — not what was skipped. Adding a
cursor entity would be modelling the ingestion mechanism, not the domain.

## 3. Triggers and tier derivation

| Tier | Role | Trigger | Source | Notes |
|---|---|---|---|---|
| **P0** | `do` | `idle-hotfix` label applied | label query | Preempts a running P2 (D16/I7) |
| **P1** | `plan` | Issue created | watermark query, `created_at > watermark` | Bot-authored issues excluded |
| **P2** | `do` | `idle-ready` label applied | label query | The ordinary path |
| **P3** | `sweep` | Local scheduler tick | no query at all | At most one queued/running per repository per bucket (§5) |
| *(redefinition)* | *P1 default* | `idle-redo` label applied | label query | New task, `derived_from` the escalated one, fresh retry budget (I9) |

**P0 — a label, because it must arrive mid-run.** P0's only behaviour is preemption: it has to show
up *while a 3-hour run is in flight*, from wherever the human is. A label is the only trigger that
does that, and it reuses the single ingestion path, so the allowlist gate, dedupe, and
loop-prevention rules are identical to P2's. A CLI enqueue would need a second ingestion path with
its own provenance, and a task with no issue behind it has nowhere to point — the ontology requires
every `Task` to carry a `TrackerRef`.

**P1 — issue creation, with bot-authored issues excluded.** This is the product's premise: file an
issue, walk away, find a plan waiting. `user.type == "Bot"` issues are skipped, or Dependabot filing
in an allowlisted repository would cost a plan run each time.

**P2 — a label.** Unchanged from #1.

**P3 — a local scheduler, because a sweep is not a reaction to anything.** P3 is the one tier idle-deck
manufactures for itself when there is nothing else to do. Nothing on GitHub should be able to summon
one; a label would let a stray tag spawn sweeps, and a sweep's *content* is still open policy. Two
things make the trigger cheap: the worker already gates on `IdlePolicy` (D5), so a sweep enqueued on a
busy machine simply waits and the trigger needs no idle-awareness of its own; and the per-bucket
dedupe key (§5) makes pile-up impossible under `max_concurrent_jobs: 1` (D8).

**Redefinition — `idle-redo`, because escalation is terminal.** I10 forbids reviving an escalated
task; recovery is a *new* task with `derived_from` and a fresh retry budget (I9). So "try again" has
to arrive somewhere, and a label keeps it on the one ingestion path. The tier defaults to `P1` — the
ontology's prescribed default, because a redefinition is by definition a changed problem statement.
An explicit tier override is a CLI concern and belongs to the operator surface
([#11](https://github.com/seppaleinen/idle-deck/issues/11)), not to this contract.

## 4. The label vocabulary

Four labels, one namespace, lowercase, hyphen-separated, no spaces, colons, or slashes:

| Label | Direction | Meaning |
|---|---|---|
| `idle-hotfix` | human → idle-deck | Run this now, at P0 |
| `idle-ready` | human → idle-deck | Run this at P2 |
| `idle-redo` | human → idle-deck | Re-run this after an escalation |
| `idle-needs-human` | idle-deck → human | Escalated; a person is required (D14) |

**The `idle-` prefix is a safety property, not a style choice.** Without it, a repository already
using `ready` in its own workflow would silently start spawning agent runs on every issue it labels —
idle-deck would hijack a human's vocabulary, which is the worst available failure for a tool whose job
is writing labels back. The prefix also makes every label idle-deck reacts to or writes unmistakably
its own, which is what makes §6's "ignore our own events" rule and any later audit trivial.

D14 wrote the escalation label as `status: needs-human`. **Its behaviour is untouched; only the
literal string is normalised here**, and D29 is the record of the exact strings. The ticket's
hyphen-versus-slash complaint turns out to be between a *label* and a *branch* name (`debug/<task_id>`,
D15) — two namespaces that never meet. The branch convention stands untouched.

Longest name: 16 characters, well under GitHub's 50-character limit. No install step is required for
the *polling* side: a label that does not exist simply matches no issues. Creating the four labels
with sensible colours is an install-time nicety in
[#11](https://github.com/seppaleinen/idle-deck/issues/11) — the contract does not depend on GitHub's
create-on-assign behaviour, which is undocumented.

## 5. Dedupe keying and idempotency

`Enqueue` is already idempotent per `dedupe_key` (D24,
[ADR 0011](../adr/0011-queue-lease.md)), so idempotency is inherited rather than reimplemented. The
only question is what the key is:

```
github:issue:<owner>/<repo>:<issue_number>:<trigger>     trigger ∈ { created, hotfix, ready, redo }
github:sweep:<owner>/<repo>:<period-bucket>              P3, one per bucket per repository
```

A **stable resource key**, not a delivery id. There is no delivery id under polling, and a level-
triggered read observes *state*, not *events* — so re-observing a condition is the normal case, not an
anomaly, and it must be free. It is: the same key enqueued twice is one task.

**P3's key makes its guard the same rule as everything else's.** The bucket is the scheduler's current
period, so repeated ticks within one bucket collapse to a single task, and the guard is just the key's
presence in a non-terminal state. Stated precisely, the invariant is **at most one sweep queued or
running per repository per bucket**, not per repository: a sweep that outlives its bucket does not
block the next one. In practice that is a distinction without a difference, since `max_concurrent_jobs`
is 1 (D8) and a sweep that is still running is by definition not idle.

**Two limitations, recorded rather than hidden:**

- Re-applying the *same* trigger label does nothing, by design. The human expresses "run it again"
  with `idle-redo`, which is a different key.
- A label removed and re-applied **inside one 60s window** is invisible.

The alternative considered and refused was edge keying — including the `labeled` event id from
`GET /repos/{o}/{r}/issues/{n}/events`, which *is* available and does carry an id and a timestamp. It
costs an extra API call per candidate issue per tick, plus a "was it ever removed" state machine just
to tell re-application from steady state, in order to buy the ability to re-apply the same label — which
`idle-redo` expresses more honestly. If a future adapter genuinely needs edge semantics, the event
endpoints are there; they are not needed for this contract.

## 6. Label lifecycle and loop prevention

**idle-deck owns the trigger label's lifecycle.** On any terminal state it removes the trigger label
it reacted to; on escalation it adds `idle-needs-human`. (For a P1 there is no trigger label to remove,
so the rule simply does not fire.)

Note what this is *not* doing: the dedupe key already makes removal unnecessary for correctness, so
this is purely about the human's view of the board. Leaving `idle-ready` on an issue whose Draft PR is
open is a lie that also blocks re-queuing.

**The loop-prevention rules, complete:**

1. idle-deck writes exactly one label — `idle-needs-human` — and it is not a trigger. Nothing idle-deck
   writes can re-trigger it.
2. Removal cannot re-trigger: absence is precisely what the label-filtered query asks for.
3. **Comments are output only.** They are never read, so commenting cannot trigger anything. This is
   also why a comment-driven instruction channel does not exist (§8).
4. The trigger label is the *only* thing that enqueues; no other label change does.

There is no rule "ignore events authored by idle-deck's own login" here, because there is no way to
write one: attributing *who* applied a label needs the per-issue events endpoint, which this contract
does not call (§8). The webhook contract would have needed that filter, because a bot's own label
write re-fires `issues.labeled`. Polling does not — the loop is already broken by rule 1. Recorded
here so a future adapter treats it as a known absence rather than a bug.

**A direct answer to the ticket's round-trip question:** no, writing `idle-needs-human` does not
re-trigger the daemon, and no authorship filter is consulted to achieve it. A trigger fires on a
trigger label, and the only label idle-deck writes is not one.

## 7. Prompt derivation

`Task.prompt` is the issue's **title and body, verbatim**, under a short fixed provenance header:

```
repo:   owner/repo
issue:  #123
url:    https://github.com/owner/repo/issues/123
tier:   P2  (role: do)

<issue title>

<issue body>
```

**Structure travels in fields; intent travels in the prompt.** Repository, ticket, tier, and role are
already structured members of `RunRequest` (ADR 0014), so none of them are inlined into prose for the
agent to parse.

The ontology makes `prompt` the task's identity — a `Task` is immutable except for `state`, and its
identity *is* its problem statement — so the prompt must be the human's words: no summarising, no
reordering, no template rendering. A configurable template is tempting precisely because it is
configurable, but it makes identity a function of configuration, and a template bug would silently
corrupt every task's identity at once.

If the body is empty, the prompt is the title alone. For P3 the prompt is a **configured string**; what
a sweep actually looks for is open policy (§10).

## 8. The query set, and its negative list

Per allowlisted repository, per tick:

| # | Query | Yields |
|---|---|---|
| 1 | `GET /repos/{o}/{r}/issues?state=open&labels=idle-hotfix&since=W` | P0 candidates |
| 2 | `…&labels=idle-ready&since=W` | P2 candidates |
| 3 | `…&labels=idle-redo&since=W` | Redefinition candidates |
| 4 | `GET /repos/{o}/{r}/issues?state=open&sort=updated&direction=asc&since=W&per_page=100` | P1 candidates (`created_at > W`, bots excluded) |
| — | P3 scheduler — no query | P3 |

Every response is filtered for the `pull_request` key: **the issues endpoint returns pull requests as
issues, and a pull request is not a task.** `state=open` throughout, so a closed issue never triggers.

Passing `since=W` on the three label queries as well as the watermark query lets one cursor do double
duty — detecting new issues *and* label changes — so a labelled-but-unchanged issue is not even
returned. Both parameters are documented on the same endpoint with no stated exclusion, but the
*combination's behaviour* is an assumption to verify in the
[#10](https://github.com/seppaleinen/idle-deck/issues/10) adapter spec. If it turns out `since` does
not compose with `labels`, the unconditional label query plus the dedupe key is still **correct** — it
is only chattier. One label is passed per query, so GitHub's multi-label AND/OR semantics never arise.

**Never handled:** `issue_comment` (comments are output only; no conversational instruction channel),
`pull_request` (idle-deck opens Draft PRs and never reviews them, D22), `ping` and webhook health
(meaningless without a receiver), push and branch events.

## 9. Worked examples

### 9a. P1 — a new issue becomes a plan

Tick at 10:00:00, watermark `2026-09-28T09:00:00Z`. Query 4 returns issue #412, `created_at
2026-09-29T09:58:00Z`, `user.type: User`, no `pull_request` key.

→ Candidate (created after the watermark, human-authored, not a PR). Tier **P1**, role `plan`, key
`github:issue:owner/repo:412:created`, prompt = header + title + body. No label is written on the way
in, and none is removed on the way out.

### 9b. P2 — a label becomes a Draft PR

At 14:22 the human applies `idle-ready` to #412. The 14:23 tick's query 2 returns it.

→ Tier **P2**, role `do`, key `github:issue:owner/repo:412:ready`, `timeout_seconds` from the tier
default (D17). The run opens a Draft PR remotely (D27, ADR 0014). On success the terminal-state rule
**removes `idle-ready`**, so the board stops claiming the issue is waiting. On escalation it removes
`idle-ready`, adds `idle-needs-human`, and posts diagnostics (D14) — the debug branch is
`debug/<task_id>` (D15/I5).

### 9c. Escalation, then redefinition

#412 escalated: `idle-needs-human` applied, diagnostics commented, debug branch preserved, task
terminal. Removing `idle-ready` in the same step means the next tick's query 2 will not return it, and
even if it did the key is already spent.

The human reads the diagnostics, fixes the problem statement, and applies `idle-redo`.

→ A **new** task: `derived_from` the escalated one, tier **P1** by default, a **fresh retry budget**
(I9), key `github:issue:owner/repo:412:redo`. The escalated task stays `escalated` forever (I10). No
queue, adapter, or loop-prevention rule behaves differently for this path — it is the same ingestion
with a different key and a lineage pointer.

### 9d. Preemption

At 09:00 a P2 is running. At 09:00:40 the human applies `idle-hotfix` to a P0 issue. The 09:01 tick
observes it within the 60s bound.

→ Tier **P0**, role `do`, enqueued ahead of the running task. D16/I7: the worker `Harness.Abort`s the
P2, `Nack(lease, preempted)`, dequeues the P0, and starts it. `preempted` never crosses the wire
(ADR 0014). The aborting attempt consumes no timeout and is retryable.

## 10. What this contract deliberately does not decide

- **Sweep content and policy** — what a P3 sweep looks for, when it runs, whether it may ever open a
  PR. Only the *trigger* is settled here; the job is a configured placeholder prompt.
- **Cost and budget policy** — ceilings, backoff schedules, what a blown budget means. Untouched by
  this ticket; the retry/taxonomy shape is the harness contract's (ADR 0014).
- **Secrets lifecycle** — rotation, scoping, mid-run expiry. Ownership is settled: idle-deck holds the
  issues token, the remote holds its own repo-scoped token (D27), and the two are never the same
  secret.
- **Live cancellation** — closing an issue does **nothing**. A queued or running task proceeds, and a
  Draft PR may open against a closed issue. This is not minimalism: `TaskState` has nowhere to put
  "dropped", and the ontology states that changing a closed value set is a migration. You can burn a
  3-hour P2 by closing its issue, and the only recourse is to let it finish or restart the daemon.
  Cancellation remains fog.
- **Webhooks** — deferred, not refused. A webhook adapter would be a second `TrackerSource`
  implementation (ADR 0012) with its own idempotency regime, and is the right answer for a hosted
  multi-user idle-deck, which D4 defers.
- **Label colours and install-time label creation** — **closed** on the operator surface (D39: no
  automatic creation; `check` reports what is missing and prints the `gh` commands).
- **A CLI tier override for `idle-redo`** — **closed** on the operator surface (D33: no `enqueue`
  and no tier override; `idle-redo` is the affordance, one click, no second ingestion path).
- **Whether `since` composes with `labels`** — verify in the #10 adapter spec (§8).

## 11. Cross-references

- Domain model: [`docs/ontology.md`](../ontology.md) — entities, invariants I1, I9, I10, I11
- Interface seams: [`docs/architecture/boundaries.md`](boundaries.md) — `TrackerSource` / `TrackerSink`
- Tracker split: [ADR 0012](../adr/0012-tracker-split.md)
- Queue lease and dedupe: [ADR 0011](../adr/0011-queue-lease.md)
- Remote execution service: [`remote-contract.md`](remote-contract.md), [ADR 0014](../adr/0014-remote-execution-service-contract.md)
- This decision: [ADR 0015](../adr/0015-github-event-contract.md) — D28, D29, D30
- Map: [#3](https://github.com/seppaleinen/idle-deck/issues/3) · Operator surface:
  [#11](https://github.com/seppaleinen/idle-deck/issues/11) · Backlog:
  [#10](https://github.com/seppaleinen/idle-deck/issues/10)
