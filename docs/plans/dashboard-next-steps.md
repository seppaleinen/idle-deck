# Implementation Plan for Dashboard Next Steps

## 1. Objective & Scope

The dashboard (`idle-deck serve`) is read-only today. This plan covers the
**next layer of observability and action** — turning it from a monitoring
panel into an operator control surface, without breaking the read-only MVP
contract.

Three work items, in priority order:

1. **`/api/tasks` endpoint** — full task listing with pagination and filters.
   The dashboard currently shows only aggregate counts and recent attempts;
   an operator troubleshooting a specific task needs to find it.
2. **Dashboard actions** — trigger a poll, cancel a running task, retry a
   failed task. These are HTTP endpoints that proxy to the daemon's existing
   seams (tracker poller, queue, worker). Read-only MVP stays intact; actions
   are opt-in and gated behind a flag.
3. **Dashboard docs in `GETTING-STARTED.md`** — screenshots, example output,
   and a note that `serve` is a fourth CLI command (D33 surface grows).

## 2. Work Items in Execution Order

### 2.1 `/api/tasks` endpoint

**Files:**
- `dashboard/handlers.go` — add `handleTasks`

**Done-when:**
- `GET /api/tasks?limit=50&offset=0&state=running&tier=P2` returns paginated
  tasks with their latest attempt outcome, ticket URL, and elapsed time.
- Empty queue returns `[]`, never `null`.
- Invalid `state`/`tier` returns `400`.

### 2.2 Dashboard actions

**Files:**
- `dashboard/server.go` — add `ActionHandler` field, wire `/api/actions/*`
- `dashboard/handlers.go` — add `handleActionPoll`, `handleActionCancel`,
  `handleActionRetry`
- `cmd/idle-deck/main.go` — pass an action bus to the dashboard server

**Done-when:**
- `POST /api/actions/poll` with `{"repo":"owner/repo"}` triggers one poll tick.
- `POST /api/actions/cancel` with `{"task_id":"..."}` aborts the running task.
- `POST /api/actions/retry` with `{"task_id":"..."}` re-enqueues the task.
- All actions require the daemon to be running (they proxy through a
  lightweight HTTP bridge or a shared channel). If the daemon is down, the
  action returns `503`.
- Actions are **disabled by default**; enabled via `IDLE_DECK_DASHBOARD_ACTIONS=1`.

### 2.3 Dashboard docs

**Files:**
- `GETTING-STARTED.md` — add a "Dashboard" section

**Done-when:**
- Section explains `idle-deck serve`, the default address, the env var to
  change it, and what each panel shows.
- Notes that `serve` is a fourth CLI command (D33 surface grows by one).
- Includes a note that actions are opt-in and disabled by default.

## 3. Out of Scope

- Authentication / multi-user. The dashboard stays local-only (D4).
- Real-time updates. Polling at 5s is sufficient for the MVP.
- A second `Harness` implementation. The dashboard probes the existing
  harness; it does not manage harnesses.