# idle-deck

> **Status: pre-implementation.** No code exists yet. The architecture is being locked down on
> [Wayfinder map #3](https://github.com/seppaleinen/idle-deck/issues/3). This README describes where
> the project is going, not something you can run today.

idle-deck turns synchronous developer workflows into an **event-driven background pipeline**. You
label an issue; idle-deck picks it up, runs an agent against it while the machine is free, and comes
back with a draft pull request. The point is not a chatbot you wait on — it is a queue that keeps
working after you close the laptop lid.

```
  GitHub event ──▶ tracker adapter ──▶ priority queue ──▶ worker daemon ──▶ harness adapter
                                                                        (remote inference)
                          ▲                                                      │
                          └────────── comment / label / draft PR ◀────────────────┘
```

---

## The shape of it

**idle-deck is an orchestration daemon.** It does not host models. It watches an issue tracker,
decides what to run and in what order, hands the work to a remote execution service, and reports the
result back onto the issue.

Five moving parts, each behind a small interface with exactly one real implementation in the MVP:

| Part | MVP implementation | What it does |
|---|---|---|
| **Tracker adapter** | GitHub | Turns tracker events into tasks; writes comments and labels back |
| **Priority queue** | SQLite | Orders work by tier, survives daemon restarts, leases running tasks |
| **Worker daemon** | Go, one worker | Runs the loop: dequeue → dispatch → report → repeat |
| **Harness adapter** | Remote HTTP service | Sends role + prompt + budget; polls a remote execution service that owns the workspace and returns typed artifacts |
| **Idle probe** | Against the inference server | If the model answers, the engine is considered idle |

### Two ideas worth understanding

**idle-deck never names a model.** It passes a *role* — `plan`, `do`, or `sweep` — plus a budget and
a timeout, and the harness decides which model fills that role. Which model is the best planner is the
harness's problem, and it changes monthly. This also means model routing is a configuration concern
rather than a hardcoded table, and it is why issue #1's "Brain vs Worker" model split is gone
([D6](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).

**Idle is a signal, not a vibe.** The project's name suggests watching for a quiet machine. It
doesn't. idle-deck asks the inference server whether it is reachable; if it answers, the engine is
considered idle and work can proceed. No macOS power heuristics, no CPU-threshold guessing, no
"activity monitor" module — and because the models run on a separate machine, idle-deck's own host is
not the bottleneck ([D5](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md),
[D20](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).

### Priority tiers

| Tier | Meaning | Trigger | Result | Default timeout |
|---|---|---|---|---|
| **P0** | Hotfix | TBD — the original handover never says | Draft PR, preempts whatever is running | — |
| **P1** | Spec clarification | New issue created | Checklist comment on the issue | 15 min |
| **P2** | Feature implementation | `status: ready` label | Draft pull request | 3 hr |
| **P3** | Idle sweep | TBD | Yields when higher-priority work arrives | — |

P0 and P3 have no defined trigger yet. That gap is tracked, not papered over — see
[GitHub event contract](https://github.com/seppaleinen/idle-deck/issues/9).

### When a task fails

Two automated retries. A third failure escalates: the workspace is preserved to `debug/<task_id>`,
diagnostics are posted as a comment, `status: needs-human` is applied, and the task leaves the queue.
A killed or preempted run counts as a retryable failure, not a success
([D14](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).

idle-deck **never merges**. It opens a Draft PR and stops. Autonomy is earned by evidence, not assumed
([D22](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).

---

## For users

**There is nothing to install yet.** This section is a statement of intent, and it will be rewritten
once the backlog in [MVP backlog](https://github.com/seppaleinen/idle-deck/issues/10) has been worked.

The intended shape, when it exists:

- **Install a single Go binary.** `go install` or a release artifact. No runtime to set up.
- **Point it at one remote execution service** and one or more GitHub repositories.
- **Single user, single machine.** No accounts, no multi-tenancy, no RBAC. Those are deliberate
  non-goals for the MVP, not oversights ([D4](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).
- **CLI only.** `idle-deck daemon`, `idle-deck queue ls`, `idle-deck status`, and job inspection and
  manual retry. A GUI is post-MVP
  ([D19](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md),
  [#2](https://github.com/seppaleinen/idle-deck/issues/2)).
- **Configuration is env-first**, with an optional YAML file and CLI flags overriding both
  ([D23](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).

A day in the life, once built: you open an issue and idle-deck's P1 tier posts a clarification
checklist back within minutes. You answer it, apply `status: ready`, and go to bed. You wake up to a
Draft PR and a comment explaining what the agent did. You review it, request changes, and merge it
yourself. If it went wrong three times, you get a `status: needs-human` label and a debug branch
instead of a silent failure.

## For developers

**Also nothing to build yet.** What follows is how the work is organised.

### Read these, in order

1. **[`AGENTS.md`](AGENTS.md)** — every decision, with its rationale and a stable id. Read it first,
   every session. The ADR convention and the multi-behavioural decisions' detail live in
   [`docs/adr/README.md`](docs/adr/README.md).
2. **[Map #3](https://github.com/seppaleinen/idle-deck/issues/3)** — the live plan. Destination, notes,
   closed decisions, and fog.
3. **[#1 Architecture Handover & Domain Ontology](https://github.com/seppaleinen/idle-deck/issues/1)**
   — the original design. A strong draft, not scripture. It is being pressure-tested, and several of
   its assumptions do not survive contact with reality (see below).

### How the work is organised

Deciding and building are separate jobs, and this repository is currently in the deciding job
([D2](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)). The map's frontier is the set of
open, unblocked, unclaimed child issues — GitHub renders the blocking graph natively, so the frontier is
visible in the issue view without opening anything. Claim a ticket by assigning it to yourself before
doing any work, and resolve **one** ticket per session.

Tickets are worked through the handover protocol: each resolution returns STATUS, SUMMARY, RATIONALE,
and TRACE. Implementation tickets then run through the dev pipeline
(`dev-team-lead → dev-architect → backend-engineer → test-engineer → code-reviewer`).

The map has **two locks**, in order:

1. **Architecture.** A reviewed ontology in `docs/ontology.md` and a set of decision records.
2. **An execution-ready backlog.** Issues specified well enough that an implementation session starts
   without making a further decision.

The current frontier is three tickets: [Ontology baseline](https://github.com/seppaleinen/idle-deck/issues/5),
[Decision record](https://github.com/seppaleinen/idle-deck/issues/7), and — newly available once the
tracker semantics are confirmed — the next layer of the architecture.

### Stack

Go 1.27 (1.27.1 installed locally), SQLite for the queue, GitHub as the only tracker, one remote HTTP
harness, CLI-first ([D9](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md),
[D13](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).

There is no CI yet, no build, and no test suite. Adding them is a cross-cutting item in the backlog
([Install, run, configure](https://github.com/seppaleinen/idle-deck/issues/11)).

### If you are about to build something

Don't. Not until [MVP backlog](https://github.com/seppaleinen/idle-deck/issues/10) has produced the
issue list, and not until [Architecture boundaries](https://github.com/seppaleinen/idle-deck/issues/6)
has fixed the interface seams. Guessing an interface and then writing three implementations against it
is the specific failure mode this map exists to prevent.

### Known problems in the original handover

If you read issue #1, you will notice things this effort is deliberately not carrying forward. They
are tracked, not quietly dropped:

- The named models **`Qwen3.8-27b`** and `Qwen2.5-Coder` assume locally-served inference with VRAM
  budgets. Models run on a separate server here, and the tier→model mapping is gone
  ([D6](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md),
  [D20](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).
- The project is called **`harness-plane`** in issue #1. It is idle-deck
  ([D1](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).
- The ontology models **no idle signal**, despite "idle compute cycles" being the whole pitch.
- The `Base*` interface prefix is not idiomatic Go ([D18](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).
- `timeout_seconds` on `TaskItem` and hardcoded per-tier timeouts are never reconciled
  ([D17](https://github.com/seppaleinen/idle-deck/blob/main/AGENTS.md)).
- Nothing specifies secrets, cost ceilings, human review, or what "idle" actually means operationally.
- **The most dangerous omission:** `dequeue()` does not say whether a running task is held by a lease
  that can expire or destroyed outright. A daemon crash either recovers the task or loses it, and the
  entire fault-tolerance story depends on which
  ([#6](https://github.com/seppaleinen/idle-deck/issues/6)).

---

## Contributing

Nothing is merged yet, and nothing is being built until the map's first lock lands. If you want to
help, the useful contributions right now are decisions, not code: claim an open frontier ticket on
[#3](https://github.com/seppaleinen/idle-deck/issues/3) and resolve it.

## License

MIT. See [`LICENSE`](LICENSE).
