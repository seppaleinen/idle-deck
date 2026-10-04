---
Title:   P3 sweep typed findings + TrackerSink.OpenIssue
Status:  accepted
Supersedes: 0019
Related: D43, D44, D45, D26, D27, D42, remote-contract.md §6, boundaries.md §1
Source:  #21 Decision: how does a P3 sweep file one issue per finding?
---

## Decision

The `report` artifact is changed from an opaque URI to a structured payload
containing an array of `finding` objects. Each finding has a `title`, `description`,
`category`, and optional `severity`. `TrackerSink` gains a new method
`OpenIssue(ctx, repo, title, body, labels)` that idle-deck uses to file one
issue per finding via the ordinary ingestion path.

The sweep remains a reporter, not an author: it produces the structured
`report` artifact, and idle-deck's core files the issues.

## Motivation

The locked documents already contained this, unremarked: the sweep example returned
`{"kind": "report", "uri": "artifact-store/run-sweep/report.md"}` — a URI, with no inline body.
`Harness` is `Start`/`Abort`/`Result` (D26, no fetch), the workspace is on the remote
(D27), and `status` reports heartbeat, watermarks, queue depth, and the active task —
never artifacts. So a report-only sweep spent 20,000 tokens writing a file that
idle-deck could neither retrieve nor show.

This is why D43 is "file an issue" and not "return a report": the destination
was the real question; "what may it do about a finding" is unanswerable until
"does the human ever see it" is answered, and the locked documents said *no*.

## Changed contract docs

### `remote-contract.md` §6 — `report` artifact gains structured findings

The `report` artifact now carries a `Findings` body instead of just a URI:

```diff
 type ReportArtifact struct {
 	URI           string
+	Findings      []Finding
 }
```

The `Finding` type is new:

```go
type Finding struct {
	Title       string
	Description string
	Category    string   // e.g. "stale-TODO", "coverage-gap", "dependency-drift"
	Severity    string   // optional: "low" | "medium" | "high"
}
```

### `boundaries.md` §1 — `TrackerSink` gains `OpenIssue`

```diff
 type TrackerSink interface {
 	Comment(ctx context.Context, ref TrackerRef, body string) error
 	SetLabels(ctx context.Context, ref TrackerRef, add, remove []string) error
+	OpenIssue(ctx context.Context, repo string, title string, body string, labels []string) error
 }
```

The method is fire-and-forget from the worker's perspective: a failed `OpenIssue`
is logged and retried by the worker's retry policy, not by the sink.

### `remote-contract.md` — `RunRequest` gains `Findings`

The `RunRequest` now carries the findings collected by the sweep agent, to be
forwarded to `TrackerSink.OpenIssue`:

```diff
 type RunRequest struct {
 	Role      string // plan | do | sweep, derived from task tier (D6, I11). Never a model name.
 	Prompt    string
 	Budget    int
 	Timeout   time.Duration
 	RepoRef   Repository // identity + allowlisted URL
 	TicketRef TrackerRef
 	AttemptID string
+	Findings  []Finding   // populated by the sweep agent; forwarded to TrackerSink.OpenIssue
 }
```

## Code — `TrackerSink.OpenIssue` (new method)

The implementation lives in the GitHub tracker adapter:

```go
func (s *GitHubTrackerSink) OpenIssue(ctx context.Context, repo, title, body string, labels []string) error {
	_, err := s.github.Client.CreateIssue(ctx, s.github.RepoFullName, title, body,
		[]string{"idle-needs-human", ...labels})
	return err
}
```

The `CreateIssue` call uses the existing GitHub client; the issue is created on
the allowlisted repository with the `idle-needs-human` label (and any additional
labels passed from the sweep).

## Trade-offs

| Pro | Con |
|-----|-----|
| Findings are structured and searchable; the human sees them in the tracker immediately | Requires changes to `remote-contract.md` (new types + `RunRequest` field), `boundaries.md` (new `TrackerSink` method), and the GitHub adapter code |
| No extra hop — the remote doesn't need to file issues | More moving parts: report → findings → OpenIssue → issue creation |
| Keeps the sweep as a reporter, idle-deck as the author | ADR + 3 contract/doc amendments + code change |
| The `idle-needs-human` label is applied automatically by `OpenIssue` | None beyond the above |

## Consequences

- `docs/ontology.md` — unchanged; `Task.ticket` remains conditionally required; P3 is the named exception (D44).
- `docs/architecture/event-contract.md` — P1's issue-creation query skips issues carrying `idle-needs-human`, stated as loop-freedom rather than as bot-detection (D45).
- `docs/operations/operator-surface.md` — `IDLE_DECK_SWEEP_PERIOD` and `IDLE_DECK_SWEEP_REPOS` are required-if-sweeping, with the default prompt recorded; the new `OpenIssue` method does not change the sweep cadence.
- #10 can slice P3. The slice is small: a scheduler tick, a config pair, a report with findings, and the `OpenIssue` path. Issue #14's P3 sweep uses the new method.
- The route to a Draft PR therefore still runs **through a human**: the sweep files an issue via `OpenIssue`, the issue is an ordinary P1 candidate, and a human applies `idle-ready` (D29) to promote it. A sweep is a way of *finding* work, never of *doing* it. D22 already means idle-deck never merges, so a Draft PR opened by a sweep would be a branch nobody is on the hook to close out.