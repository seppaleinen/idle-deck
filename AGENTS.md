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
pointer. Nothing is silently rewritten. An ADR's **body** is immutable; in the header, `Status:`
flips and `Related:` is correctable (**D42**) — the rest is frozen.

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
| **D5** *(superseded)* | ~~**Idle means: idle-deck can reach the model.** It probes the inference server; if it answers, the engine is considered idle. No platform idle heuristics.~~ **Definition kept, probe location changed by D31.** | [ADR 0003](docs/adr/0003-idle-is-a-probe.md) → superseded by [ADR 0016](docs/adr/0016-idle-probed-at-the-harness.md) |
| **D31** | **Idle is probed at the harness's `GET /health`, and that endpoint must report model reachability, not process liveness.** Definition unchanged; no platform heuristics. | [ADR 0016](docs/adr/0016-idle-probed-at-the-harness.md). One URL and one credential instead of two, and D6 keeps model knowledge out of the coordinator. The reachability requirement is load-bearing: a liveness ping would report a model-down machine as idle. |
| **D6** | **idle-deck never names a model.** It passes a *role* (`plan` / `do` / `sweep`) plus a budget to the harness. The harness decides the model. Widened to *provider* and *vendor* by **D40**. | [ADR 0004](docs/adr/0004-never-name-a-model.md) |
| **D7** | MVP ships **exactly one** harness adapter, talking to a remote execution service over JSON/HTTP. What obliges an adapter to exist, and when a second may be added, is **D41**. | [ADR 0005](docs/adr/0005-one-harness-adapter.md) |
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

| **D43** | A P3 sweep **files one issue per finding**, labelled `idle-needs-human`, and may **never** open a PR or produce a `feature` branch. Both are non-retryable protocol violations. The sweep's lens is a **configured prompt** with a narrow default (stale TODO/FIXME with age and last-touched context); cadence is `IDLE_DECK_SWEEP_PERIOD` (default `168h`, `0` disables), with `IDLE_DECK_SWEEP_REPOS` as a narrowing subset of the allowlist. No adaptive backoff. | [ADR 0019](docs/adr/0019-p3-sweep-policy.md). A sweep is a **reporter, not an author**: it finds work, never does it, and the route to a Draft PR runs through a human applying `idle-ready`. The PR prohibition is D15's corollary — `feature/<ticket_id>-<slug>` has no `ticket_id` for a sweep to put there. The config-over-built-in choices answer the ticket's mute risk with a value the operator owns, rather than with machinery nobody can observe from outside. |
| **D44** | **`Task.ticket` is required for every tier except P3**, where it is null. | [ADR 0019](docs/adr/0019-p3-sweep-policy.md). Amends the ontology. A sweep's task exists when the scheduler ticks, before any run, so no issue exists to point at; the issue a sweep *files* is a different task's ticket. The ontology already listed `schedule.sweep` in `TriggeredBy.event_type` and then made mandatory the one field a sweep cannot supply — the model anticipated sweeps and had no way to say so. |
| **D45** | **idle-deck never re-ingests an artifact it produced.** P1 skips issues carrying `idle-needs-human`, so a sweep cannot read back its own write. | [ADR 0019](docs/adr/0019-p3-sweep-policy.md). Stated as loop-freedom rather than as bot-detection because the existing `user.type == "Bot"` guard is a property of the **credential**, and D4 makes a human account the likely one — under which a sweep's issue would become an unattended P1 at 5,000 tokens a pop. Bot-detection was already known not to cover idle-deck's own label writes; this makes the invariant general instead of a property of the one case that happened to be safe. |

### Naming and lifecycle conventions

| id | Decision | Rationale |
|---|---|---|
| **D15** | Debug branches are `debug/<task_id>`. Feature branches are `feature/<ticket_id>-<slug>`. | Debug is short and unique; feature branches carry the ticket for traceability. |
| **D21** | Successful runs push a feature branch, open a Draft PR, and **clean up the workspace**. Failed and timed-out runs preserve state to a debug branch. | Keeps the retry path debuggable without hoarding branches forever. |

### Language and interfaces

| id | Decision | Rationale |
|---|---|---|
| **D9** | **Go 1.27** (installed locally: `go1.27.1 darwin/arm64`). | [ADR 0010](docs/adr/0010-go.md) |
| **D42** | **An ADR's body is frozen; its header's cross-reference fields are not.** `Status:` flips and `Superseded by:` is added by design; `Related:` may be corrected, because a navigational pointer asserts nothing. `Title:`, `Source:`, `Supersedes:`, and everything from `## Decision` onward are immutable. A correction is its own commit, not a supersession. | The convention claimed "the only mutable field is `Status:`" while the same document instructed adding a `Superseded by:` line — so the header already gained fields and the rule was under-specified, not strict. It surfaced on ADR 0017, whose `Related:` listed D31 (the idle probe, unrelated) and omitted D32 (its own decision, which 0004/0005 both list). A rule nobody can apply is the same defect as an id cited before it is defined. |
| **D18** | Go interface naming follows Go convention (`Harness`, not `BaseHarness`). **Confirmed** in [Architecture boundaries #6](https://github.com/seppaleinen/idle-deck/issues/6): interfaces are named by behaviour; `Base*` is dead. | Issue #1's `Base*` prefix is not idiomatic. |
| **D24** | **Queue dequeue is a lease, never a pop.** Expiry without ack is recorded as `timeout` on the abandoned attempt and the task is re-queued, consuming one retry. No lost-task path. | [ADR 0011](docs/adr/0011-queue-lease.md) |
| **D25** | The tracker seam splits into **`TrackerSource`** (inbound parse, allowlist gate I1) and **`TrackerSink`** (outbound comment/label), one GitHub implementation. PR production is the harness's, not the tracker's (I4). | [ADR 0012](docs/adr/0012-tracker-split.md) |
| **D26** | The **`Harness`** interface is execution lifecycle only — `Start` / `Abort` / `Result` over a remote execution service, carrying a role never a model. No `prepare_workspace`, no local git. Workspace ownership is deferred to #8. | [ADR 0013](docs/adr/0013-harness-lifecycle.md) |
| **D27** | The **workspace lives on the remote**. The remote owns checkout, git, and Draft PR production; idle-deck gets typed artifacts as data. Wire: `POST/GET/DELETE /runs` + health, idempotent by `attempt_id`, server-enforced timeout, bearer+TLS, zero inbound calls. Contract: `docs/architecture/remote-contract.md`. | [ADR 0014](docs/adr/0014-remote-execution-service-contract.md) |
| **D40** | **"Framework-agnostic" means idle-deck names no model, provider, or vendor** — and nothing more. Provable by construction: `RunRequest` has no field a name could occupy. The MVP makes **no** claim that a second `Harness` implementation drops in. | [ADR 0018](docs/adr/0018-framework-agnostic-means-never-naming-a-vendor.md). [#12](https://github.com/seppaleinen/idle-deck/issues/12) was asked whether to ship a second adapter, but #8 had already made that option a *reversal* of D27 rather than a backlog item, and the word was carrying two claims of which only one is provable. A headline that outruns the evidence is what the decision record exists to prevent. |
| **D41** | **The harness seam is falsified by a conformance suite, not a second adapter.** The MVP adapter ships with nine client-observable cases specified against the wire contract (`docs/architecture/remote-contract.md` §9a), and a second adapter is added only when a named trigger fires: a second execution model is wanted, a case proves **unfalsifiable** client-side, or a second operator needs a different harness. | [ADR 0018](docs/adr/0018-framework-agnostic-means-never-naming-a-vendor.md). The unproven part is the *contract* — idempotency, server-enforced timeout, confirmed-dead abort, the outcome taxonomy — and a local-subprocess adapter shares none of it. D40's deferral without a trigger list would be an unowned promise, which is the same defect as the broad claim. |

### Configuration

| id | Decision | Rationale |
|---|---|---|
| **D23** | **Env-first configuration**: env vars, optional YAML file, CLI flags override. Tokens are never logged and never committed. | Twelve-factor, easy to run locally. Concrete var names are pinned in [#11](https://github.com/seppaleinen/idle-deck/issues/11). |
| **D34** | **MVP configuration is env vars + CLI flags only — no YAML file.** Precedence flags > env > defaults. Concrete names, and the required-vs-defaulted split, in [`docs/operations/operator-surface.md`](docs/operations/operator-surface.md). | A narrowing of **D23**'s *scope*, not a supersession: D23 permits an optional YAML file, and the MVP simply does not build it. A second source of truth can disagree with the first, and "which one won" is a bug class env-only config cannot have. |

### Artifact, CLI, and operations

| id | Decision | Rationale |
|---|---|---|
| **D32** | **One static cgo-free binary, `go install`**, main package at the repo root, pure-Go SQLite. Release binaries, Homebrew, and containers deferred. | [ADR 0017](docs/adr/0017-the-artifact.md). The driver is the whole decision: cgo means a C toolchain on every machine forever. `max_concurrent_jobs: 1` (D8) means throughput is not a constraint this project has. |
| **D33** | **The CLI surface is `run`, `status`, `check`, `--version`.** Automatic forward-only migrations at startup. No `stop`, no `once`/cron, no `enqueue`/tier override, no `queue ls`. | Each absence has a reason: `launchctl bootout` is the stop; `once` would give the lease, heartbeat, and watermark three owners depending on invocation and would break P0 preemption's resident worker; a tier override is a second ingestion path for something `idle-redo` already does with one click. |
| **D35** | **Supervision is a documented `launchd` LaunchAgent** (per-user, no root, `KeepAlive: SuccessfulExit false`), plus foreground `run` for development. **No plist generator.** | macOS has `launchd`, not `systemd`. A generator is a second surface to keep in sync with the config surface, bought with a one-time copy-paste. |
| **D36** | **Secrets are the environment and nothing else.** No `.env` reader. Redaction in the config layer, never at each log call. Never written to SQLite. Rotation is rotate-and-restart; a mid-run expiry is an ordinary retryable failure. Keychain deferred. | Closes the map's secrets fog. A program that silently reads a file from its working directory is a surprise, and a secrets file is a second source of truth. The `0600` plist or wrapper is read by `launchd`/the shell, never by idle-deck. |
| **D37** | **`budget` is a generation-token ceiling.** Per-role defaults: plan 5000, sweep 20000, hotfix 50000, do 100000. Tier timeouts: plan 900s, sweep 3600s, **hotfix 1800s**, do 10800s. Retry backoff exponential from 30s capped at 10m, `Retry-After` honoured. **No global spend ceiling.** | The wire contract carried three budget numbers with **no unit**; tokens are the natural currency for an LLM harness and match the existing magnitudes. P0 is the only timeout this project added: a hotfix longer than a sweep is a feature, and every second it runs is a second the P2 it displaced stays aborted. A global ceiling is **not** an oversight — idle-deck cannot observe remote-metered spend, so a cap it cannot see is a number that does nothing. |
| **D38** | **Local development runs against in-repo HTTP stubs behind base-URL overrides** (`IDLE_DECK_GITHUB_API`, `IDLE_DECK_HARNESS_URL`) — never a second `TrackerSource`. | D28 made the dev loop stateless: polling observes state, so there is nothing to inject. The base-URL override exercises the *real* adapters; a second implementation would only ever run under a test, which is the second-implementation question #12 asks, answered by an artifact nobody ships. The override also serves GitHub Enterprise, so it is not test-only. |
| **D39** | **No automatic label creation.** `check` reports which of the four D29 labels are missing and prints the exact `gh label create` commands. Colours documented as a suggested palette, never applied. | Auto-creating writes to a repository's label namespace — a visible mutation needing admin permission, done silently on first run, in a namespace shared with humans. That is the same objection that made D29's prefix a safety property rather than a style choice. |

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
  local scheduler (P3), `idle-redo` (redefinition); labels are D29. Live cancellation is still open.
  Cost/rate-limit policy, secrets lifecycle, and label colours are **closed** — D37, D36,
  and D39 on the operator surface. **What a P3 sweep does is now closed too** — D43/D44/D45,
  [ADR 0019](docs/adr/0019-p3-sweep-policy.md): a sweep is a *reporter*, filing one issue per
  finding, never a PR, never re-ingesting its own write.
- **The remote contract is locked.** [#8](https://github.com/seppaleinen/idle-deck/issues/8) —
  **the workspace lives on the remote** (D27, [ADR 0014](docs/adr/0014-remote-execution-service-contract.md),
  wire contract at `docs/architecture/remote-contract.md`): typed results, idempotency by
  `attempt_id`, server-enforced timeout, bearer+TLS, zero inbound calls. Cost/rate-limit policy and
  secrets lifecycle are **closed** by D37/D36; the exact failure-code list is owed by #10. The
  contract now also carries the **conformance suite** (§9a, D41) — nine client-observable cases the
  MVP adapter must pass, which is what [#12](https://github.com/seppaleinen/idle-deck/issues/12)
  decided instead of a second adapter.
- **The operator and developer surface is locked.** [#11](https://github.com/seppaleinen/idle-deck/issues/11) —
  **one static cgo-free binary** installed with `go install` (D32, [ADR
  0017](docs/adr/0017-the-artifact.md)), env vars + flags and **no YAML** (D34, the MVP scope of
  D23), CLI limited to `run`/`status`/`check`/`--version` with health as a command and **no HTTP
  endpoint** (D33, which follows D28's no-inbound-surface), supervision by a documented `launchd`
  LaunchAgent (D35), secrets env-only with no `.env` reader (D36), and `budget` pinned as a
  **generation-token** ceiling per role with P0 at 1800s/50000 (D37). The idle probe is now
  unambiguously the harness's `/health`, and must report reachability rather than liveness (D31,
  [ADR 0016](docs/adr/0016-idle-probed-at-the-harness.md)) — which **supersedes D5 and ADR 0003**,
  the second real supersession, revising a decision's mechanism while keeping its definition. The
  surface is in [`docs/operations/operator-surface.md`](docs/operations/operator-surface.md). Still
  open: a **global spend ceiling** (impossible until the remote reports usage), release
  binaries/Homebrew/containers (until there is a second user), a YAML file, and the macOS Keychain.
- **Queue lease semantics — resolved.** Dequeue is a lease with expiry (D24, [ADR
  0011](docs/adr/0011-queue-lease.md)): expiry without ack records `timeout` on the abandoned attempt
  and re-queues, consuming one retry. A daemon crash cannot strand a task.
- **What "framework-agnostic" means is resolved.** [#12](https://github.com/seppaleinen/idle-deck/issues/12) —
  the headline word is narrowed to *idle-deck names no model, provider, or vendor* (D40), a property of
  `RunRequest`'s shape rather than a claim about adapter count, and the MVP claims no pluggability it
  cannot show. The seam is falsified by the **conformance suite** instead of a second implementation
  (D41), with a named trigger list for when a second adapter may be added. The ticket's own "ship a
  local subprocess too" option had quietly become a reversal of D27 rather than a backlog item, and a
  second agent CLI is not a second adapter at all — it runs *inside* the remote.


## Related

- [`docs/ontology.md`](docs/ontology.md) — the locked domain model.
- [`README.md`](README.md) — what idle-deck is, for users and developers.
- [Map #3](https://github.com/seppaleinen/idle-deck/issues/3) — the live plan.
- [#1 Architecture Handover & Domain Ontology](https://github.com/seppaleinen/idle-deck/issues/1) —
  the draft this effort pressure-tested.
