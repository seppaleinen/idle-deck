# AGENTS.md

Decisions, recorded. Read this before touching idle-deck.

**Project status: nothing is built yet.** This file and the README are the only artifacts. The
architecture is being locked down on [Wayfinder map #3](https://github.com/seppaleinen/idle-deck/issues/3).
Do not start implementing until the map's first lock lands.

## How decisions are recorded

Every decision gets a stable `D<n>` id so issues, PRs, and commit messages can point at it without
prose duplication. Decisions live in **one of two tiers**:

- **ADR tier** — decisions that are expensive to reverse (changing them would change how two or more
  other decisions behave). Detail lives in `docs/adr/NNNN-title.md`; `AGENTS.md` keeps the index row.
- **Table tier** — scoping/policy decisions. The row here is the whole record, with rationale in the
  same row.

The full convention — tiers, file format, immutability, statuses, superseding, and the D-id
verification check — is in [`docs/adr/README.md`](docs/adr/README.md). Decisions are additive; a
changed decision gets a **new** id marked *supersedes*, and the old one is marked *superseded* with a
pointer. Nothing is silently rewritten. The only mutable field on an ADR is its `Status:` header.

> Convention locked by [Decision record #7](https://github.com/seppaleinen/idle-deck/issues/7).
> Requires: implementers cite their D-refs on issues; reviewers check them. The D-id verification
> check must pass before any push touching `AGENTS.md` or `docs/adr/`.

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

### Domain model

| id | Decision | Rationale |
|---|---|---|
| **D10** | Issue #1's ontology is a **revisable draft, not binding**. The domain model is re-derived, not inherited. | [ADR 0002](docs/adr/0001-ontology-revisable.md) |
| **D11** | The ontology is a **standalone document** ([`docs/ontology.md`](docs/ontology.md)), separated from policy and from implementation choice. | [ADR 0002](docs/adr/0002-ontology-standalone-document.md) |

### Models and idle

| id | Decision | Rationale |
|---|---|---|
| **D5** | **Idle means: idle-deck can reach the model.** It probes the inference server; if it answers, the engine is considered idle. No platform idle heuristics. | [ADR 0003](docs/adr/0003-idle-is-a-probe.md) |
| **D6** | **idle-deck never names a model.** It passes a *role* (`plan` / `do` / `sweep`) plus a budget to the harness. The harness decides the model. | [ADR 0004](docs/adr/0004-never-name-a-model.md) |
| **D7** | MVP ships **exactly one** harness adapter, talking to a remote execution service over JSON/HTTP. | [ADR 0005](docs/adr/0005-one-harness-adapter.md) |
| **D20** | Models run on a **server separate from the workstation**. idle-deck orchestrates; it does not host inference. | [ADR 0006](docs/adr/0006-models-on-a-separate-server.md) |

### Adapters and the event pipeline

| id | Decision | Rationale |
|---|---|---|
| **D13** | **Minimal interfaces, one concrete implementation each**: GitHub tracker, SQLite queue, one remote harness. | [ADR 0008](docs/adr/0008-minimal-interfaces-one-implementation.md) |
| **D12** | **Multi-repo, event-driven.** The repository comes from the webhook's own origin. Deny-by-default allowlist. | [ADR 0007](docs/adr/0007-multi-repo-event-driven.md) |
| **D14** | **Two automated retries**, then escalate: preserve the debug branch, comment diagnostics, apply `status: needs-human`, drop from queue. | [ADR 0009](docs/adr/0009-two-retries-then-escalate.md) |
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
| **D9** | **Go 1.27** (installed locally: `go1.27.1 darwin/arm64`). | [ADR 0010](docs/adr/0010-go.md) |
| **D18** | Go interface naming follows Go convention (`Harness`, not `BaseHarness`) — **pending** confirmation in [Architecture boundaries #6](https://github.com/seppaleinen/idle-deck/issues/6). | Issue #1's `Base*` prefix is not idiomatic. |

### Configuration

| id | Decision | Rationale |
|---|---|---|
| **D23** | **Env-first configuration**: env vars, optional YAML file, CLI flags override. Tokens are never logged and never committed. | Twelve-factor, easy to run locally. Concrete var names are pinned in [#11](https://github.com/seppaleinen/idle-deck/issues/11). |

## Open, and deliberately so

- **The ontology is locked** ([`docs/ontology.md`](docs/ontology.md), resolved by
  [#5](https://github.com/seppaleinen/idle-deck/issues/5)). Six entities, six closed value sets,
  eleven named invariants, and an explicit negative list. Issue #1's flat `TaskItem` is split into
  `Task` + `TaskAttempt`; `repository_url` becomes a `Repository` entity; idle, provenance, and task
  lineage are additions. Retry counts and tier timeouts are policy and stayed out. The document
  carries a kept/reworded/dropped mapping for every part of #1, with reasons.
- **P0 and P3 have no trigger.** Both tiers exist in the ontology; nothing produces them. A gap in the
  event contract, not the ontology — [#9](https://github.com/seppaleinen/idle-deck/issues/9).
- **The remote contract is not written.** [#8](https://github.com/seppaleinen/idle-deck/issues/8).
  Notably unresolved: **does the workspace live on the remote side or locally?** Two incompatible
  architectures currently share one interface name. Invariant I8 (a running attempt is abortable via
  its session handle) is the obligation this imposes on the harness.
- **Queue lease semantics are the highest-consequence unknown.** Dequeue-while-running must either be a
  lease that expires (a daemon crash cannot strand a task) or a destructive pop that loses it. The whole
  fault-tolerance story depends on the answer. [#6](https://github.com/seppaleinen/idle-deck/issues/6).

## Related

- [`docs/ontology.md`](docs/ontology.md) — the locked domain model.
- [`README.md`](README.md) — what idle-deck is, for users and developers.
- [Map #3](https://github.com/seppaleinen/idle-deck/issues/3) — the live plan.
- [#1 Architecture Handover & Domain Ontology](https://github.com/seppaleinen/idle-deck/issues/1) —
  the draft this effort pressure-tested.
