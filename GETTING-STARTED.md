# idle-deck — MVP Capabilities & Getting Started

## What it is

idle-deck is an **orchestration daemon** that watches GitHub issues and runs agent
work in the background while you do other things. It does not host models itself — it
coordinates a remote execution service (the harness) that does the actual work.

## What the MVP can do

### Watch repos for trigger conditions (polling, every 60s)

- `idle-hotfix` label → **P0 hotfix** — runs immediately, preempts any P2 in progress
- New issue → **P1 plan** — posts a clarification checklist comment
- `idle-ready` label → **P2 feature** — runs the implementation, produces a Draft PR
- Local scheduler (idle capacity) → **P3 sweep** — scans for stale TODO/FIXME markers,
  files one issue per finding labelled `idle-needs-human` (never opens a PR itself)

### Priority queue

SQLite-backed, ordered by tier. Dequeue is a lease with expiry — if the daemon crashes,
the abandoned run is reclaimed after the lease timeout. Two automated retries per task,
then escalation.

### Idle detection

Asks the harness's `GET /health` whether the model is reachable (not process liveness).
If it answers, the engine is idle and work proceeds.

### CLI

```
idle-deck run         # daemon in the foreground
idle-deck status      # read local state
idle-deck check       # validate config + connectivity
idle-deck --version   # version
```

## Install

```bash
go install github.com/seppaleinen/idle-deck@v0.1.0
```

Static, cgo-free binary — no C toolchain needed.

## Configuration

All env vars, no config file:

| Variable | Purpose |
|---|---|
| `IDLE_DECK_GITHUB_TOKEN` | PAT for GitHub (Issues read/write) |
| `IDLE_DECK_REPOS` | Comma-separated `owner/repo` allowlist |
| `IDLE_DECK_HARNESS_URL` | Remote execution service URL |
| `IDLE_DECK_HARNESS_TOKEN` | Bearer token for the harness |

Everything else has safe defaults (poll interval 60s, budget, timeouts, sweep period 7d).

## Try it locally (stubs, no real GitHub/harness needed)

```bash
IDLE_DECK_POLL_INTERVAL=2s \
IDLE_DECK_REPOS=acme/widgets \
IDLE_DECK_GITHUB_API=http://127.0.0.1:9099 \
IDLE_DECK_HARNESS_URL=http://127.0.0.1:9098 \
IDLE_DECK_HARNESS_TOKEN=stub \
IDLE_DECK_DB=/tmp/idle-deck-dev.db \
make dev
```

`make dev` starts the stubs and the daemon together. `make test` runs the suite.

## Supervised (launchd)

```bash
mkdir -p ~/Library/Logs/idle-deck
cp docs/ops/launchd/com.seppaleinen.idle-deck.plist ~/Library/LaunchAgents/
chmod 600 ~/Library/LaunchAgents/com.seppaleinen.idle-deck.plist
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.seppaleinen.idle-deck.plist
```

## The workflow, in practice

1. Open a GitHub issue
2. idle-deck P1 posts a clarification checklist
3. You answer, apply `idle-ready`
4. idle-deck P2 runs the implementation, opens a Draft PR
5. You review it. Changes? Apply `idle-redo` for a fresh retry
6. If it fails three times, you get `idle-needs-human` + a debug branch

## Verify it works

```bash
idle-deck --version          # → idle-deck v0.1.0
idle-deck check              # validates config + connectivity
go test ./...                # 75 tests pass
```
