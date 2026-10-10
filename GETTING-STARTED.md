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

## Running with a real harness (remote wrapper)

To use a real agent (e.g., opencode) as the harness, you need to run the remote wrapper server that executes the agent and speaks idle-deck's wire contract.

### Build the remote server

```bash
go build -o idle-deck-remote ./cmd/remote/
```

### Configure and run the remote server

Set the following environment variables:

| Variable | Purpose |
|---|---|
| `IDLE_DECK_REMOTE_BARE_DIR` | Directory for bare repo cache (e.g. `/tmp/idle-deck-bare`) |
| `IDLE_DECK_REMOTE_BASE_DIR` | Directory for worktrees (e.g. `/tmp/idle-deck-work`) |
| `IDLE_DECK_REMOTE_GIT_TOKEN` | **Repo-scoped** personal access token for git operations (push, PR creation). Needs `Contents: Read and write` + `Metadata: Read-only`. |
| `IDLE_DECK_REMOTE_HTTP_TOKEN` | Bearer token for the HTTP server (must match what idle-deck sends). For local dev you can set any non-empty string. |
| `IDLE_DECK_REMOTE_ROLE_TO_MODEL` | JSON mapping of role to model name, e.g. `'{"plan":"opencode/big-pickle","do":"opencode/big-pickle","sweep":"opencode/big-pickle"}'` |

Then start the server:

```bash
IDLE_DECK_REMOTE_BARE_DIR=/tmp/idle-deck-bare \
IDLE_DECK_REMOTE_BASE_DIR=/tmp/idle-deck-work \
IDLE_DECK_REMOTE_GIT_TOKEN=ghp_... \
IDLE_DECK_REMOTE_HTTP_TOKEN=secret123 \
IDLE_DECK_REMOTE_ROLE_TO_MODEL='{"plan":"opencode/big-pickle","do":"opencode/big-pickle","sweep":"opencode/big-pickle"}' \
./idle-deck-remote
```

The server will listen on the address specified by `IDLE_DECK_REMOTE_LISTEN_ADDR` (default `:8080`).

### Configure idle-deck to point to the real harness

Set `IDLE_DECK_HARNESS_URL` to the server's base URL (e.g. `http://127.0.0.1:8080`) and `IDLE_DECK_HARNESS_TOKEN` to the HTTP token you set above.

Example full environment for a local test with a real opencode agent:

```bash
IDLE_DECK_POLL_INTERVAL=10s \
IDLE_DECK_REPOS=yourname/yourrepo \
IDLE_DECK_GITHUB_TOKEN=ghp_... # fine-grained PAT for issues/comments \
IDLE_DECK_HARNESS_URL=http://127.0.0.1:8080 \
IDLE_DECK_HARNESS_TOKEN=secret123 \
IDLE_DECK_REMOTE_BARE_DIR=/tmp/idle-deck-bare \
IDLE_DECK_REMOTE_BASE_DIR=/tmp/idle-deck-work \
IDLE_DECK_REMOTE_GIT_TOKEN=ghp_... # repo-scoped PAT for git \
IDLE_DECK_REMOTE_HTTP_TOKEN=secret123 \
IDLE_DECK_REMOTE_ROLE_TO_MODEL='{"plan":"opencode/big-pickle","do":"opencode/big-pickle","sweep":"opencode/big-pickle"}' \
IDLE_DECK_DB=/tmp/idle-deck-real.db \
idle-deck run
```

> **Note**: The remote server requires a working opencode installation (`opencode` on PATH) and a gitHub token with repo scope for git operations. The `IDLE_DECK_GITHUB_TOKEN` is still needed for idle-deck's own issue/comment traffic.


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
