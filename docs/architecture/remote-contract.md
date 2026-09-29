# idle-deck ↔ remote execution service: wire contract

The resolution of [Remote execution service contract #8](https://github.com/seppaleinen/idle-deck/issues/8).
This is the **prototype artifact**: the concrete request/response contract under the locked `Harness`
interface ([ADR 0013](../adr/0013-harness-lifecycle.md), D26). It pins, in order of consequence:
where the workspace lives, transport/auth, idempotency, cancellation, progress, result typing, the
error taxonomy, and one worked example per result shape.

**Status:** draft-as-decision (prototype ticket #8). It is binding for the MVP harness adapter
(backlog ticket #10); a deviation is a new decision.

---

## 0. Workspace ownership — the root answer

**The workspace lives on the remote execution service.** The remote owns the checkout, the working
tree, the git operations, and the Draft PR production. idle-deck never touches the remote filesystem
and never performs git through this contract; it sends a prompt plus refs and receives **typed
artifacts as data**.

Everything else in this document follows from that single fact:

- The remote is reponsible for the working tree state that [D21](../../AGENTS.md) guarantees:
  a successful run pushes `feature/<ticket_id>-<slug>` and opens a Draft PR (I4 — the `pull_request`
  artifact); a failed/timed-out run preserves `debug/<task_id>` (I5 — the `branch` artifact with
  `branch_purpose = debug`).
- idle-deck's worker enforces I4/I5 from the *returned artifacts*, never from filesystem knowledge —
  consistent with ADR 0013's "the harness is not asked to do git *through the interface*": the git
  happens remote-side and its outputs arrive as data.
- Because the remote owns the process, the remote also owns the **timeout enforcement** (D17): it
  kills its own run at the deadline even if idle-deck dies first. No stranded billing.
- I8's `remote_session_id` is the remote's `run_id`; cancellation is a wire call (section 4).

---

## 1. Transport and authentication

- **Transport:** HTTPS/TLS only. The server refuses plaintext HTTP. `base_url` is operator config
  (names belong to [Install, run, configure #11](https://github.com/seppaleinen/idle-deck/issues/11)).
- **Client → server auth:** `Authorization: Bearer <token>` on every request. The token is
  operator-issued, never logged, never committed (D23).
- **Server → GitHub auth:** the remote holds its own **repo-scoped GitHub token**, configured by the
  operator. It is the remote's secret, not idle-deck's.
- **No inbound calls.** The remote never calls back into idle-deck. Every interaction is
  idle-deck-initiated (poll/pull). This keeps NAT/firewall trivial and answers "does the remote
  authenticate back?" structurally: there is no remote-initiated call to authenticate.
- **Content type:** `application/json` for requests and (non-log) responses. Log chunks are JSON
  arrays of strings.

> Token rotation, scoping mid-run, and expiry behavior are the **secrets lifecycle** fog item and
> belong on the operator surface (#11). This contract pins the *shape* only.

---

## 2. Endpoints

All under `{base_url}/v1`.

| Method & path | Maps to (`Harness`) | Purpose |
|---|---|---|
| `POST /runs` | `Start` | Create a run (idempotent by `attempt_id`). |
| `GET /runs/{id}` | `Result` (adapter polls internally) | Status while running; terminal result + artifacts when done. |
| `DELETE /runs/{id}` | `Abort` | Cancel a running run; `204` only when confirmed dead. |
| `GET /health` | `IdlePolicy` probe (D5) | If it answers, the engine is considered idle. |

---

## 3. `POST /runs` — Start

Request body:

```json
{
  "attempt_id": "c73d8f2a-...",        // idempotency key = TaskAttempt.id
  "role": "do",                        // plan | do | sweep — NEVER a model name (D6, I11)
  "prompt": "Implement ... and open a PR.",
  "budget": 100000,                    // ceiling the harness respects (field travels; policy is fog)
  "timeout_seconds": 10800,            // server enforces this (D17); tier default comes from idle-deck
  "repository": {
    "id": "repo-1",
    "name": "owner/awesome",
    "url": "https://github.com/owner/awesome.git",
    "tracker": "github"
  },
  "ticket": {
    "tracker": "github",
    "external_id": "77",
    "url": "https://github.com/owner/awesome/issues/77"
  }
}
```

(Shape mirrors the locked `RunRequest` in [`boundaries.md`](boundaries.md); note `Allowlisted` is not
sent — I1 is enforced by `TrackerSource.Parse` at ingestion, and the remote only ever receives a
repo it was already told to accept.)

Responses:

| Status | Meaning |
|---|---|
| `201 Created` | Run started. Body: `{ "run_id": "..." , "status": "running" }` |
| `200 OK` | **Idempotent duplicate** — this `attempt_id` is already known. Body: the *existing* `run_id` and its current status. **No new run is created.** |
| `422 Unprocessable` | `budget_exceeded` — the remote refuses the ceiling (section 7). |
| `401/403` | Auth failure. Retrying will not help (section 7). |

**Idempotency guarantee (the retry-safety story):** the server keys runs by `attempt_id`. A retried
`Start` after a lost ACK returns the same `run_id` (200), never a second execution (D14's retries and
D24's lease expiry both rely on this). This is **orthogonal to** `TriggeredBy.dedupe_key`, which
handles *event redelivery* inside the queue — #9's job. The server keeps idempotency state for a
grace period covering worst-case retry + lease expiry; a `Start` beyond it is a genuinely new run
(matching a genuinely new attempt, which gets a fresh `attempt_id`).

---

## 4. `DELETE /runs/{id}` — Abort (cancellation)

- Success is `204 No Content`, returned **only when the remote side has confirmed cancellation** (or
  the run was already terminal — Abort is idempotent). This fulfils ADR 0013's "`Abort` returns only
  when the remote side confirms cancellation (or has already completed)" and I8.
- **Server-side backstop:** independent of any `DELETE`, the server enforces `timeout_seconds` from
  the `Start` request. On expiry it kills the run and records `timed_out`. A dead idle-deck cannot
  leave a billed run orbiting (the ticket's "tokens burning on someone else's hardware" fear).

---

## 5. Progress — `GET /runs/{id}` while running

The adapter keeps `Harness.Result` blocking from the worker's view by **polling a status endpoint**
internally. No SSE in the MVP.

| Response (while running) | Meaning |
|---|---|
| `200` `{ "status": "running", "last_activity_at": "...", "next_log_offset": 0, "logs": ["..."] }` | Alive signal: a 3-hour P2 is verifiably not hung. `logs` returns **incremental** chunks (client passes `?log_offset=`). |
| `404` | Unknown run id (non-retryable — see section 7). |

The worker/operator gets liveness; the `log` artifact can be assembled incrementally. SSE is the
documented v2 path when [#12](https://github.com/seppaleinen/idle-deck/issues/12) re-examines the
adapter — the locked interface does not foreclose it.

---

## 6. Result typing — `GET /runs/{id}` when terminal

`200` with a **typed** result. The outcome vocabulary is the server's; artifacts use exactly the
locked `ArtifactKind` (`comment` / `pull_request` / `branch` / `report` / `log`).

```json
{
  "status": "terminal",
  "outcome": {
    "state": "completed",             // completed | failed | timed_out | cancelled
    "code": null,                     // set when state = failed; e.g. "test_failure", "agent_error"
    "message": ""
  },
  "artifacts": [
    { "kind": "pull_request", "uri": "https://github.com/owner/awesome/pull/89", "branch_purpose": null },
    { "kind": "branch", "branch_purpose": "feature", "uri": "https://github.com/owner/awesome/tree/feature/77-add-cache" },
    { "kind": "log", "uri": "artifact-store/run-xyz/run.log", "branch_purpose": null }
  ]
}
```

Rules:

- `branch_purpose` is only meaningful when `kind = branch` (`feature` or `debug`, per the ontology).
- `comment` artifacts carry an additional `body` field (the comment text — idle-deck posts it via
  `TrackerSink.Comment`, it does not re-write it).
- **Typed, not opaque:** idle-deck never parses prose. I4 ("a succeeded P2 attempt has a
  `pull_request` artifact") is checkable structurally.
- `timed_out`/`cancelled` runs carry whatever artifacts the remote could preserve before stopping
  (typically `log`, plus `branch`+debug on a failed attempt that crosses the escalation threshold,
  I5).

**Wire outcome → `AttemptOutcome` mapping (done in the adapter):**

| Wire `outcome.state` | `AttemptOutcome` | Notes |
|---|---|---|
| `completed` | `succeeded` | Worker `Ack`s. |
| `failed` (+ `code`) | `retryable_failure` / `non_retryable_failure` per code | See section 7. |
| `timed_out` | `timeout` | Retryable (D17). |
| `cancelled` | `preempted` — **client-side steering** | The worker aborted, so it already knows why (D16) and records `preempted` via `Nack` (I7). The server only ever says `cancelled`. |

---

## 7. Error taxonomy

The contract's failure vocabulary must land on the locked `AttemptOutcome`, because retry policy
(D14) operates on that enum. `POST /runs` and `GET /runs/{id}` share one error body shape:

```json
{
  "error": {
    "code": "unauthorized",
    "message": "human-readable detail for the escalation comment"
  }
}
```

| Wire condition | Maps to attempt outcome / behavior |
|---|---|
| `400`, `401`, `403`, `404` (on `POST`) | `non_retryable_failure` — configuration or bug; retrying won't fix it. |
| idempotent duplicate (`200` on `POST`) | **already-started path** — same `run_id`, no new execution. |
| `422` `budget_exceeded` | `non_retryable_failure` — the ceiling was refused; retry won't help. Ceiling *policy* (values, backoff) stays fog #2 on the operator surface (#11). |
| `408`, `429` (+ `Retry-After`) | `retryable_failure` — transient overload. Backoff *schedule* is policy (fog #2). |
| `5xx` | `retryable_failure` — server error. |
| network error / server unreachable | `retryable_failure` — from the adapter's perspective. |
| `404` on `GET /runs/{id}` / `DELETE` | `non_retryable_failure` — unknown run id implies lost state; escalating beats looping. |
| wire `outcome.state = failed` + `code` | retryable/non-retryable per code: environment-class codes (`agent_error`, server-internal) are retryable; task-class codes (`test_failure`, `budget_exceeded`) are non-retryable. The **exact code list is part of the harness adapter spec** in #10, not this contract. |

Two deliberate boundaries: **429/budget policy** (how many times, how fast) is not decided here — this
contract pins *which condition means what*, not the schedule. And **`preempted` never appears on the
wire** — the server only reports `cancelled`; the steered outcome is the adapter's, because only the
worker knows the abort reason.

---

## 8. Worked examples

### 8a. P1 — spec clarification → `comment` artifact

A new issue is created → P1, `role: plan`, default timeout 900s (15 min).

```http
POST /v1/runs
Authorization: Bearer <idle-deck-token>
Content-Type: application/json

{
  "attempt_id": "a1b2c3d4-1111-...",
  "role": "plan",
  "prompt": "Read issue #42 and reply with a clarifying checklist: what is being asked, what is ambiguous, what blockers exist.",
  "budget": 5000,
  "timeout_seconds": 900,
  "repository": { "id": "repo-1", "name": "owner/awesome", "url": "https://github.com/owner/awesome.git", "tracker": "github" },
  "ticket": { "tracker": "github", "external_id": "42", "url": "https://github.com/owner/awesome/issues/42" }
}
```

```http
201 Created
{ "run_id": "run-abc", "status": "running" }
```

After a few minutes the adapter's poll sees:

```http
200 OK
{
  "status": "terminal",
  "outcome": { "state": "completed", "code": null, "message": "" },
  "artifacts": [
    { "kind": "comment", "uri": "https://github.com/owner/awesome/issues/42#issuecomment-123456", "body": "Clarifying questions:\n1. ...\n2. ..." },
    { "kind": "log", "uri": "artifact-store/run-abc/run.log" }
  ]
}
```

Worker: outcome → `succeeded`, `Ack`; posts the comment body via `TrackerSink.Comment` (or the
comment artifact's uri is recorded as written). I4 doesn't apply (P1, not P2).

### 8b. P2 — feature implementation → `pull_request` artifact

`status: ready` label applied → P2, `role: do`, default timeout 10800s (3 hr).

```http
POST /v1/runs
{
  "attempt_id": "c73d8f2a-2222-...",
  "role": "do",
  "prompt": "Implement server-side caching for issue #77 and open a Draft PR.",
  "budget": 100000,
  "timeout_seconds": 10800,
  "repository": { "id": "repo-1", "name": "owner/awesome", "url": "https://github.com/owner/awesome.git", "tracker": "github" },
  "ticket": { "tracker": "github", "external_id": "77", "url": "https://github.com/owner/awesome/issues/77" }
}
```

The run takes ~2h. The adapter's `GET /runs/run-xyz` polls report `last_activity_at` advancing, so
it's provably not hung. At the end:

```http
200 OK
{
  "status": "terminal",
  "outcome": { "state": "completed", "code": null, "message": "" },
  "artifacts": [
    { "kind": "branch", "branch_purpose": "feature", "uri": "https://github.com/owner/awesome/tree/feature/77-add-cache" },
    { "kind": "pull_request", "uri": "https://github.com/owner/awesome/pull/89" },
    { "kind": "log", "uri": "artifact-store/run-xyz/run.log" }
  ]
}
```

Worker: outcome → `succeeded`, `Ack`. **I4 is satisfied structurally** — the run has a
`pull_request` artifact. The Draft PR was opened remotely (D22: never merges; idle-deck stops).

### 8c. P3 — idle sweep → `report` artifact, and preemption

A P3 sweep runs (`role: sweep`) looking for, e.g., stale TODOs per the (still foggy) sweep policy.
Then a P0 hotfix arrives.

```http
POST /v1/runs
{
  "attempt_id": "e9f8a7b6-3333-...",
  "role": "sweep",
  "prompt": "Scan the repo for TODO/FIXME markers that are stale and summarize in a report.",
  "budget": 20000,
  "timeout_seconds": 3600,
  "repository": { "id": "repo-1", "name": "owner/awesome", "url": "https://github.com/owner/awesome.git", "tracker": "github" },
  "ticket": { "tracker": "github", "external_id": "101", "url": "https://github.com/owner/awesome/issues/101" }
}
```

It completes quietly:

```http
200 OK
{
  "status": "terminal",
  "outcome": { "state": "completed", "code": null, "message": "" },
  "artifacts": [
    { "kind": "report", "uri": "artifact-store/run-sweep/report.md" },
    { "kind": "log", "uri": "artifact-store/run-sweep/run.log" }
  ]
}
```

**Preemption (D16/I7) — the same sweep, interrupted** by a P0 above it in the queue. The worker
selects on `Queue.Events`, calls `Abort`, then wraps up:

```http
DELETE /v1/runs/run-sweep
204 No Content          // the remote has confirmed the sweep process is dead
```

Polling afterwards (or the value `Result` returns) shows:

```http
200 OK
{
  "status": "terminal",
  "outcome": { "state": "cancelled", "code": null, "message": "aborted by client" },
  "artifacts": [ { "kind": "log", "uri": "artifact-store/run-sweep/partial.log" } ]
}
```

Worker: wire `cancelled` → **steered** to `preempted` in the adapter, `Nack(lease, preempted)` (I7:
no timeout consumed, retryable, no burn of the retry budget). The P0 run then takes the slot.

---

## 9. What this contract deliberately does not decide

- **Cost/rate-limit policy** (ceilings, backoff schedule) — fog onto #11.
- **Secrets lifecycle** (rotation, scoping, mid-run expiry) — fog onto #11.
- **Sandboxing mechanism** — the remote must confine execution to the checked-out repo; *how* is the
  remote's implementation, not a wire concern.
- **SSE streaming** — documented v2 path, deferred (Q5).
- **The exact failure-code list and backoff numbers** — harness-adapter spec in #10.

---

## 10. Cross-references

- Interface: [`boundaries.md`](boundaries.md) — `Harness` seam (`Start`/`Abort`/`Result`), `RunRequest`, `RunResult`, worker loop.
- Decision: [ADR 0014](../adr/0014-remote-execution-service-contract.md), [D27](../../AGENTS.md).
- Upstream: [ADR 0013](../adr/0013-harness-lifecycle.md) (D26), [ADR 0005](../adr/0005-one-harness-adapter.md) (D7), [ADR 0006](../adr/0006-models-on-a-separate-server.md) (D20), [ADR 0003](../adr/0003-idle-is-a-probe.md) (D5).
- Ontology: [`../ontology.md`](../ontology.md) — `ArtifactKind`, `AttemptOutcome`, I4, I5, I7, I8, I11.