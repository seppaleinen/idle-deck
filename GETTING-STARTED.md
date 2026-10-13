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

## Dashboard (read-only web UI + actions)

idle-deck includes a built-in dashboard server for observability and
operator control. It runs on `127.0.0.1:8090` by default and serves
an embedded single-page app plus a JSON API.

### Start the dashboard

```bash
idle-deck serve
```

Optional env vars / flags:

| Variable | Flag | Default | Purpose |
|---|---|---|---|
| `IDLE_DECK_DASHBOARD_ADDR` | `--dashboard-addr` | `127.0.0.1:8090` | Listen address |
| `IDLE_DECK_DASHBOARD_ACTIONS` | `--dashboard-actions` | `false` | Enable write actions (poll/cancel/retry) |

The dashboard reads the same SQLite database as the daemon (read-only,
WAL mode). It requires no auth — local-only by design (D4).

### Dashboard UI

Open `http://127.0.0.1:8090/` in a browser. Panels:

- **Heartbeat** — daemon liveness (beat age, stale flag) + run lock holder
- **Harness** — live `GET /health` probe (reachable, status code, body)
- **Repositories** — configured repos, watermark, last poll time
- **Queue** — task counts by state (queued/running/succeeded/escalated) and tier
- **Active task** — running task ID, tier, role, attempt, elapsed time
- **Recent attempts** — last 50 attempts with outcome, timestamps, ticket link
- **Recent artifacts** — branches, PRs, comments, reports produced
- **Configuration** — redacted view of all resolved config (secrets hidden)

The UI auto-refreshes every 5 seconds.

### JSON API

All endpoints return JSON; `[]` for empty collections (never `null`).

| Endpoint | Description |
|---|---|
| `GET /api/status` | Single "everything" payload (used by UI) |
| `GET /api/health` | Dashboard liveness (`{"status":"ok"}`) |
| `GET /api/repos` | Per-repo watermark + last poll |
| `GET /api/queue` | Queue depth by state+tier, totals by state |
| `GET /api/tasks?limit=50&offset=0&state=running&tier=P2` | Paginated task list with latest attempt |
| `GET /api/attempts` | Last 50 attempts with outcomes |
| `GET /api/artifacts` | Last 100 artifacts across all attempts |
| `GET /api/heartbeat` | Beat time, stale flag, run lock |
| `GET /api/config` | Full redacted config |

#### Actions (opt-in, requires `IDLE_DECK_DASHBOARD_ACTIONS=true`)

Actions are written to the `dashboard_actions` table and processed
by the daemon's poller on its next tick (every 60s by default).
The HTTP call returns immediately with an `action_id`; the action
is executed asynchronously.

| Endpoint | Payload | Effect |
|---|---|---|
| `POST /api/actions/poll` | `{"repo":"owner/repo"}` | Trigger a poll for that repo |
| `POST /api/actions/cancel` | `{"task_id":"..."}` | Mark running task as escalated |
| `POST /api/actions/retry` | `{"task_id":"..."}` | Re-enqueue task for another attempt |

Example:

```bash
# Enable actions
IDLE_DECK_DASHBOARD_ACTIONS=true idle-deck serve

# In another terminal, trigger a poll
curl -X POST http://127.0.0.1:8090/api/actions/poll \
  -H "Content-Type: application/json" \
  -d '{"repo":"seppaleinen/idle-deck"}'
```

### Notes

- The `serve` command is a **fourth CLI command** (D33 surface grows by one).
  The MVP stated `run`/`status`/`check`/`--version`; `serve` is an explicit
  extension for observability.
- Actions are **disabled by default** — they require opt-in via the env var
  or flag. This preserves the read-only MVP contract.
- The dashboard and daemon share the same SQLite database. The dashboard
  opens it read-only (`_mode=ro`) so concurrent reads don't block the
  daemon's writes.

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