# idle-deck domain ontology

The canonical domain model. This document answers *what exists in the domain and what must be true
about it*. It deliberately answers nothing else — see [What this document is not](#what-this-document-is-not).

**Status:** locked by [Ontology baseline](https://github.com/seppaleinen/idle-deck/issues/5), resolved
against [#1 Architecture Handover & Domain Ontology](https://github.com/seppaleinen/idle-deck/issues/1),
which is a revisable draft, not binding ([D10](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).

---

## The one-paragraph version

idle-deck receives **events** from an issue tracker, turns them into **tasks** attached to an
allowlisted **repository**, orders them in a priority queue, and runs each one as a sequence of
**attempts**. Each attempt is executed by an adapter against a remote engine that idle-deck never
hosts, and leaves behind **artifacts** — a pull request, a branch, a comment, a report. An attempt
that fails may be retried; when the retry budget runs out the task is **escalated** to a human, and
that escalation is terminal. A human can respond by defining a **new task** that cites the old one
and inherits its debug branch — but never by reviving the escalated task.

---

## Entities

Six. Five are durable; `Artifact` is the sixth and is the record of what a run *left behind*.

### `Repository`

A source-code repository idle-deck is permitted to act on.

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | identifier | yes | Stable within the daemon. |
| `url` | string | yes | Canonical clone/identity URL. |
| `name` | string | yes | `owner/repo`. |
| `tracker` | enum | yes | Which tracker hosts it. MVP: `github`. |
| `allowlisted` | bool | yes | Trust flag. Invariant **I1**. |

**Why it is an entity.** Once events are accepted from many repositories (D12), "which repositories
may this daemon touch" becomes a domain question with a domain answer. Without this entity the
allowlist is string-matching against a field, and the trust boundary is a convention rather than a
rule.

### `Task`

The durable, **immutable** statement of intent: *do this work, for this repository, at this priority.*

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | identifier | yes | Stable. Appears in branch names (D15). |
| `repository_id` | → `Repository` | yes | Never a URL string. |
| `ticket` | `TrackerRef` | yes | Value object, see below. |
| `tier` | `TaskTier` | yes | Authoritative priority. |
| `prompt` | string | yes | What the agent is asked to do. |
| `payload` | object | no | Free-form tier-specific data (checklist state, etc). |
| `timeout_seconds` | int | no | Overrides tier default when set (D17). |
| `budget` | int | no | Ceiling the harness respects. **The field, not the policy.** |
| `state` | `TaskState` | yes | Lifecycle, see [State machine](#state-machine). |
| `triggered_by` | `TriggeredBy` | yes | Value object, see below. |
| `derived_from` | → `Task` | no | Lineage. Nullable. See [Redefinition](#redefinition). |
| `created_at` | timestamp | yes | |

**`Task` is immutable except for `state`.** Its identity *is* its problem statement. This is what
makes I10 coherent and what forbids revival in place.

**`priority_score` is not a field.** Tier is the only priority (D8, D16). If a derived sort key is
needed by a queue implementation, it is computed, not stored, and never authoritative.

### `TaskAttempt`

One execution of a `Task`. A task has **1..N** attempts. This is the entity that makes run history
recoverable — the thing #1's flat `TaskItem` could not express.

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | identifier | yes | |
| `task_id` | → `Task` | yes | |
| `ordinal` | int | yes | 1-based execution order. |
| `outcome` | `AttemptOutcome` | yes | Terminal once set. |
| `started_at` | timestamp | no | Null until dispatched. |
| `finished_at` | timestamp | no | |
| `timeout_seconds` | int | no | Resolved value actually applied. |
| `role` | `TaskRole` | **derived** | From `task.tier`. Never stored. |
| `remote_session_id` | string | no | Handle for abort. See **I8**. |
| `artifacts` | 0..N `Artifact` | — | |

**Retry count is not a field.** It is `len(attempts)`. A counter column is lossy by construction and
throws away the exact information the escalation path exists to surface (D14).

### `Artifact`

Something an attempt produced that outlives the attempt.

| Field | Type | Required | Notes |
|---|---|---|---|
| `id` | identifier | yes | |
| `attempt_id` | → `TaskAttempt` | yes | |
| `kind` | `ArtifactKind` | yes | Discriminated. |
| `branch_purpose` | `BranchPurpose` | no | **Only meaningful when `kind = branch`.** |
| `uri` | string | yes | Where it lives. |
| `produced_at` | timestamp | yes | |

**Why `branch_purpose` is an attribute and not a kind.** `debug/<task_id>` (D15) and
`feature/<ticket_id>-<slug>` are the same *kind* of thing with different intent. Splitting them into
two kinds makes I4/I5 read as "has an artifact of kind `debug_branch`", which is awkward phrasing
and would force a new kind the first time a third branch purpose appears.

### `TriggeredBy`

Value object. Provenance.

| Field | Type | Required | Notes |
|---|---|---|---|
| `event_type` | string | yes | Tracker-agnostic. Closed vocabulary per tracker: GitHub uses `issue.opened`, `issue.labeled:<label>`, `schedule.sweep`. |
| `dedupe_key` | string | yes | A **stable resource key**, anchoring ingestion idempotency. |
| `received_at` | timestamp | yes | When the daemon observed the condition. |

**Why not a first-class `Event` entity.** A single-user MVP has no inbox to drain, and the
deny-by-default allowlist already bounds the blast radius. A value object is nearly free and makes
ingestion idempotency answerable from stored data. The event contract confirmed the instinct from the
other side: idle-deck polls, so it observes *state* rather than *events* — its per-repository
watermark is adapter state, deliberately not a domain entity, because it records what was **skipped**,
and `TriggeredBy` records what caused a task that **exists**.

### `TrackerRef`

Value object. A tracker's own identity for a work item.

| Field | Type | Required | Notes |
|---|---|---|---|
| `tracker` | enum | yes | |
| `repository_id` | → `Repository` | yes | |
| `external_id` | string | yes | Tracker's own id (a GitHub issue number). |
| `url` | string | yes | |

**Why not a bare `ticket_id: string`.** An unqualified id is ambiguous across trackers and repos. Once
`Repository` exists, the reference should be unambiguous too.

---

## Value sets

Closed sets. These become enums; changing one is a migration.

| Set | Values |
|---|---|
| `TaskTier` | `P0` `P1` `P2` `P3` |
| `TaskRole` | `plan` `do` `sweep` |
| `TaskState` | `queued` `running` `succeeded` `escalated` |
| `AttemptOutcome` | `succeeded` `retryable_failure` `non_retryable_failure` `timeout` `preempted` |
| `ArtifactKind` | `comment` `pull_request` `branch` `report` `log` |
| `BranchPurpose` | `feature` `debug` |

### Notes on the sets

**`role` is derived from `tier`, never stored.**

| Tier | Role | Meaning |
|---|---|---|
| `P0` | `do` | Hotfix. |
| `P1` | `plan` | Spec clarification. |
| `P2` | `do` | Feature implementation. |
| `P3` | `sweep` | Idle sweep. |

Storing it would make a `P2` task with role `plan` representable — a legal-but-meaningless state that
someone will eventually create. D6 already made the harness own model choice; owning the tier→role
mapping is the same responsibility split one level up.

**`aborted` is deliberately absent.** It was reserved for human cancellation, which is not in the
MVP (fog item 3: *task cancellation and live user intervention*). An unreachable enum member is a
state nobody has thought about. Adding it later is free. `preempted` and `timeout` are both
reachable today and both required to make D16 and D17 expressible.

**There is no `failed` task state.** I6 makes escalation the only terminal failure. A task either
succeeded or needs a human.

---

## State machine

Task state and attempt outcome are **two different questions** and are modelled separately:

- *Did this attempt fail?* — many kinds (`AttemptOutcome`).
- *Is the task finished?* — few states (`TaskState`).

Collapsing them is precisely what forces the lossy `retry_count`-as-truth design of #1.

```
Task:      queued ──▶ running ──┬──▶ succeeded        (terminal, success)
                             │
                             └──▶ escalated         (terminal, failure — never revived)

Attempt:   (started) ──▶ succeeded
                    ├──▶ retryable_failure      ─┐
                    ├──▶ non_retryable_failure  ─┤ new attempt, if budget remains
                    ├──▶ timeout                ─┘ (D17: timeout is retryable)
                    └──▶ preempted              ─┘ (D16: P0 evicted the run)
```

A task in `running` always has exactly one attempt in a non-terminal state.

---

## Redefinition

**Escalation is terminal. Recovery happens by authoring a new task.**

When a human responds to an escalation, they do not revive the escalated `Task` — its `prompt` is
part of its identity, and changing it in place would make the id refer to two different problems over
time, with attempt history silently spanning both. Instead:

1. The escalated task stays `escalated`, forever.
2. A **new** `Task` is created with `derived_from` pointing at it.
3. The new task's `tier` is **explicitly chosen**; the prescribed default for a redefinition is
   `P1`, because a redefinition is by definition a changed problem statement and deserves a
   re-clarification round. The human may override this — if they have already clarified it, forcing
   P1 is friction.
4. The new task **may read and branch from the old task's debug branch.** This is what keeps the
   escalation path worth pushing: the redefinition can cite what broke.
5. The new task gets a **fresh retry budget** (I9).

---

## Invariants

Each names its enforcer. A stated rule nobody checks is a wish; naming the enforcer turns this
document into acceptance criteria for the MVP backlog.

| # | Invariant | Enforcer |
|---|---|---|
| **I1** | A task may only exist for a `Repository` with `allowlisted = true`. | Tracker adapter, at event ingestion |
| **I2** | A task in `succeeded` or `escalated` has ≥ 1 attempt. | Queue / worker |
| **I3** | An attempt is either not-yet-started or terminal; there is no other state. | State machine, not persistence |
| **I4** | A `succeeded` attempt on a `P2` task has a `pull_request` artifact. | Harness adapter, on return |
| **I5** | An `escalated` task has a `branch` artifact with `branch_purpose = debug`. | Worker, at escalation |
| **I6** | Exhausted retry budget ⟹ `escalated`, never a terminal `failed` state. | Retry policy |
| **I7** | A `preempted` attempt consumes no timeout and is retryable. | Preemption path |
| **I8** | A running attempt is abortable via its `remote_session_id`. | Harness adapter contract — see the remote execution service contract |
| **I9** | Retry budget is per-lineage-root; redefinition never inherits consumed retries. | Task creation |
| **I10** | An `escalated` task never returns to `queued`. Recovery is a new task with `derived_from`. | State machine |
| **I11** | `role` is always derivable from `tier`; it is never stored, so it cannot disagree. | Construction / no persistence of the field |

**On I9.** The tempting alternative was for a lineage chain to share one retry budget, hard-bounding
total work. That bounds the loop by punishing the human for engaging, which is backwards. The loop is
already bounded: retries are finite *per task*, and each redefinition is a human decision, not an
automatic re-enqueue. Nothing here can loop without a human in it.

---

## What this document is not

Stated explicitly so the next person does not helpfully add it back.

1. **Retry counts and tier timeouts.** D14's "2 retries", D17's "15 min / 3 hr". These are policy
   constants. The ontology references a *retry budget* and a *timeout* as concepts; their values live
   in decision records.
2. **Cost ceilings and `429` behaviour.** `budget` is modelled as a field. What happens when it is
   exhausted, throttled, or refused is policy — and the harness contract does not exist yet.
3. **Worker concurrency.** `max_concurrent_jobs: 1` is implementation. What a *worker* is remains
   undecided.
4. **The lease mechanism.** How dequeue-while-running is implemented belongs to the architecture
   boundaries ticket. The *obligation* — an attempt in `running` is abortable and holds a session
   handle — is ontology, and is I8.
5. **Storage.** No tables, no SQL types, no indexes, no migrations. This model's projection onto
   SQLite is deferred to the backlog.
6. **Language and interfaces.** Go, the adapters, method signatures, the `Base*` naming question
   (D18). All belong to the architecture boundaries ticket.
7. **GitHub specifics.** Webhook delivery, signature verification, concrete label strings. The
   ontology says "tracker event", not "webhook" — and idle-deck does not use webhooks at all. Decided
   in [`docs/architecture/event-contract.md`](architecture/event-contract.md)
   ([D28](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md),
   [D29](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).
8. **Merge policy.** D22 says idle-deck never merges. The ontology has no opinion on what happens
   after a pull request exists, because there is no pull-request entity — only an artifact pointing
   at one.

---

## Scope discipline

This ontology is a **domain superset**, not an MVP-shaped sketch. But "superset" is only safe with a
rule, otherwise it becomes fiction:

> A concept belongs here if it is a *thing that exists or will exist in the system*, not merely a
> thing the MVP has not built yet.

- **In, despite the MVP not building it:** `P3` sweeps. A P3 task is a real task, and the event
  contract gave it a trigger (a local scheduler). What a sweep *looks for* is policy, not ontology.
- **Out, despite seeming relevant:** cost *policy* (no behaviour exists to model), multi-worker
  concurrency (what a worker is has not been decided).

---

## Mapping from issue #1

What was kept, reworded, and dropped — and why.

| #1 content | Disposition | Reason |
|---|---|---|
| `TaskItem` as one flat entity | **Split** into `Task` + `TaskAttempt` | Attempt history was unrecoverable, which is what the escalation path exists to surface (D14). |
| `retry_count (0-2)` | **Dropped** as a field | Lossy by construction. Derived as `len(attempts)`. |
| `repository_url` (string field) | **Replaced** by `Repository` entity | Trust domain must be an invariant, not string-matching (D12, I1). |
| `ticket_id` (string field) | **Replaced** by `TrackerRef` value object | A bare id is ambiguous across trackers and repos. |
| `priority_tier` | **Kept** as `TaskTier` | |
| `priority_score` | **Kept as a concept, dropped as a field** | No stated relationship to tier. Derived, non-authoritative. |
| `task_type (SPEC_CLARIFICATION \| FEATURE_IMPLEMENTATION \| SWEEP)` | **Dropped** | Redundant with `tier`. A `P2` *is* feature implementation. Two axes for one distinction invites disagreement. |
| `payload` | **Kept** | Free-form tier-specific data is genuinely open-ended. |
| `timeout_seconds` | **Kept** on both task and attempt | Task holds the requested value; attempt holds the resolved one actually applied (D17). |
| `created_at` | **Kept** | |
| P0–P3 tier policy table | **Partly kept** | The *semantics* of each tier are kept. The hardcoded timeouts and the Qwen model assignments are policy/implementation and were dropped. |
| "Brain vs Worker" model strategy | **Dropped** | D6. idle-deck never names a model. Replaced by derived `TaskRole`. |
| Model names `Qwen3.8-27b` / `Qwen2.5-Coder` | **Dropped** | Assumed locally-served inference with VRAM budgets. Models run on a separate server (D20). `Qwen3.8-27b` does not appear to be a real model. |
| `BaseTracker` | **Dropped as a name** | Not idiomatic Go (D18). The responsibilities are kept; the interfaces belong to the boundaries ticket. |
| `BaseQueue` | **Dropped as a name** | As above. |
| `BaseHarness` | **Dropped as a name** | As above. Its methods bundle remote dispatch with *local* git operations — `prepare_workspace` and `preserve_debug_state` imply filesystem access, but D20/D7 put execution on a remote server. Two incompatible architectures share one interface name. Unresolved. |
| `dequeue()` semantics | **Dropped as unspecified** | Never says whether a running task holds a lease that expires or is destroyed. A daemon crash either recovers the task or loses it. The highest-consequence gap in #1. |
| `nack(task_id, reason)` | **Sharpened** | Became `AttemptOutcome`, which distinguishes retryable from non-retryable (D14). |
| Max 2 retries then escalate | **Kept as policy, not ontology** | Per [What this is not](#what-this-document-is-not) #1. |
| Debug branch `draft/debug-<task_id>` | **Normalized** to `branch` + `branch_purpose = debug`, named `debug/<task_id>` | D15. The `draft/` prefix was a PR concept leaking into a branch name. |
| P3 "aborts gracefully if higher-priority tasks enter the queue" | **Kept** as `preempted` | Now an outcome type rather than prose (D16, I7). |
| "Successful runs push a clean feature branch and open a Draft PR" | **Kept** as artifacts | I4. |
| "Failed or timed-out runs preserve state to a debug branch" | **Kept** as artifacts | I5. |
| *(absent from #1)* | **Added:** idle | The project pitches "idle compute cycles" but #1 models no idle signal. Idle is **not** an entity — it has no lifecycle, nothing references it, and no query answers "tell me about the idle". It is a named responsibility: an `IdlePolicy` the worker depends on, satisfied by probing the inference server (D5). The interface itself belongs to the boundaries ticket. |
| *(absent from #1)* | **Added:** task lineage | Escalation needed somewhere to go. `derived_from` gives it somewhere. |
| *(absent from #1)* | **Added:** provenance | Without `TriggeredBy`, "why did this task exist" is unanswerable from idle-deck's own data. |
| *(absent from #1)* | **Added:** artifact record | #1 had no way to represent the three different output shapes a run can produce. |

---

## Open questions this document does not answer

Recorded so they are visible rather than assumed away.

- **Every tier has a trigger — resolved.** P0 and P3 used to exist in the model with nothing producing
  them. [GitHub event contract #9](https://github.com/seppaleinen/idle-deck/issues/9) settled the
  triggers: `idle-hotfix` (P0), issue creation (P1), `idle-ready` (P2), a local scheduler (P3), and
  `idle-redo` for a redefinition (D28/D29, [ADR
  0015](architecture/event-contract.md)). The `Event` entity stayed unnecessary — polling observes
  state, so a per-repository watermark is adapter state, not domain.
- **The workspace-ownership contradiction is resolved.** The #1 mapping flagged that
  `remote_session_id` may need a different home if the workspace turned out to be local. [Remote
  execution service contract #8](https://github.com/seppaleinen/idle-deck/issues/8) settled it: **the
  workspace lives on the remote** (D27, [ADR 0014](adr/0014-remote-execution-service-contract.md));
  `remote_session_id` stays as-is and equals the remote's `run_id`.
- **Sweep policy.** What a sweep looks for, and whether it may ever open a PR, is undecided. The
  trigger exists; the job does not.
- **Cancellation.** `aborted` is reserved. The reserved outcome is named; the feature is not designed.
  Consequently **closing a GitHub issue does not cancel its task** — `TaskState` has nowhere to put
  "dropped", and changing a closed value set is a migration (see the event contract).

---

## Related

- [`AGENTS.md`](../AGENTS.md) — recorded decisions, `D1`–`D29`.
- [Wayfinder map #3](https://github.com/seppaleinen/idle-deck/issues/3) — the live plan.
- [Architecture boundaries](https://github.com/seppaleinen/idle-deck/issues/6) — the four interfaces.
- [Remote execution service contract](https://github.com/seppaleinen/idle-deck/issues/8) — I8's enforcer.
- [GitHub event contract](architecture/event-contract.md) — triggers, tier derivation, label vocabulary.
