---
Title:   A P3 sweep files one issue per finding, never a PR, and never re-enters its own write
Status:  accepted
Supersedes: —
Related: D43, D44, D45, D8, D22, D28, D29, D30, D34, D40, I1, I10, 0011, 0014, 0015
Source:  #13 What does a P3 idle sweep do?
---

## Decision

The trigger and the plumbing existed; the job did not. Five questions, and they resolve into one
shape: **a sweep is a reporter, not an author.** It looks, it files, and it stops. Everything it
finds becomes an ordinary tracker issue on the ordinary ingestion path.

**D43 — a sweep's finding becomes an issue, and a sweep may never open a pull request.** A P3 run
returns a `report` artifact; the report's content is then filed as **one new issue per finding** via
`TrackerSink`. A sweep **may not** produce a `pull_request` artifact, and a `sweep`-role run that
returns one is a **non-retryable protocol violation**, not a run to escalate — the remote broke a
stated contract and re-running it would break it again. A `sweep` run may likewise not produce a
`feature` branch: D15 names feature branches `feature/<ticket_id>-<slug>`, and a sweep has no
`ticket_id` to put there, so the branch name cannot be conformant.

The route to a Draft PR therefore runs **through a human**: the sweep files an issue, the issue is an
ordinary P1 candidate, and a human applies `idle-ready` (D29) to promote it. A sweep is a way of
*finding* work, never of *doing* it. D22 already means idle-deck never merges, so a Draft PR opened
by a sweep would be a branch nobody is on the hook to close out.

**D44 — a sweep's `Task` carries no `TrackerRef`, and the ontology says so out loud.** The ontology
required `Task.ticket` on every task, which a sweep cannot supply: the task exists when the scheduler
ticks, before any run, before any issue. `ticket` is now **required for every tier except P3**, and
the exception is named rather than inferred. The reason is worth stating because it is the whole
point: **a sweep is not yet about any work item.** The issue it files is a *different* task's ticket,
not this one's. This is the only tier for which that is true, which is why it is the only exception.

**D45 — a sweep may not escalate, and idle-deck never re-ingests its own writes.** A sweep finding
does **not** become a P1 by itself. The general invariant is **loop-freedom: idle-deck never re-ingests
an artifact it produced.** It is already true for labels — idle-deck's only label write is
`idle-needs-human`, which is not a trigger, so writing it does not re-fire the daemon. A sweep's
issue is the same shape: it carries `idle-needs-human`, and an issue carrying that label is a report
*to* a human, not a request *from* one. So the existing rule is reused rather than a second mechanism
invented — and the label is honest, because "this needs a human" is precisely what a sweep finding
is.

The hazard this closes is real and was not hypothetical. P1's only guard against self-feeding is
`user.type == "Bot"` (event contract §P1), and D4 makes a **human** account the likely case: a
sweep-filed issue from a human PAT would otherwise become an unattended P1 at 5,000 tokens a pop, with
the sweep reading back its own write. Expressing the guard as loop-freedom rather than as bot-detection
is what makes it survive the account being a human.

**Cadence, and the lens — configuration, not policy.** The bucket period is an **operator-owned
config value** (`IDLE_DECK_SWEEP_PERIOD`, default `168h`), not a built-in schedule, and `0` is a global
kill switch. What a sweep looks for is a **configured prompt** with one narrow shipped default:
**stale TODO/FIXME markers, with age and last-touched context**. Each candidate policy the ticket
listed — dependency drift, flakiness, coverage, open README questions — is a *different job*, and
"find problems in this repo" is not a job at all. A finding keeps idle-deck out of the business of
holding opinions about what code health means, across every repository it watches, forever. And the
default is deliberately a *finding* rather than a *judgment*, because an unattended 20,000-token run
whose output cannot be checked against the repository is not auditable.

**No adaptive backoff.** A sweep that finds nothing every night gets muted, and that fear is real —
but it is answered by a config value the operator owns, not by machinery. Adaptive backoff would make
the dedupe key's period *change* over time, which is a subtle thing to depend on, and its behaviour
would be unobservable from outside. A weekly default plus an operator-set period is one line of config
and the operator owns the consequence.

**Opt-out is a narrowing subset, never a second allowlist.** `IDLE_DECK_SWEEP_REPOS` defaults to
every allowlisted repository. The allowlist still governs *access*; the sweep set governs only *which
allowed repositories also get P3*. D30's point was that the allowlist is a trust boundary, and a trust
boundary should stay boring — so no per-repository period syntax is bolted onto it.

## The report had no read path

The locked documents already contained this, unremarked: the sweep example returned
`{"kind": "report", "uri": "artifact-store/run-sweep/report.md"}` — a URI, with no inline body.
`Harness` is `Start`/`Abort`/`Result` (D26, no fetch), the workspace is on the remote (D27), and
`status` reports heartbeat, watermarks, queue depth, and the active task — never artifacts. So a
report-only sweep spent 20,000 tokens writing a file that idle-deck could neither retrieve nor show.

**This is why D43 is "file an issue" and not "return a report."** The destination was the real
question; "what may it do about a finding" was downstream of it. A sweep whose only output is
unreadable is a sweep that finds the same nothing every bucket, which is the mute risk the ticket
named, arrived at structurally rather than by taste.

## Why each part is settled the way it is

| Question | Answer | Load-bearing reason |
|---|---|---|
| What does it look for? | Configured prompt, narrow default (stale TODO/FIXME) | Keeps idle-deck out of per-repo code-health opinion |
| When does it run? | `IDLE_DECK_SWEEP_PERIOD`, default `168h`; `0` disables | Operator owns the mute risk; no unobservable machinery |
| What does it do with a finding? | One issue per finding, labelled `idle-needs-human` | The only place the human already looks |
| May it open a Draft PR? | **No** — non-retryable protocol violation | D15 has no `ticket_id` for `feature/<ticket_id>-<slug>` |
| May it escalate to P1? | **No** — idle-deck never re-ingests its own writes | `user.type == "Bot"` is not a guard when the PAT is human (D4) |

## The part that was missed until it was asked

Nothing in the ticket's five questions asked **where a sweep's output goes**, and the answer changed
the others. "What may a sweep do about a finding" is unanswerable until "does the human ever see it" is
answered, and the locked documents said *no* — silently, because a missing read path is not an error
any check reports. The same shape as the `report` artifact being unreadable: a gap that passes
verification because nothing asserts against it.

There is also a **stale worked example**, now corrected: §8c showed a sweep run carrying
`"ticket": {"external_id": "101"}`. A P3 sweep comes from a local scheduler with no GitHub query, so
issue 101 cannot exist. The example predates D28's trigger decision and contradicted it.

## What is deliberately still open

- **What the operator configures a sweep to look for.** The mechanism is decided; the content is the
  operator's, by construction. There is no built-in policy catalogue, and adding one would be a new
  decision.
- **Cancellation.** Unchanged and still undecided. A sweep is not cancelled by anything, because
  `TaskState` has nowhere to put "dropped" — a sweep runs to its timeout or to preemption.
- **A sweep that finds something in a repository with no allowlist entry** is impossible by
  construction (D30, I1): the scheduler iterates the allowlist, so there is no repository to sweep
  that is not already watched.

## Consequences

- `docs/ontology.md` — `Task.ticket` is conditionally required; P3 is the named exception. The
  `schedule.sweep` entry in `TriggeredBy.event_type` was already there, so the model anticipated
  sweeps and then made the one field they cannot supply mandatory; this decision resolves that.
- `docs/architecture/event-contract.md` — P1's issue-creation query skips issues carrying
  `idle-needs-human`, stated as loop-freedom rather than as bot-detection.
- `docs/architecture/remote-contract.md` — §8c's example loses its fabricated ticket, and a
  `pull_request` artifact on a `sweep` run is a non-retryable protocol violation.
- `docs/operations/operator-surface.md` — `IDLE_DECK_SWEEP_PERIOD` and `IDLE_DECK_SWEEP_REPOS` are
  required-if-sweeping, with the default prompt recorded.
- #10 can slice P3. The slice is small: a scheduler tick, a config pair, a report-to-issue path, and
  the two structural refusals.
