# AGENTS.md

Decisions, recorded. Read this before touching idle-deck.

**Project status: nothing is built yet.** This file and the README are the only artifacts. The
architecture is being locked down on [Wayfinder map #3](https://github.com/seppaleinen/idle-deck/issues/3).
Do not start implementing until the map's first lock lands.

## How decisions are recorded (provisional)

Every decision gets a stable `D<n>` id so issues, PRs, and commit messages can point at it without
prose duplication. A decision lives in exactly one place — this table — and is referenced elsewhere by
id. Decisions are additive; a changed decision gets a **new** id marked *supersedes*, and the old one
is marked *superseded* with a pointer. Nothing is silently rewritten.

> This scheme is provisional. [Decision record #7](https://github.com/seppaleinen/idle-deck/issues/7)
> decides whether `AGENTS.md` is the permanent home or whether these graduate into `docs/adr/`. Until
> then, record here.

## Decisions

### Project identity

| id | Decision | Rationale |
|---|---|---|
| **D1** | The project is **idle-deck**. The former name `harness-plane` is deprecated. | Issue #1 still carries the old name. One name, everywhere. |

### Shape of the MVP

| id | Decision | Rationale |
|---|---|---|
| **D2** | This effort ends at **documents and issues, not code**. | Deciding and building are different jobs. A working-but-unexamined system is worse than a clear spec. |
| **D3** | The MVP backlog is the map's **second lock**, not a byproduct. Each issue must be startable without a further decision. | The point of the map is a backlog someone else can execute. |
| **D4** | MVP audience is **single-user on one machine**. Open-source installability is a stated *future* goal, not an MVP goal. | Auth, multi-tenancy, and RBAC would consume the whole MVP. |
| **D8** | `max_concurrent_jobs: 1` for the MVP. | Single user, one workstation. The boundary must not foreclose N. |
| **D22** | **Never merges.** The MVP opens a Draft PR and stops. Merge policy is future work. | Autonomy is earned by evidence, not assumed. |
| **D19** | **CLI only** for the MVP. [#2](https://github.com/seppaleinen/idle-deck/issues/2) (GUI) is post-MVP and its requirements stay deliberately undecided. | Don't build a UI against an unsettled ontology. |

### Models and idle

| id | Decision | Rationale |
|---|---|---|
| **D5** | **Idle means: idle-deck can reach the model.** It probes the inference server; if it answers, the engine is considered idle. No platform idle heuristics. | Replaces hand-waving with a testable signal. Injectable, not hardcoded to macOS. |
| **D6** | **idle-deck never names a model.** It passes a *role* (`plan` / `do` / `sweep`) plus a budget to the harness. The harness decides the model. | Kills the Brain-vs-Worker box from issue #1. Model choice is an adapter concern. |
| **D7** | MVP ships **exactly one** harness adapter, talking to a remote execution service over JSON/HTTP. | Deepest cut available. Whether "framework-agnostic" is honest with one adapter is ticket [#12](https://github.com/seppaleinen/idle-deck/issues/12). |
| **D20** | Models run on a **server separate from the workstation**. idle-deck orchestrates; it does not host inference. | Settles the VRAM question from issue #1 — this hardware has no NVIDIA GPU. |

### Adapters and the event pipeline

| id | Decision | Rationale |
|---|---|---|
| **D13** | **Minimal interfaces, one concrete implementation each**: GitHub tracker, SQLite queue, one remote harness. | Prevents premature generality while keeping the seams real. |
| **D12** | **Multi-repo, event-driven.** The repository comes from the webhook's own origin. Deny-by-default allowlist. | The repository field is derived, not configured per task. The allowlist is a safety rail, not a feature. |
| **D14** | **Two automated retries**, then escalate: preserve the debug branch, comment diagnostics, apply `status: needs-human`, drop from queue. | From issue #1, stated unambiguously. Non-retryable failures escalate without burning retries. |
| **D17** | `TaskItem.timeout_seconds` **overrides** the tier default when set. A killed or preempted run counts as a retryable failure. | Explicit beats implicit. Timeouts are load-bearing. |
| **D16** | With one worker, a **P0 aborts the running P2** and takes the slot. Aborted runs follow normal retry/escalation. | Makes "immediate preemption" real at `max_concurrent_jobs: 1`. |

### Naming and lifecycle conventions

| id | Decision | Rationale |
|---|---|---|
| **D15** | Debug branches are `debug/<task_id>`. Feature branches are `feature/<ticket_id>-<slug>`. | Debug is short and unique; feature branches carry the ticket for traceability. |
| **D21** | Successful runs push a feature branch, open a Draft PR, and **clean up the workspace**. Failed and timed-out runs preserve state to a debug branch. | Keeps the retry path debuggable without hoarding branches forever. |

### Language and interfaces

| id | Decision | Rationale |
|---|---|---|
| **D9** | **Go 1.27** (installed locally: `go1.27.1 darwin/arm64`). | A long-running daemon with a worker pool and subprocess supervision is exactly Go's strength; a single static binary suits a personal daemon. |
| **D18** | Go interface naming follows Go convention (`Harness`, not `BaseHarness`) — **pending** confirmation in [Architecture boundaries #6](https://github.com/seppaleinen/idle-deck/issues/6). | Issue #1's `Base*` prefix is not idiomatic. |

### Configuration

| id | Decision | Rationale |
|---|---|---|
| **D23** | **Env-first configuration**: env vars, optional YAML file, CLI flags override. Tokens are never logged and never committed. | Twelve-factor, easy to run locally. Concrete var names are pinned in [#11](https://github.com/seppaleinen/idle-deck/issues/11). |

## Open, and deliberately so

- **The ontology is not locked.** [#5](https://github.com/seppaleinen/idle-deck/issues/5) writes
  `docs/ontology.md` and pressure-tests issue #1. Issue #1's ontology is a strong draft, not gospel:
  it models no idle signal, no repo trust domain, no task result, no attempt history, and no failure
  classification.
- **The remote contract is not written.** [#8](https://github.com/seppaleinen/idle-deck/issues/8).
  Notably unresolved: **does the workspace live on the remote side or locally?** Two incompatible
  architectures currently share one interface name.
- **Queue lease semantics are the highest-consequence unknown.** Dequeue-while-running must either be a
  lease that expires (a daemon crash cannot strand a task) or a destructive pop that loses it. The whole
  fault-tolerance story depends on the answer. [#6](https://github.com/seppaleinen/idle-deck/issues/6).

## Related

- [`README.md`](README.md) — what idle-deck is, for users and developers.
- [Map #3](https://github.com/seppaleinen/idle-deck/issues/3) — the live plan.
- [#1 Architecture Handover & Domain Ontology](https://github.com/seppaleinen/idle-deck/issues/1) —
  the draft this effort pressure-tests.
