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
| **D12** *(superseded)* | ~~Multi-repo, event-driven. The repository comes from the webhook's own origin. Deny-by-default allowlist.~~ Mechanism replaced by **D30**; deny-by-default intent unchanged. | [ADR 0007](docs/adr/0007-multi-repo-event-driven.md) → superseded by [ADR 0015](docs/adr/0015-github-event-contract.md) |
| **D30** | **Multi-repo, config-driven polling.** The repository is the **configured target**, not an inbound event's origin. Deny-by-default allowlist, **checked at ingestion** (I1). | [ADR 0015](docs/adr/0015-github-event-contract.md). idle-deck polls, so it *names* the repos it reads — the trust boundary is a config list, not a filter on hostile traffic. |
| **D14** | **Two automated retries**, then escalate: preserve the debug branch, comment diagnostics, apply the needs-human label (string in **D29**), drop from queue. | [ADR 0009](docs/adr/0009-two-retries-then-escalate.md) |
| **D17** | `TaskItem.timeout_seconds` **overrides** the tier default when set. A killed or preempted run counts as a retryable failure. | Explicit beats implicit. Timeouts are load-bearing. |
| **D16** | With one worker, a **P0 aborts the running P2** and takes the slot. Aborted runs follow normal retry/escalation. | Makes "immediate preemption" real at `max_concurrent_jobs: 1`. |
| **D28** | **The event contract**: idle-deck **polls** (60s, conditional requests, no inbound surface, no signature to verify); one watermark per repo seeded at first observation; every tier has a trigger; **dedupe is a stable resource key**; the trigger label is removed on any terminal state; the prompt is the issue's title and body verbatim; closing an issue does nothing. | [ADR 0015](docs/adr/0015-github-event-contract.md). Contract in [`docs/architecture/event-contract.md`](docs/architecture/event-contract.md). |
| **D29** | **Label vocabulary**: `idle-hotfix`, `idle-ready`, `idle-redo`, `idle-needs-human`. Lowercase, hyphens, no space/colon/slash, all `idle-`-prefixed. | [ADR 0015](docs/adr/0015-github-event-contract.md). The prefix is **collision safety**, not style: a bare `ready` would hijack a repo's own label vocabulary and silently spawn agent runs. Normalises D14's `status: needs-human` literal; D14's behaviour is untouched. |

### Naming and lifecycle conventions

| id | Decision | Rationale |
|---|---|---|
| **D15** | Debug branches are `debug/<task_id>`. Feature branches are `feature/<ticket_id>-<slug>`. | Debug is short and unique; feature branches carry the ticket for traceability. |
| **D21** | Successful runs push a feature branch, open a Draft PR, and **clean up the workspace**. Failed and timed-out runs preserve state to a debug branch. | Keeps the retry path debuggable without hoarding branches forever. |

### Language and interfaces

| id | Decision | Rationale |
|---|---|---|
| **D9** | **Go 1.27** (installed locally: `go1.27.1 darwin/arm64`). | [ADR 0010](docs/adr/0010-go.md) |
| **D18** | Go interface naming follows Go convention (`Harness`, not `BaseHarness`). **Confirmed** in [Architecture boundaries #6](https://github.com/seppaleinen/idle-deck/issues/6): interfaces are named by behaviour; `Base*` is dead. | Issue #1's `Base*` prefix is not idiomatic. |
| **D24** | **Queue dequeue is a lease, never a pop.** Expiry without ack is recorded as `timeout` on the abandoned attempt and the task is re-queued, consuming one retry. No lost-task path. | [ADR 0011](docs/adr/0011-queue-lease.md) |
| **D25** | The tracker seam splits into **`TrackerSource`** (inbound parse, allowlist gate I1) and **`TrackerSink`** (outbound comment/label), one GitHub implementation. PR production is the harness's, not the tracker's (I4). | [ADR 0012](docs/adr/0012-tracker-split.md) |
| **D26** | The **`Harness`** interface is execution lifecycle only — `Start` / `Abort` / `Result` over a remote execution service, carrying a role never a model. No `prepare_workspace`, no local git. Workspace ownership is deferred to #8. | [ADR 0013](docs/adr/0013-harness-lifecycle.md) |
| **D27** | The **workspace lives on the remote**. The remote owns checkout, git, and Draft PR production; idle-deck gets typed artifacts as data. Wire: `POST/GET/DELETE /runs` + health, idempotent by `attempt_id`, server-enforced timeout, bearer+TLS, zero inbound calls. Contract: `docs/architecture/remote-contract.md`. | [ADR 0014](docs/adr/0014-remote-execution-service-contract.md) |

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
- **The event contract is locked.** [#9](https://github.com/seppaleinen/idle-deck/issues/9) — idle-deck
  **polls** every 60s with no inbound surface (D28, [ADR 0015](docs/adr/0015-github-event-contract.md),
  contract at `docs/architecture/event-contract.md`): one watermark per repo seeded at first
  observation, the configured repo list is the allowlist (D30), a stable resource dedupe key, the
  trigger label removed on any terminal state, and the prompt is the issue's title and body verbatim.
  **Every tier now has a trigger** — `idle-hotfix` (P0), issue creation (P1), `idle-ready` (P2), a
  local scheduler (P3), `idle-redo` (redefinition); labels are D29. Still open: what a P3 *sweep*
  looks for (trigger settled, content not), cost/rate-limit policy, secrets lifecycle, live
  cancellation, and label colours — fog onto the operator surface
  ([#11](https://github.com/seppaleinen/idle-deck/issues/11)) and the #10 adapter spec.
- **The remote contract is locked.** [#8](https://github.com/seppaleinen/idle-deck/issues/8) —
  **the workspace lives on the remote** (D27, [ADR 0014](docs/adr/0014-remote-execution-service-contract.md),
  wire contract at `docs/architecture/remote-contract.md`): typed results, idempotency by
  `attempt_id`, server-enforced timeout, bearer+TLS, zero inbound calls. Cost/rate-limit policy,
  secrets lifecycle, and the exact failure-code list are still open — fog onto the operator surface
  ([#11](https://github.com/seppaleinen/idle-deck/issues/11)) and the #10 adapter spec.
- **Queue lease semantics — resolved.** Dequeue is a lease with expiry (D24, [ADR
  0011](docs/adr/0011-queue-lease.md)): expiry without ack records `timeout` on the abandoned attempt
  and re-queues, consuming one retry. A daemon crash cannot strand a task.

## Related

- [`docs/ontology.md`](docs/ontology.md) — the locked domain model.
- [`README.md`](README.md) — what idle-deck is, for users and developers.
- [Map #3](https://github.com/seppaleinen/idle-deck/issues/3) — the live plan.
- [#1 Architecture Handover & Domain Ontology](https://github.com/seppaleinen/idle-deck/issues/1) —
  the draft this effort pressure-tested.
