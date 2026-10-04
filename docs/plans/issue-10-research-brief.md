# Research Brief: Issue #10 — MVP Backlog: Slice the First Runnable System

**Task:** Produce the ordered, execution-ready MVP backlog for idle-deck (the map's second lock).

**Domain:** mixed (dev + devops, but purely a planning/decision artifact — no code)

---

## Refined Requirements

From the locked contracts and issue #10 itself, the MVP must prove **one end-to-end loop**:

```
GitHub poll → Task (P1/P2) → Priority queue (lease) → Remote harness dispatch → Agent runs → Draft PR opened on the ticket → Human reviews
```

**Scope constraints (all locked):**
- Go 1.27, single static cgo-free binary (`go install`), main at repo root (D9, D32)
- GitHub polling, no webhooks, no inbound surface (D28, D30)
- SQLite queue, lease semantics, forward-only migrations (D24, D33)
- One remote HTTP harness adapter, workspace on remote (D7, D26, D27)
- `max_concurrent_jobs: 1` (D8)
- Idle = harness `/health` reports model reachability (D31)
- CLI only: `run`, `status`, `check`, `--version` (D33)
- Env + flags only, no YAML, no `.env` reader (D34, D36)
- Labels: `idle-hotfix`, `idle-ready`, `idle-redo`, `idle-needs-human` (D29)
- Two retries then escalate with debug branch (D14)
- P0 preempts running P2 (D16)
- Never merges; Draft PR only (D22)
- Single user, one machine (D4)
- P3 sweep: reporter only, files issues labelled `idle-needs-human`, never PR/feature branch (D43, D44, D45)

**Definition of Done for the MVP (the capstone):**
- Golden path runs against stubs (CI green)
- Golden path runs against **real** GitHub repo + **real** remote service → real Draft PR
- All invariants I1–I11 hold at end of run
- `v0.1.0` tag cut on the commit that passes; `go install ...@v0.1.0` reproduces working binary

---

## Web Findings

- **Go module layout**: Standard `main` at root + `cmd/` is unconventional but D32 explicitly requires "main package at the repo root, so the binary is named `idle-deck`". The `cmd/idle-deck` package exists for discoverability but is not the entry point.
- **Pure-Go SQLite**: `modernc.org/sqlite` v1.60.0 is the only driver that satisfies CGO_ENABLED=0 for static binary on all platforms. This is locked (D32).
- **launchd LaunchAgent**: macOS-only supervision; plist documented, no generator (D35). The four mechanical details (no `~` expansion, log dir must exist, `KeepAlive: SuccessfulExit false`, `ThrottleInterval`) are documented in operator-surface §3.
- **GitHub REST polling**: 60s interval, conditional requests (`If-Modified-Since`/`If-None-Match`), watermark on `updated_at`, pagination `asc` — all specified in event-contract §2, §8.
- **Conformance suite**: Nine client-observable cases against the wire contract (remote-contract §9a). This is what *falsifies* the harness seam instead of a second adapter (D41).

---

## App Source Findings

The repository currently contains **only** `AGENTS.md`, `README.md`, `docs/`, and `docs/adr/`. No Go code exists yet. The first implementation issue (#15) will create the repo skeleton.

**Package structure implied by boundaries.md and operator-surface.md:**
```
github.com/seppaleinen/idle-deck
├── main.go                    # entry point (D32)
├── config/                    # env + flags, redaction, validation
├── store/                     # SQLite schema, migrations, Queue impl
├── tracker/                   # GitHub TrackerSource + TrackerSink
├── harness/                   # Remote HTTP adapter + conformance suite
├── worker/                    # Composition root: idle gate, dispatch, preemption, escalation
├── idle/                      # HarnessIdlePolicy (probes /health)
├── cmd/idle-deck/             # Reference layout (no main)
└── test/stubs/                # httptest servers for tracker + harness (dev only)
```

---

## Infra Findings

No cluster/GitOps infrastructure exists for idle-deck itself — it runs as a local binary on the operator's machine. The "remote execution service" is an external dependency (not managed by this project). The only local state is the SQLite file at `~/Library/Application Support/idle-deck/idle-deck.db`.

---

## Open Questions Answered (from locked decisions)

| Question | Answer | Source |
|----------|--------|--------|
| Slicing order? | Cross-cutting foundation first (skeleton, config, store), then adapters (tracker, harness, idle), then worker, then CLI, then supervision doc, then golden path capstone. | D2, D3, D13, D23–D39 |
| How to defer without fake abstractions? | `// TODO(#N)` comments in code; deferral lines in issue bodies; no empty interface methods; no `Base*` names (D18). | D13, D18, D34, D42 |
| Cross-cutting vs feature slices? | Foundation: #15 (skeleton), #14 (config), #17 (store/queue). Adapters: #16 (stubs), #18 (tracker), #20 (harness), #22 (idle). Composition: #19 (worker). Surface: #23 (CLI), #25 (launchd doc). Capstone: #24 (golden path + tag). P3: #26 (blocked on #21). | D13, D25, D26, D27, D33 |
| Done-when per issue? | Each issue (#14–#26) already has checkable done-when criteria in its body. | Issues #14–#26 |
| Parallelizable from clean checkout? | #16 (stubs) can run after #15+#14; #22 (idle) after #15+#14; #25 (launchd doc) after #15+#23. Worker (#19) and golden path (#24) are serial bottlenecks. | Dependency graph below |
| Still-open decision blocking a slice? | **Yes: #21 (D43 gap)** — how a P3 sweep files issues. Three options (A/B/C) require a new ADR. #26 is blocked until resolved. | Issue #21, D42, D43 |

---

## Remaining Risks

1. **#21 (D43 gap) is a decision, not implementation** — must be resolved by user via new ADR before #26 can start. Three options recorded in #21; user must choose.
2. **`since` + `labels` composition** — event-contract §8 assumes `since` composes with `labels` on GitHub's issues endpoint. This is unverified against real API; #18 done-when #6 requires one real check. If it fails, adapter must fall back to unconditional label queries (chattier but correct).
3. **Real remote service for conformance suite** — #20 done-when #1 requires passing all nine cases against a *real* remote service, not the stub. The remote service must exist and be configured before #20 can complete.
4. **`v0.1.0` tag timing** — Must be cut on the exact commit that makes the golden path pass against a real remote service (operator-surface §2, #24). Not on `main` after drift.
5. **CI smoke test conflict** — #15 plan expects `check` to exit 0; #14 requires `check` to exit non-zero without config. CI must be updated in #14 (W5).
6. **No `Config` seam in boundaries.md** — #14 plan notes config is cross-cutting data package, not an adapter seam. If a seam is needed later, an ADR is required (R2 in #14 plan).

---

## Proposed Ordered Backlog

The backlog below is **already created as GitHub issues #14–#26**. This brief validates the ordering, surfaces the blocking edges, and confirms each issue is independently startable.

### Cross-Cutting Foundation (must run first, serial)

| Issue | Title | Blocks | Key Done-When |
|-------|-------|--------|---------------|
| **#15** | Repository Skeleton, Go Module, and CI Gates | All | `go build ./...` passes; CI green on empty tree; D-id check passes; smoke test runs |
| **#14** | Configuration Layer: Env Vars + Flags, No YAML | #16, #18, #20, #22, #23 | `check` refuses without 4 required vars; with config, prints redacted output; no `.env` reader |
| **#17** | Storage: SQLite Schema, Forward-Only Migrations, Queue Lease | #18, #19, #23, #26 | Lease expiry records `timeout` + re-queues; idempotent enqueue; ordering P0>P2, FIFO within tier; `Events` fires for preemption |

### Adapter Slices (can partially parallelize after foundation)

| Issue | Title | Depends On | Key Done-When |
|-------|-------|------------|---------------|
| **#16** | In-Repo HTTP Stubs for Tracker and Harness | #15, #14 | `make dev` starts stubs + daemon; golden path runs against stubs; CI green |
| **#18** | GitHub Tracker Adapter: Poll, Watermark, Four Queries, Label Lifecycle | #15, #14, #17, #16 | Four worked examples produce correct `Task`; PRs/bots skipped; dedupe idempotent; trigger label lifecycle correct; `since`+`labels` checked vs real API |
| **#20** | Harness Adapter: Wire Contract, Outcome Mapping, Conformance Suite | #15, #14 | All 9 conformance cases pass vs real remote; failure-code list mapped; `RunRequest` has no model field |
| **#22** | Idle Policy: Probe Harness `/health` for Model Reachability | #15, #14 | Stub `reachable: true` → `Idle()=true`; `reachable: false`/error → `false` + error; worker re-probes |

### Composition Root (serial bottleneck)

| Issue | Title | Depends On | Key Done-When |
|-------|-------|------------|---------------|
| **#19** | Worker: Composition Root, Idle Gate, Dispatch, Preemption, Escalation | #15, #14, #17, #16, #18, #20, #22 | P1→comment succeeds; P2 fails 2×→escalates with debug branch + `idle-needs-human`; P0 preempts P2 within 60s; `idle-redo`→new task P1 fresh budget; `status` reports 7 items |

### Surface & Documentation

| Issue | Title | Depends On | Key Done-When |
|-------|-------|------------|---------------|
| **#23** | CLI Surface: run, status, check, --version | #15, #14, #17, #19, #22 | `--version` prints; `check` 6 steps; `run` starts worker; `Ctrl-C` exits; `status` reads DB live |
| **#25** | Supervision: Documented launchd LaunchAgent | #15, #23 | Plist in repo with 4 mechanical details; human can copy/adjust/bootstrap; `bootout` stops cleanly |

### Decision Gate (blocks P3)

| Issue | Title | Depends On | Key Done-When |
|-------|-------|------------|---------------|
| **#21** | **Decision: How Does a P3 Sweep File One Issue Per Finding?** | #18, #20 | New ADR accepted (A/B/C); contract docs amended; #26 unblocked |

### Capstone (serial, last)

| Issue | Title | Depends On | Key Done-When |
|-------|-------|------------|---------------|
| **#24** | Golden Path: End-to-End Proof + v0.1.0 Tagged Release | **All above** | Integration test passes vs stubs; same path passes vs real GitHub + real remote → real Draft PR; all I1–I11 hold; `v0.1.0` tag cut on passing commit; `go install @v0.1.0` works |

### P3 Slice (blocked on #21)

| Issue | Title | Depends On | Key Done-When |
|-------|-------|------------|---------------|
| **#26** | P3 Idle Sweep: Scheduler Tick, Config Pair, Report-to-Issue Path | **#21 (blocked)**, #17, #18, #20, #19 | P3 enqueued on bucket boundary; dedupe collapses ticks; sweep returns `report` → files issues `idle-needs-human`; forbidden artifacts (`pull_request`/`feature` branch) → non-retryable violation; `SWEEP_PERIOD=0` disables; loop-freedom (D45) verified |

---

## Blocking Edges (Dependency Graph)

```
#15 (skeleton)
  └─▶ #14 (config)
        ├─▶ #16 (stubs)
        ├─▶ #17 (store/queue) ──▶ #18 (tracker) ──┐
        ├─▶ #22 (idle policy)                     │
        └─▶ #20 (harness)                         ▼
                            #19 (worker) ◀────────┘
                                │
                                ▼
                         #23 (CLI)
                                │
                                ▼
                         #25 (launchd doc)
                                │
                                ▼
                         #24 (golden path + tag)  ◀── #21 (decision) ──▶ #26 (P3 sweep)
```

**Parallelizable pairs (from clean checkout, after deps met):**
- `#16` and `#22` — both only need `#15` + `#14`
- `#18` and `#20` — both need `#15` + `#14` + `#17` + `#16`; can run in parallel
- `#25` — only needs `#15` + `#23`; can run anytime after CLI

**Serial bottlenecks (cannot parallelize):**
- `#17` must finish before `#18`, `#19`, `#23`, `#26`
- `#19` must finish before `#23`, `#24`
- `#21` must finish before `#26`
- `#24` is the final integration — everything must be done

---

## Cross-Cutting vs Feature Slices

| Cross-Cutting (Foundation) | Feature Slices (Depend on Foundation) |
|----------------------------|---------------------------------------|
| #15 Repo skeleton + CI | #16 Stubs (dev tooling) |
| #14 Config (env/flags/redaction) | #18 Tracker adapter (poll/watermark/queries) |
| #17 Store + Queue lease + migrations | #20 Harness adapter (wire/contract/conformance) |
| | #22 Idle policy (probe /health) |
| | #19 Worker (composition root) |
| | #23 CLI (run/status/check/version) |
| | #25 Launchd documentation |
| | #21 Decision (D43 gap) |
| | #24 Golden path (capstone) |
| | #26 P3 sweep (blocked on #21) |

---

## Explicit Non-Goals Per Slice (Consolidated)

| Slice | Non-Goals |
|-------|-----------|
| All | No second adapter implementations (D13, D40) |
| All | No webhook receiver, no signature verification (D28) |
| All | No merge — Draft PR only (D22) |
| All | No GUI — CLI only (D19) |
| All | No multi-user, no RBAC (D4) |
| #14 | No YAML/TOML/JSON config file; no `.env` reader (D34, D36) |
| #17 | No second queue impl; no `queue ls`; no retention/vacuum; no retry-count column |
| #18 | No PR production (harness returns artifact); no comment-driven channel; no auto label creation (D39) |
| #19 | No multi-worker; no `stop`/`once`/`enqueue`/`queue ls`; no live cancellation; no merge |
| #20 | No second harness impl; no SSE; no local workspace/git; no model names in request; no global spend ceiling |
| #22 | No platform idle heuristics; no second IdlePolicy impl; no caching beyond worker loop |
| #23 | No HTTP endpoint/metrics; no plist generator |
| #24 | No merge; no GUI; no multi-user; no global ceiling; no live cancellation |
| #25 | No plist generator; no systemd; no LaunchDaemon |
| #26 | No PR, no feature branch, no escalation for sweep; no adaptive backoff; no per-repo period syntax; no policy catalogue |

---

## Still-Open Decisions That Block a Slice

| Decision | Blocking | Status |
|----------|----------|--------|
| **D43 gap (#21)**: How does a P3 sweep file issues? | #26 (P3 sweep) cannot start | **OPEN** — requires new ADR choosing Option A, B, or C from #21 |
| Global spend ceiling | #20 (harness), #24 (golden path) | **DEFERRED** — impossible until remote reports usage (D37) |
| Release binaries / Homebrew / container | Distribution | **DEFERRED** — until second user exists (D32, operator-surface §2) |
| macOS Keychain | Secrets hardening | **DEFERRED** — launchd plist `chmod 600` is sufficient for MVP (D36) |
| YAML config file | Config layer | **DEFERRED** — D23 permits; MVP does not build (D34) |
| CLI tier override for `idle-redo` | CLI surface | **DEFERRED** — `idle-redo` is the affordance (D33) |
| Live cancellation / `aborted` outcome | Ontology, worker | **DEFERRED** — `aborted` reserved, no feature (ontology) |
| Webhook adapter | Tracker | **DEFERRED** — hosted multi-user idle-deck deferred by D4 |

---

## Verification

The brief file exists at the target path: