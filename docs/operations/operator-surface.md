# idle-deck operator and developer surface

How idle-deck is installed, configured, run, supervised, observed, and developed against.

**Status:** locked by [Install, run, configure: the operator and developer
surface #11](https://github.com/seppaleinen/idle-deck/issues/11). Specific enough that an
implementation session writes the code from this document without making a further decision.

**Decisions:** [ADR 0016](../adr/0016-idle-probed-at-the-harness.md) (idle probe) · [ADR
0017](../adr/0017-the-artifact.md) (artifact, distribution, CI) — and **D31**–**D38** in
[`AGENTS.md`](../../AGENTS.md).

Two premises from earlier locks shape everything here, so they are worth stating before the surface
itself. **D28** means there is no inbound network surface, so there is no health *endpoint* to
expose — health is a CLI command reading local state. **D27** means the workspace, git, and Draft PR
production live on the remote, so "install idle-deck" installs a coordinator with almost no local
dependencies: one static binary and one SQLite file.

---

## 1. The artifact

**One static, cgo-free Go binary, with its main package at the repository root.**

| Property | Value |
|---|---|
| Module path | `github.com/seppaleinen/idle-deck` |
| Main package | repository root (so the binary is named `idle-deck`) |
| Language | Go 1.27 (D9) |
| CGO | `CGO_ENABLED=0` — static, no C toolchain anywhere |
| Queue storage | SQLite via `modernc.org/sqlite` (pure Go) |

**Why cgo-free is not negotiable for the MVP.** The SQLite *driver* is the only thing that decides
whether cgo is involved, and D13 says "SQLite queue" without naming one. `mattn/go-sqlite3` is the
faster, more battle-tested driver and it needs cgo — which means a C toolchain on every machine that
builds idle-deck, `CGO_ENABLED=0` becomes impossible, cross-compiling needs a C cross-compiler per
target, and `go install` breaks on any machine without Xcode CLT. `modernc.org/sqlite` (v1.60.0,
declares `go 1.26.0`, dependency chain entirely pure Go) trades raw SQLite throughput for a binary
that installs anywhere Go does. At `max_concurrent_jobs: 1` (D8) with one small queue, throughput
is not a constraint this project has.

## 2. Installation

```bash
go install github.com/seppaleinen/idle-deck@latest        # convenience: tracks main
go install github.com/seppaleinen/idle-deck@v0.1.0        # reproducible: a real tag
```

The repository is public and had **no tags** when this was decided, so `@latest` resolves to a
pseudo-version of `main` — it is a moving target. A `v0.x.y` tag is cut when the first working
binary exists (#10), and the tagged form is the reproducible install. Nothing needs to be built
locally; a developer who wants to hack on it runs `go build` from a checkout.

**Deferred, with the reason recorded so it is re-opened by evidence rather than re-derived:**
release binaries per platform, a Homebrew formula, and a container image. All three exist to serve
someone who does not have Go, and D4 scopes the MVP to a single user on one machine — the owner of
this repository, who has Go. A container in particular buys isolation from dependencies idle-deck
barely has: no inbound surface, one static binary, one SQLite file. The first person who is not that
user should reopen this section.

## 3. Running it

Two modes, and no third.

**Foreground, for development and for anyone who wants to watch it work:**

```bash
idle-deck run
```

Runs in the foreground, logs structured JSON to stderr, exits on `Ctrl-C`. This is also the
developer path — see §9.

**Supervised, as an operator runs it — a `launchd` LaunchAgent.** macOS is the platform, so
`launchd` is the native supervisor and `systemd` is not a candidate at all. A **LaunchAgent**
(per-user, `~/Library/LaunchAgents/`, starts at login, no root) rather than a **LaunchDaemon**
(system-wide, starts at boot, needs root): idle-deck is a personal tool acting as your GitHub
identity, so it belongs to your user session and starts when you log in.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>                  <string>com.seppaleinen.idle-deck</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/you/go/bin/idle-deck</string>
    <string>run</string>
  </array>
  <key>RunAtLoad</key>              <true/>
  <key>KeepAlive</key>
  <dict><key>SuccessfulExit</key>   <false/></dict>
  <key>ThrottleInterval</key>       <integer>10</integer>
  <key>EnvironmentVariables</key>
  <dict>
    <key>IDLE_DECK_GITHUB_TOKEN</key>  <string>github_pat_...</string>
    <key>IDLE_DECK_HARNESS_TOKEN</key>  <string>...</string>
  </dict>
  <key>StandardOutPath</key>        <string>/Users/you/Library/Logs/idle-deck/stdout.log</string>
  <key>StandardErrorPath</key>      <string>/Users/you/Library/Logs/idle-deck/stderr.log</string>
</dict>
</plist>
```

```bash
mkdir -p ~/Library/Logs/idle-deck          # launchd does not create this, and fails without it
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.seppaleinen.idle-deck.plist
launchctl bootout  gui/$(id -u)/com.seppaleinen.idle-deck
```

Four mechanical details, each of which bites:

- **`launchd` does not expand `~`.** Every path in the plist is absolute.
- **The log directory must exist** or the job refuses to start.
- **`KeepAlive` is `SuccessfulExit: false`, not `true`.** Plain `true` would respawn the daemon even
  after a deliberate clean stop. This is the "restart on crash, respect the exit" form.
- **`ThrottleInterval`** prevents a crash loop from becoming a spin.

**No plist generator subcommand.** It would be a second surface to keep in sync with §4, in exchange
for a one-time copy-paste. Document the plist; let `launchctl` do its job.

The canonical copy of the plist above lives at
[`docs/ops/launchd/com.seppaleinen.idle-deck.plist`](../ops/launchd/com.seppaleinen.idle-deck.plist),
with the install/uninstall commands and the four mechanical details spelled out in
[`docs/ops/launchd/README.md`](../ops/launchd/README.md).

## 4. Configuration

**Environment variables and CLI flags only. No YAML file in the MVP.**

D23 established "env vars, optional YAML file, CLI flags override". This is the *MVP scope* of D23:
the optional YAML file is **not built**. Nothing is superseded — D23's content is not falsified, it
is simply not yet implemented — but an implementation session should not add a config file parser
because a decision record mentions one.

A YAML file would be a second source of truth that can disagree with the first, and "which one won"
becomes a class of bug that env-only configuration cannot have. It returns when someone actually
wants it; the precedence chain below is already the shape it would slot into.

**Precedence: CLI flags > environment > built-in defaults.** Environment beats defaults so a
supervised daemon can be configured without a config file, and flags beat both so a developer can
override one value for one run without editing anything.

### Required — the daemon refuses to start without these

| Variable | Meaning |
|---|---|
| `IDLE_DECK_GITHUB_TOKEN` | Fine-grained PAT for the tracker, Issues read/write. Needs write because D29's label lifecycle writes labels. |
| `IDLE_DECK_REPOS` | Comma-separated `owner/repo` list. **This list is the allowlist** (D30), checked again at ingestion (I1). |
| `IDLE_DECK_HARNESS_URL` | Base URL of the remote execution service. |
| `IDLE_DECK_HARNESS_TOKEN` | Bearer token for that service (D27). |

### Defaults — every one of these can be left alone

| Variable | Default | Source |
|---|---|---|
| `IDLE_DECK_DB` | `~/Library/Application Support/idle-deck/idle-deck.db` | — |
| `IDLE_DECK_POLL_INTERVAL` | `60s` | D28 |
| `IDLE_DECK_MAX_CONCURRENT_JOBS` | `1` | D8 |
| `IDLE_DECK_LOG_LEVEL` | `info` | — |
| `IDLE_DECK_LOG_FORMAT` | `json` | — |
| `IDLE_DECK_TIER_TIMEOUT_PLAN` | `900` (15 min) | remote contract §8a |
| `IDLE_DECK_TIER_TIMEOUT_DO` | `10800` (3 hr) | remote contract §8b |
| `IDLE_DECK_TIER_TIMEOUT_SWEEP` | `3600` (1 hr) | remote contract §8c |
| `IDLE_DECK_TIER_TIMEOUT_HOTFIX` | `1800` (30 min) | D37 |
| `IDLE_DECK_BUDGET_PLAN` | `5000` | remote contract §8a |
| `IDLE_DECK_BUDGET_DO` | `100000` | remote contract §8b |
| `IDLE_DECK_BUDGET_SWEEP` | `20000` | remote contract §8c |
| `IDLE_DECK_BUDGET_HOTFIX` | `50000` | D37 |
| `IDLE_DECK_GITHUB_API` | `https://api.github.com` | also serves GitHub Enterprise |
| `IDLE_DECK_RETRY_BACKOFF_INITIAL` | `30s` | D37 |
| `IDLE_DECK_RETRY_BACKOFF_MAX` | `10m` | D37 |
| `IDLE_DECK_SWEEP_PERIOD` | `168h` (7 days) | D43; `0` disables sweeping entirely |
| `IDLE_DECK_SWEEP_REPOS` | *unset* = every allowlisted repo | D43; a narrowing subset, never a second allowlist |
| `IDLE_DECK_SWEEP_PROMPT` | the stale-TODO/FIXME default below | D43 |

`IDLE_DECK_SWEEP_PERIOD` is the **bucket period**, and it is operator-owned rather than a built-in
schedule. `0` is the global kill switch. There is no adaptive backoff: a sweep that finds nothing
every night does get muted, and that risk is answered by a config value the operator controls, not by
machinery whose behaviour cannot be observed from outside and which would make the dedupe key's period
change over time ([D43](../adr/0019-p3-sweep-policy.md)).

`IDLE_DECK_SWEEP_REPOS` is a **narrowing subset** of `IDLE_DECK_REPOS`, not a second allowlist. The
allowlist still governs *access* (D30/I1); this governs only *which allowed repositories also get P3*.
Unset means every allowlisted repository is swept — which is why "watched" and "swept" are the same
set by default and separable by choice, rather than being the same thing by construction.

`IDLE_DECK_SWEEP_PROMPT` is what the sweep looks for, and the shipped default is deliberately narrow:

> Scan the repository for `TODO` and `FIXME` markers. For each, report the file, line, the marker's
> age in days, and whether the surrounding file has changed more recently than the marker was added.
> Report only markers older than 90 days. Produce a report; make no changes.

Each candidate policy the sweep ticket considered — dependency drift, flakiness, coverage, open README
questions — is a **different job**, and "find problems in this repo" is not a job at all. The default
is a *finding*, not a *judgment*, because an unattended 20,000-token run whose output cannot be
checked against the repository is not auditable. The prompt is text idle-deck passes to the harness;
it **cannot** instruct a tracker write, because what a sweep may do about a finding is a rule idle-deck
enforces (D43), not something the prompt can grant.

`IDLE_DECK_GITHUB_API` is **not a test-only affordance**. Pointing the tracker adapter at a different
API base is exactly what GitHub Enterprise requires, so it earns its place on its own merits; §9 then
reuses it for free.

## 5. CLI surface

| Command | Purpose |
|---|---|
| `idle-deck run` | Run the daemon in the foreground. The only mode. |
| `idle-deck status` | Read local state and report what the daemon is doing. No daemon required to read, only to be interesting. |
| `idle-deck check` | Validate configuration and connectivity without starting anything. |
| `idle-deck --version` | Version. |

**Deliberately absent**, each rejected for a stated reason rather than by omission:

- **No `stop`.** `launchctl bootout` is the stop, and `Ctrl-C` is the stop in the foreground. A
  second stop path that can disagree with the supervisor is worse than none.
- **No `once`, and no cron mode.** Tempting on macOS, where cron could replace the daemon entirely.
  Rejected: it would give the lease, the heartbeat, and the watermark three different owners
  depending on invocation, and P0 preemption (D16) depends on a resident worker being there to
  receive the tick.
- **No `enqueue` and no CLI tier override.** #9 deferred a tier override here. Still deferred: a
  second ingestion path with its own provenance story, to buy a tier choice, when `idle-redo`
  already gives one click for "run it again". It returns when some tier cannot be expressed as a
  label.
- **No `migrate`.** Schema migrations run automatically at startup, forward-only, with no
  down-migrations. One user with one database does not need migration tooling.
- **No `queue ls`.** Queue inspection is part of `status` (§6). A separate subcommand would split
  one question — "what is idle-deck doing?" — across two commands.

### `idle-deck check`

Validates configuration *and* connectivity, because the failures it exists to catch are not
configuration failures:

1. Every required variable is present and non-empty (printing the **name**, never the value).
2. One authenticated read against the tracker — confirms the PAT is valid *and* has Issues read.
3. One `GET /health` against the harness — confirms the URL and the bearer token.
4. The SQLite path is writable, and the schema is current.
5. Which of the four D29 labels exist in each configured repository, and for any that are missing,
   the exact `gh label create` command to run.
6. Resolved configuration, with secrets redacted and non-secret values shown at their defaults.

## 6. Observability

**No HTTP endpoint, and no metrics exporter.** D28 established that idle-deck has no listening
socket; a localhost health endpoint would reintroduce exactly the surface #9 removed. Health is a
command that reads local state.

**Liveness without a socket** is a **heartbeat row in the SQLite state** the daemon already owns.
That choice buys staleness detection for free — a heartbeat with a timestamp answers both "is it
alive" and "how long has it been silent" — and it adds no new mechanism. The daemon beats on every
poll tick and on every state transition; `status` treats a heartbeat older than **three poll
intervals** as stale.

**Logs** are `log/slog` to stderr, JSON by default, `--log-format=text` for humans. Under
`launchd` these land in the `StandardErrorPath` file, which is the same structured stream — no
separate logging subsystem.

**`idle-deck status` reports:**

- heartbeat age, and whether the daemon holds the run lock
- queue depth broken down by tier and by state
- **last successful poll per repository, with its watermark** — the number that tells you whether
  ingestion is silently stuck
- the active task: its tier, role, attempt, and how long it has been running
- harness reachability, and the outcome of the most recent call
- the most recent escalation, if any

That last-poll-plus-watermark pair is the one an operator actually needs at 2am, because a daemon
that is up but not ingesting looks exactly like a healthy one without it.

## 7. Secrets

**The environment, and nothing else.** idle-deck reads tokens from the environment and has no
secrets-file reader. There is no `.env` support, on purpose: a program that silently reads a file
from the working directory is a surprise waiting to happen, and a secrets file is a second source of
truth that can disagree with the environment.

The ergonomics are not lost, because **the file is read by `launchd` or your shell, never by
idle-deck**. Either the token sits in the plist's `EnvironmentVariables` (the plist itself `chmod
600`), or a wrapper script exports it from a `0600` file. Either way idle-deck's own configuration
model stays single-source.

- **Never logged.** Redaction happens in the configuration layer, so no call site can leak a token
  by forgetting — rather than at each log statement. `check` and `status` print a variable's
  *name* and whether it is set, never its value.
- **Never written to SQLite.** The state file holds tracker references, prompts, and artifact
  pointers. No secret is among them.
- **Never committed.** No `.env` is read, so none can be accidentally created where a future
  `.gitignore` rule would matter.
- **Rotation is: rotate, restart.** No hot reload. This is a single-user tool, and a daemon
  holding a stale token for a week is not a failure mode worth a reload path.
- **A token that expires mid-run is an ordinary retryable failure.** D14 gives two automated retries
  and the #8 taxonomy maps auth failure accordingly, so a token rotated underneath a running task
  costs one retry, not the task.

**Deferred: the macOS Keychain.** It buys little here — `launchd` already holds the token in a
`0600` file — and it needs either a dependency or a `security`/`osascript` shim. It is the right
hardening if idle-deck ever serves anyone else.

## 8. Cost and tier policy

**`budget` is a generation-token ceiling.** The wire contract carried `"budget": 100000` with the
comment "ceiling the harness respects" and **never stated a unit**; three undefined numbers sat in
a locked document. Tokens are the natural currency for an LLM harness and match the magnitudes
already chosen (5,000 for a plan is a plausible plan; 100,000 a plausible feature). A currency
ceiling would be model-agnostic, which sounds better until you notice idle-deck never names a model
(D6), cannot know the remote's pricing, and so could neither choose a sensible value nor explain
one. idle-deck never interprets the budget beyond mapping `422 budget_exceeded` to
`non_retryable_failure`.

| Tier | Role | Timeout | Budget |
|---|---|---|---|
| P0 hotfix | `do` | 1800s (30 min) | 50,000 |
| P1 plan | `plan` | 900s (15 min) | 5,000 |
| P2 do | `do` | 10800s (3 hr) | 100,000 |
| P3 sweep | `sweep` | 3600s (1 hr) | 20,000 |

P0's numbers are the only ones this ticket added, and the reasoning is ordering, not measurement: a
hotfix longer than a sweep is a feature wearing a hotfix's label, and every second a P0 runs is a
second the P2 it displaced stays aborted. 30 minutes keeps P0 strictly under the sweep tier; 50,000
sits between a sweep and a feature, because a hotfix is real edit-and-verify work — more than a
scan-and-report, less than a feature. `TaskItem.timeout_seconds` still overrides the tier default
when set (D17).

**Retry backoff:** exponential from 30s, capped at 10 minutes, and `Retry-After` is honoured
whenever the remote sends it — which the #8 taxonomy already maps to `retryable_failure`. Two
retries total, then escalation (D14).

**No global spend ceiling, and that is not an oversight.** idle-deck cannot enforce a monthly cap
because the remote meters spend and idle-deck only ever sees an outcome. A ceiling it cannot
observe is a number in a config file that does nothing. It becomes decidable when the remote
reports usage; until then the per-run ceiling is the real control.

The GitHub side needs no policy at all: #9 measured ~1,200 requests/hour at 60s across five
repositories against a 5,000/hour budget, cut much lower by conditional requests.

## 9. Local development

**Run idle-deck end to end with no real GitHub and no real harness.**

#11 was written before #9 and asked for a "fake event injector", because it assumed webhooks. There
is nothing to inject. **Polling observes state, so the dev loop is stateless**: change the stub's
state and the next tick sees it. That deleted a whole category of dev tooling the ticket was
budgeting for, and it is the main dividend of D28.

**Both dependencies attach by base-URL override, not by a second implementation:**

- **Tracker stub** — an `httptest` server implementing the four queries the event contract actually
  uses, fixtures as JSON. Pointed at with `IDLE_DECK_GITHUB_API=http://127.0.0.1:PORT`.
- **Harness stub** — an `httptest` server implementing `POST /runs`, `GET /runs/{id}`,
  `DELETE /runs/{id}`, and `GET /health`, returning canned typed artifacts. Pointed at with
  `IDLE_DECK_HARNESS_URL`.

This exercises the **real adapters** — URL building, pagination, the `pull_request` filter,
conditional requests, watermark advancement, the wire outcome mapping — rather than a
`TrackerSource` implementation that only a developer ever runs and which therefore never tests the
seam it was built to test. A second `TrackerSource` was considered and rejected for exactly that
reason: it would be the second implementation [#12](https://github.com/seppaleinen/idle-deck/issues/12)
weighed, except one that exists only under a test — and that question is now answered (D40/D41,
[ADR 0018](../adr/0018-framework-agnostic-means-never-naming-a-vendor.md)).

```bash
IDLE_DECK_POLL_INTERVAL=2s \
IDLE_DECK_REPOS=acme/widgets \
IDLE_DECK_GITHUB_API=http://127.0.0.1:9099 \
IDLE_DECK_HARNESS_URL=http://127.0.0.1:9098 \
IDLE_DECK_HARNESS_TOKEN=stub \
IDLE_DECK_DB=/tmp/idle-deck-dev.db \
make dev
```

`make dev` starts both stubs and the daemon together. `make test` runs the suite. That is the whole
of the build tooling — Go needs none, so the Makefile is convenience only.

**A golden path** worth having as a fixture, because it exercises the tier machinery end to end: a
P1 issue appears → a `plan` run returns a `comment` artifact → the operator applies `idle-ready` →
a P2 run returns a `pull_request` artifact and the trigger label is removed → the run escalates, so
`idle-needs-human` is applied and a debug branch preserved → `idle-redo` produces a new task with
`derived_from` set and a fresh retry budget.

Two notes on what the stubs do and do not prove.

**The harness stub speaks the same wire contract as a real remote service, by construction — so it is
the same execution model, not a different one.** It exercises the real adapter (URL building, status
polling, abort, the outcome→`AttemptOutcome` mapping) against a server it controls the answers of. It
is evidence that the adapter is correct; it is **not** evidence that a second, structurally different
implementation would fit the `Harness` interface, which is what
[#12](https://github.com/seppaleinen/idle-deck/issues/12) was about. That gap is closed by the
**conformance suite** (D41), not by a second stub — see §11.

And the `since`-composes-with-`labels` assumption #9 flagged can be exercised against a stub —
though what that verifies is the *adapter's* handling of the behaviour, not GitHub's, which is why
#10's adapter spec still owes a real check.

## 10. CI

The repository had **no `.github` directory at all** when this was decided, so there was no CI.

| Gate | Why |
|---|---|
| `go build ./...` | It compiles. |
| `go vet ./...` | The cheap correctness pass. |
| `go test ./...` | The suite, including the stub-backed integration tests. |
| **D-id verification** | AGENTS.md *mandates* this before any push touching `AGENTS.md` or `docs/adr/`. It has been running by hand; CI is where it cannot be forgotten. |
| **Smoke test** | The binary runs `--version` and `check` with **no configuration present**, and `run` refuses to start with a clear error naming the missing variables. This is what proves §4's "refuses to start cleanly" actually holds. |

**`golangci-lint` is deferred.** It is a versioned external toolchain that will churn, and `go vet`
covers the MVP. Pinning it from day one is a reasonable thing to want and an unreasonable thing to
adopt before there is code to lint.

## 11. What this document deliberately does not decide

- **A global spend ceiling** — impossible until the remote reports usage (§8).
- **Release binaries, Homebrew, container images** — deferred until there is a second user (§2).
- **The macOS Keychain** — hardening for a future multi-user idle-deck (§7).
- **A YAML config file** — D23 permits it; the MVP does not build it (§4).
- **A CLI tier override for `idle-redo`** — still deferred; `idle-redo` is the affordance (§5).
- **What a P3 sweep looks for** — the *mechanism* is decided ([D43](../adr/0019-p3-sweep-policy.md)):
  `IDLE_DECK_SWEEP_PROMPT`, with a narrow shipped default. The *content* is the operator's, by
  construction — there is no built-in policy catalogue, and adding one would be a new decision.
- **Live cancellation and a `stop`/`requeue` surface** — post-MVP by the ontology's own cut.
- **A second harness adapter, and SSE** — the MVP ships one adapter and makes no pluggability claim
  (D40). A second is gated on a named trigger list, and the **conformance suite** (D41) is what
  obliges an adapter to exist at all. Neither is on the operator surface: they are harness-contract
  concerns, specified in [`docs/architecture/remote-contract.md`](../architecture/remote-contract.md)
  and decided by [ADR 0018](../adr/0018-framework-agnostic-means-never-naming-a-vendor.md).

## 12. Cross-references

- Domain model: [`docs/ontology.md`](../ontology.md) — entities, I1, I7, I9, I10, I11
- Interface seams: [`docs/architecture/boundaries.md`](../architecture/boundaries.md) — the four interfaces
- Event contract: [`docs/architecture/event-contract.md`](../architecture/event-contract.md) — triggers, labels, the query set
- Remote execution service: [`docs/architecture/remote-contract.md`](../architecture/remote-contract.md) — `/health`, `budget`, timeouts
- This decision: [ADR 0016](../adr/0016-idle-probed-at-the-harness.md), [ADR 0017](../adr/0017-the-artifact.md) — D31–D39
- Harness seam: [ADR 0018](../adr/0018-framework-agnostic-means-never-naming-a-vendor.md) — D40, D41
- Map: [#3](https://github.com/seppaleinen/idle-deck/issues/3) · Backlog: [#10](https://github.com/seppaleinen/idle-deck/issues/10)
