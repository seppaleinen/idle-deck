package worker

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/seppaleinen/idle-deck/harness"
	"github.com/seppaleinen/idle-deck/queue"
)

// tierRank returns a numeric rank for a task tier; lower is higher priority.
// P0=0, P1=1, P2=2, P3=3. Unknown tiers get the lowest priority (99).
func tierRank(t queue.TaskTier) int {
	switch t {
	case queue.TierP0:
		return 0
	case queue.TierP1:
		return 1
	case queue.TierP2:
		return 2
	case queue.TierP3:
		return 3
	default:
		return 99
	}
}

// defaultTimeoutForTier returns the tier default timeout per D37.
// P0=1800s, P1=900s, P2=10800s, P3=3600s.
func defaultTimeoutForTier(tier queue.TaskTier) time.Duration {
	switch tier {
	case queue.TierP0:
		return 1800 * time.Second
	case queue.TierP1:
		return 900 * time.Second
	case queue.TierP2:
		return 10800 * time.Second
	case queue.TierP3:
		return 3600 * time.Second
	default:
		return 0
	}
}

// defaultBudgetForTier returns the tier default budget per D37.
// P0=50000, P1=5000, P2=100000, P3=20000.
func defaultBudgetForTier(tier queue.TaskTier) int {
	switch tier {
	case queue.TierP0:
		return 50000
	case queue.TierP1:
		return 5000
	case queue.TierP2:
		return 100000
	case queue.TierP3:
		return 20000
	default:
		return 0
	}
}

// Queue is the local interface the worker depends on. The concrete adapter
// (store.SQLiteQueue) satisfies it structurally. Defined locally so the
// composition root does not import an adapter package (boundaries.md §4).
type Queue interface {
	Enqueue(ctx context.Context, task queue.Task) error
	Dequeue(ctx context.Context) (queue.Lease, error)
	Ack(ctx context.Context, lease queue.Lease, attemptID string) error
	Nack(ctx context.Context, lease queue.Lease, attemptID string, outcome queue.AttemptOutcome) error
	Events(ctx context.Context) (<-chan queue.QueueEvent, error)
	Release(ctx context.Context, lease queue.Lease) error
	GetTask(ctx context.Context, taskID string) (queue.Task, error)
	SaveArtifacts(ctx context.Context, attemptID string, artifacts []queue.Artifact) error
	AttemptCount(ctx context.Context, taskID string) (int, error)
}

// Harness is the local interface the worker depends on. The concrete adapter
// (harness.RemoteHarness) satisfies it structurally.
type Harness interface {
	Start(ctx context.Context, req harness.RunRequest) (harness.RunID, error)
	Abort(ctx context.Context, id harness.RunID) error
	Result(ctx context.Context, id harness.RunID) (harness.RunResult, error)
}

// TrackerSink writes diagnostics and labels back onto the tracker.
// The worker declares its own local copy of this shape so the composition
// root does not import an adapter package.
type TrackerSink interface {
	Comment(ctx context.Context, ref queue.TrackerRef, body string) error
	SetLabels(ctx context.Context, ref queue.TrackerRef, add, remove []string) error
	OpenIssue(ctx context.Context, repo string, title string, body string, labels []string) error
}

// IdlePolicy is the idle-gating dependency.
type IdlePolicy interface {
	Idle(ctx context.Context) (bool, error)
}

// Worker is the core composition root (not an adapter). It owns the pipeline:
// Queue.Dequeue → Harness.Start → Harness.Result → Ack/Nack, with idle gating,
// retry/escalation, and preemption.
//
// Per boundaries.md §4, it has a constructor and a Run(ctx) method, no interface.
// max_concurrent_jobs is a constructor parameter (D8; MVP: 1).
type Worker struct {
	queue          Queue
	harness        Harness
	sink           TrackerSink
	policy         IdlePolicy
	maxConcurrent  int
	backoffInitial time.Duration
	backoffMax     time.Duration
	log            *slog.Logger
}

// NewWorker creates a new Worker with all four dependencies injected.
// maxConcurrent is the worker-side concurrency limit (D8; MVP: 1).
// backoffInitial and backoffMax define the exponential backoff range (D37).
// Defaults are 30s initial, 10m max.
func NewWorker(q Queue, h Harness, s TrackerSink, p IdlePolicy, maxConcurrent int, backoffInitial, backoffMax time.Duration, log *slog.Logger) *Worker {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if backoffInitial <= 0 {
		backoffInitial = 30 * time.Second
	}
	if backoffMax <= 0 {
		backoffMax = 10 * time.Minute
	}
	if backoffMax < backoffInitial {
		backoffMax = backoffInitial
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Worker{
		queue:          q,
		harness:        h,
		sink:           s,
		policy:         p,
		maxConcurrent:  maxConcurrent,
		backoffInitial: backoffInitial,
		backoffMax:     backoffMax,
		log:            log,
	}
}

// Run runs the worker loop until ctx is cancelled. It returns ctx.Err() on
// clean shutdown.
func (w *Worker) Run(ctx context.Context) error {
	w.log.Info("worker: starting", "max_concurrent", w.maxConcurrent)

	var wg sync.WaitGroup
	errCh := make(chan error, w.maxConcurrent)
	for i := 0; i < w.maxConcurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.runLoop(ctx); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return ctx.Err()
}

// runLoop is a single worker loop. It runs until ctx is cancelled.
func (w *Worker) runLoop(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		// 1. Idle gate — before each Dequeue, consult IdlePolicy.
		// While not idle, sleep 1s and re-probe. Never fake-idle on probe
		// failure (error ≠ idle: boundaries.md §4, IdlePolicy negative list).
		if err := w.waitIdle(ctx); err != nil {
			return err
		}

		// 2. Dequeue — blocks until work is available.
		lease, err := w.queue.Dequeue(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return err
			}
			w.log.Error("worker: dequeue", "error", err)
			continue
		}

		// 3. Fetch task details.
		task, err := w.queue.GetTask(ctx, lease.TaskID)
		if err != nil {
			w.log.Error("worker: get task", "task_id", lease.TaskID, "error", err)
			// Release the lease so the task can be dequeued again.
			_ = w.queue.Release(ctx, lease)
			continue
		}

		// 4. Build RunRequest.
		req := w.buildRunRequest(task, lease)

		w.log.Info("worker: starting run",
			"task_id", task.ID,
			"tier", task.Tier,
			"role", req.Role,
			"attempt_id", lease.AttemptID,
		)

		// 5. Start the run.
		runID, err := w.harness.Start(ctx, req)
		if err != nil {
			w.log.Error("worker: harness start", "task_id", task.ID, "err", err)
			// Start failure is a retryable failure: the harness did not
			// accept the run, so the attempt can be retried.
			if err := w.queue.Nack(ctx, lease, lease.AttemptID, queue.OutcomeRetryableFailure); err != nil && !errors.Is(err, queue.ErrLeaseExpired) {
				w.log.Error("worker: nack after start failure", "task_id", task.ID, "err", err)
			}
			continue
		}

		// 6. Wait for result with preemption.
		result, preempted, err := w.waitForResult(ctx, lease, runID)
		if err != nil {
			w.log.Error("worker: wait for result", "task_id", task.ID, "error", err)
			if errNack := w.queue.Nack(ctx, lease, lease.AttemptID, queue.OutcomeRetryableFailure); errNack != nil && !errors.Is(errNack, queue.ErrLeaseExpired) {
				w.log.Error("worker: nack after wait error", "task_id", task.ID, "err", errNack)
			}
			continue
		}

		// 7. Handle result.
		if err, shouldBackoff := w.handleResult(ctx, lease, task, result, preempted); err != nil {
			w.log.Error("worker: handle result", "task_id", task.ID, "error", err)
			return err
		} else if shouldBackoff {
			// Apply exponential backoff with P0 preemption interrupt.
			backoff, err := w.calculateBackoff(ctx, lease.TaskID)
			if err != nil {
				w.log.Warn("worker: backoff calculation failed, using default", "task_id", lease.TaskID, "err", err)
				backoff = w.backoffInitial
			}
			w.log.Info("worker: backoff before next dequeue", "task_id", lease.TaskID, "backoff", backoff)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-w.queueEventsTimer(ctx, backoff):
			}
		}
	}
}

// waitIdle blocks until the IdlePolicy reports idle. On probe error it logs
// and retries (error ≠ idle: never fake-idle). While not idle it sleeps 1s
// and re-probes.
func (w *Worker) waitIdle(ctx context.Context) error {
	for {
		idle, err := w.policy.Idle(ctx)
		if err != nil {
			w.log.Warn("worker: idle probe error", "err", err)
			// Error ≠ idle: never fake-idle.
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(1 * time.Second):
			}
			continue
		}
		if idle {
			return nil
		}
		w.log.Debug("worker: not idle, waiting")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
}

// buildRunRequest constructs a harness.RunRequest from a task and attempt id.
// Role is derived from tier via RoleForTier (D6, I11). Prompt is verbatim.
// Budget and timeout come from the task, falling back to tier defaults (D17, D37).
func (w *Worker) buildRunRequest(task queue.Task, lease queue.Lease) harness.RunRequest {
	role := queue.RoleForTier(task.Tier)

	timeout := time.Duration(task.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultTimeoutForTier(task.Tier)
	}

	budget := task.Budget
	if budget <= 0 {
		budget = defaultBudgetForTier(task.Tier)
	}

	return harness.RunRequest{
		Role:    string(role),
		Prompt:  task.Prompt,
		Budget:  budget,
		Timeout: timeout,
		RepoRef: harness.Repository{
			ID:      task.RepositoryID,
			Name:    task.RepositoryID,
			Tracker: task.Ticket.Tracker,
		},
		TicketRef: harness.TicketRef{
			Tracker:      task.Ticket.Tracker,
			RepositoryID: task.Ticket.RepositoryID,
			ExternalID:   task.Ticket.ExternalID,
			URL:          task.Ticket.URL,
		},
		AttemptID: lease.AttemptID,
	}
}

// waitForResult runs harness.Result in a goroutine and selects on:
//   - the result channel: handle the outcome
//   - the events channel: if a higher-tier event arrives, preempt (D16)
//   - context Done: abort and return
//
// Returns (result, preempted, error). preempted=true means the run was
// aborted due to a higher-tier event; the Nack and artifact save have
// already been done. Error is returned only on context cancellation.
func (w *Worker) waitForResult(ctx context.Context, lease queue.Lease, runID harness.RunID) (harness.RunResult, bool, error) {
	resultCh := make(chan harness.RunResult, 1)
	errCh := make(chan error, 1)

	go func() {
		r, err := w.harness.Result(ctx, runID)
		if err != nil {
			errCh <- err
		} else {
			resultCh <- r
		}
	}()

	events, err := w.queue.Events(ctx)
	if err != nil {
		w.log.Error("worker: events channel", "error", err)
		// Fall back to waiting without preemption.
		select {
		case r := <-resultCh:
			return r, false, nil
		case err := <-errCh:
			return harness.RunResult{}, false, err
		case <-ctx.Done():
			w.harness.Abort(ctx, runID)
			return harness.RunResult{}, false, ctx.Err()
		}
	}

	// Drain any stale events from before this run started.
drain:
	for {
		select {
		case <-events:
		default:
			break drain
		}
	}

	leaseTierRank := tierRank(lease.Tier)
	for {
		select {
		case <-ctx.Done():
			w.harness.Abort(ctx, runID)
			// Wait for the result goroutine to drain.
			select {
			case <-resultCh:
			case <-errCh:
			case <-time.After(5 * time.Second):
			}
			return harness.RunResult{}, false, ctx.Err()
		case r := <-resultCh:
			return r, false, nil
		case err := <-errCh:
			return harness.RunResult{}, false, err
		case ev := <-events:
			if tierRank(ev.Tier) < leaseTierRank {
				// Preemption (D16): higher-tier event arrived.
				w.log.Info("worker: preempting run",
					"task_id", lease.TaskID,
					"running_tier", lease.Tier,
					"event_tier", ev.Tier,
				)
				_ = w.harness.Abort(ctx, runID)
				// Wait for the result goroutine to return (it should be
				// preempted — the harness returns OutcomePreempted).
				select {
				case r := <-resultCh:
					// Save any artifacts from the aborted run.
					if len(r.Artifacts) > 0 {
						_ = w.queue.SaveArtifacts(ctx, lease.AttemptID, mapHarnessArtifacts(lease.AttemptID, r.Artifacts))
					}
					// Nack with preempted.
					if err := w.queue.Nack(ctx, lease, lease.AttemptID, queue.OutcomePreempted); err != nil && !errors.Is(err, queue.ErrLeaseExpired) {
						w.log.Error("worker: nack preempted", "task_id", lease.TaskID, "err", err)
					}
					// Check escalation (preempted is retryable per D17/I7).
					// We need to fetch the task to check escalation state.
					if updatedTask, err := w.queue.GetTask(ctx, lease.TaskID); err == nil {
						if updatedTask.State == queue.StateEscalated {
							w.escalate(ctx, updatedTask, r, lease.AttemptID)
						}
					}
					return harness.RunResult{}, true, nil
				case err := <-errCh:
					w.log.Error("worker: result error after abort", "err", err)
					if errNack := w.queue.Nack(ctx, lease, lease.AttemptID, queue.OutcomePreempted); errNack != nil && !errors.Is(errNack, queue.ErrLeaseExpired) {
						w.log.Error("worker: nack preempted after error", "task_id", lease.TaskID, "err", errNack)
					}
					return harness.RunResult{}, true, nil
				case <-time.After(5 * time.Second):
					w.log.Warn("worker: result goroutine did not return after abort")
					if errNack := w.queue.Nack(ctx, lease, lease.AttemptID, queue.OutcomePreempted); errNack != nil && !errors.Is(errNack, queue.ErrLeaseExpired) {
						w.log.Error("worker: nack preempted timeout", "task_id", lease.TaskID, "err", errNack)
					}
					return harness.RunResult{}, true, nil
				}
			}
			// Lower or equal tier event: ignore, keep waiting.
		}
	}
}

// handleResult processes a terminal RunResult. On success it saves artifacts
// and Acks. On failure it Nacks with the outcome and checks for escalation.
// Returns (err, shouldBackoff). shouldBackoff is true when the task was retried (not escalated and not succeeded).
func (w *Worker) handleResult(ctx context.Context, lease queue.Lease, task queue.Task, result harness.RunResult, preempted bool) (error, bool) {
	// If preempted, the Nack, artifact save, and escalation check were
	// already done in waitForResult (D16/I7). Return immediately to avoid
	// a double-Nack.
	if preempted {
		return nil, false
	}

	// Map harness artifacts to queue artifacts and save them.
	artifacts := mapHarnessArtifacts(lease.AttemptID, result.Artifacts)
	if len(artifacts) > 0 {
		if err := w.queue.SaveArtifacts(ctx, lease.AttemptID, artifacts); err != nil {
			w.log.Error("worker: save artifacts", "attempt_id", lease.AttemptID, "err", err)
		}
	}

	// Determine the outcome to pass to Nack.
	var outcome queue.AttemptOutcome
	switch result.Outcome {
	case harness.OutcomeSucceeded:
		outcome = queue.OutcomeSucceeded
	case harness.OutcomeRetryableFailure:
		outcome = queue.OutcomeRetryableFailure
	case harness.OutcomeNonRetryableFailure:
		outcome = queue.OutcomeNonRetryableFailure
	case harness.OutcomeTimeout:
		outcome = queue.OutcomeTimeout
	case harness.OutcomePreempted:
		outcome = queue.OutcomePreempted
	default:
		w.log.Warn("worker: unknown outcome", "outcome", result.Outcome)
		outcome = queue.OutcomeNonRetryableFailure
	}

	w.log.Info("worker: run outcome",
		"task_id", task.ID,
		"tier", task.Tier,
		"outcome", outcome,
		"preempted", preempted,
	)

	if outcome == queue.OutcomeSucceeded {
		if err := w.queue.Ack(ctx, lease, lease.AttemptID); err != nil {
			if errors.Is(err, queue.ErrLeaseExpired) {
				w.log.Warn("worker: ack lease expired (task may have been reclaimed)", "task_id", task.ID)
				return nil, false
			}
			w.log.Error("worker: ack", "task_id", task.ID, "err", err)
			// W6: log+continue on transient DB errors, consistent with start/wait paths.
			return nil, false
		}
		// Terminal state: remove the trigger label we reacted to (event-contract §6).
		w.removeTriggerLabel(ctx, task)
		return nil, false
	}

	// Nack with the outcome; the queue's retry policy decides (I6).
	if err := w.queue.Nack(ctx, lease, lease.AttemptID, outcome); err != nil {
		if errors.Is(err, queue.ErrLeaseExpired) {
			w.log.Warn("worker: nack lease expired (task may have been reclaimed)", "task_id", task.ID)
			return nil, false
		}
		w.log.Error("worker: nack", "task_id", task.ID, "err", err)
		// W6: log+continue on transient DB errors, consistent with start/wait paths.
		return nil, false
	}

	// After Nack, check if the task was escalated (D14, ADR 0009).
	updated, err := w.queue.GetTask(ctx, lease.TaskID)
	if err != nil {
		w.log.Error("worker: get task after nack", "task_id", lease.TaskID, "err", err)
		return nil, false
	}
	if updated.State != queue.StateEscalated {
		w.log.Info("worker: task queued for retry", "task_id", task.ID, "state", updated.State)
		// Task was retried, apply backoff.
		return nil, true
	}

	// Escalation path (D14, ADR 0009, event-contract §6, ontology I5):
	// 1. Comment diagnostics via TrackerSink.Comment.
	// 2. Apply idle-needs-human label and remove trigger label via TrackerSink.SetLabels.
	// 3. Ensure debug branch artifact exists (I5).
	// All are fire-and-forget: a failed comment/label/save is logged and does not block.
	w.escalate(ctx, updated, result, lease.AttemptID)

	return nil, false
}

// calculateBackoff returns the exponential backoff for a task based on attempt count (D37).
func (w *Worker) calculateBackoff(ctx context.Context, taskID string) (time.Duration, error) {
	count, err := w.queue.AttemptCount(ctx, taskID)
	if err != nil {
		return 0, err
	}
	if count <= 0 {
		count = 1
	}
	backoff := w.backoffInitial * time.Duration(1<<uint(count-1))
	if backoff > w.backoffMax {
		backoff = w.backoffMax
	}
	return backoff, nil
}

// queueEventsTimer returns a channel that fires after d duration, but is interruptible by Queue.Events.
func (w *Worker) queueEventsTimer(ctx context.Context, d time.Duration) <-chan struct{} {
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			select {
			case ch <- struct{}{}:
			case <-ctx.Done():
			}
		case <-w.eventsNotify(ctx):
			// P0 arrived, interrupt backoff
			return
		}
	}()
	return ch
}

// eventsNotify returns a channel that fires when a higher-tier event arrives.
// The channel is buffered (size 1) so the send never blocks: if the timer
// fires first and the caller has already stopped listening, the event goroutine
// can still deliver without panicking on a send to a closed channel.
func (w *Worker) eventsNotify(ctx context.Context) <-chan struct{} {
	events, err := w.queue.Events(ctx)
	if err != nil {
		return make(chan struct{})
	}
	ch := make(chan struct{}, 1)
	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				// Any event may interrupt backoff; preemption logic will handle priority.
				select {
				case ch <- struct{}{}:
				case <-ctx.Done():
					return
				}
				return
			}
		}
	}()
	return ch
}

// buildDiagnosticsBody constructs the comment body for an escalation (D14).
func (w *Worker) buildDiagnosticsBody(task queue.Task, result harness.RunResult) string {
	var sb strings.Builder
	sb.WriteString("idle-deck escalation (D14)\n")
	sb.WriteString("\n")
	sb.WriteString("task: ")
	sb.WriteString(task.ID)
	sb.WriteString("\n")
	sb.WriteString("tier: ")
	sb.WriteString(string(task.Tier))
	sb.WriteString("\n")
	sb.WriteString("outcome: ")
	sb.WriteString(string(result.Outcome))
	sb.WriteString("\n")
	if len(result.Artifacts) > 0 {
		sb.WriteString("artifacts:\n")
		for _, a := range result.Artifacts {
			sb.WriteString("- ")
			sb.WriteString(a.Kind)
			if a.BranchPurpose != nil {
				sb.WriteString(" (purpose: ")
				sb.WriteString(*a.BranchPurpose)
				sb.WriteString(")")
			}
			sb.WriteString(": ")
			sb.WriteString(a.URI)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// mapHarnessArtifacts converts harness-returned artifacts to queue.Artifact
// for persistence. IDs are generated fresh; the harness does not assign them.
func mapHarnessArtifacts(attemptID string, in []harness.Artifact) []queue.Artifact {
	if len(in) == 0 {
		return nil
	}
	out := make([]queue.Artifact, 0, len(in))
	now := time.Now().UnixNano()
	for _, a := range in {
		qa := queue.Artifact{
			ID:            newQueueID(),
			AttemptID:     attemptID,
			Kind:          queue.ArtifactKind(a.Kind),
			BranchPurpose: "",
			URI:           a.URI,
			ProducedAt:    now,
		}
		if a.BranchPurpose != nil {
			qa.BranchPurpose = queue.BranchPurpose(*a.BranchPurpose)
		}
		out = append(out, qa)
	}
	return out
}

// Label vocabulary (D29). The worker declares its own local copies so the
// composition root does not import the tracker adapter (boundaries.md §4).
const (
	labelHotfix     = "idle-hotfix"
	labelReady      = "idle-ready"
	labelRedo       = "idle-redo"
	labelNeedsHuman = "idle-needs-human"
)

// triggerLabelForEvent maps a task's TriggeredBy.EventType to the D29 trigger
// label that must be removed on terminal state (event-contract §6). Returns ""
// when the event carried no label (P1 issue opened, P3 sweep) — for those
// there is no trigger label to remove.
//
// The tracker adapter (tracker.Github.Parse) records eventType as
// "issue.labeled:<full-label>" — e.g. "issue.labeled:idle-hotfix" — so the
// cases match the full D29 label names, not bare trigger strings.
func triggerLabelForEvent(eventType string) string {
	switch eventType {
	case "issue.labeled:idle-hotfix":
		return labelHotfix
	case "issue.labeled:idle-ready":
		return labelReady
	case "issue.labeled:idle-redo":
		return labelRedo
	default:
		return ""
	}
}

// ensureDebugBranch enforces I5: an escalated task has a branch artifact
// with branch_purpose = debug. If the harness did not return one, idle-deck
// synthesizes it — the debug branch is named debug/<task_id> (D15) and is
// owned by idle-deck, not the harness (boundaries.md §3).
func (w *Worker) ensureDebugBranch(ctx context.Context, attemptID string, task queue.Task, result harness.RunResult) {
	for _, a := range result.Artifacts {
		if a.Kind == string(queue.ArtifactBranch) && a.BranchPurpose != nil && *a.BranchPurpose == string(queue.BranchDebug) {
			return
		}
	}
	artifact := queue.Artifact{
		ID:            newQueueID(),
		AttemptID:     attemptID,
		Kind:          queue.ArtifactBranch,
		BranchPurpose: queue.BranchDebug,
		URI:           "debug/" + task.ID,
		ProducedAt:    time.Now().UnixNano(),
	}
	if err := w.queue.SaveArtifacts(ctx, attemptID, []queue.Artifact{artifact}); err != nil {
		w.log.Error("worker: save synthesized debug branch", "task_id", task.ID, "err", err)
	}
}

// escalate fires the escalation side-effects (D14, event-contract §6, I5):
// diagnostics comment, idle-needs-human label (and trigger-label removal),
// and the debug-branch artifact. All are fire-and-forget.
func (w *Worker) escalate(ctx context.Context, task queue.Task, result harness.RunResult, attemptID string) {
	w.log.Warn("worker: task escalated", "task_id", task.ID, "tier", task.Tier)

	body := w.buildDiagnosticsBody(task, result)
	if err := w.sink.Comment(ctx, task.Ticket, body); err != nil {
		w.log.Error("worker: escalation comment", "task_id", task.ID, "err", err)
	}
	if err := w.setEscalationLabels(ctx, task); err != nil {
		w.log.Error("worker: escalation label", "task_id", task.ID, "err", err)
	}
	w.ensureDebugBranch(ctx, attemptID, task, result)
}

// setEscalationLabels applies idle-needs-human and removes the trigger label
// the task reacted to (event-contract §6: "on escalation it adds
// idle-needs-human" and removes the trigger label on any terminal state).
func (w *Worker) setEscalationLabels(ctx context.Context, task queue.Task) error {
	remove := triggerLabelForEvent(task.TriggeredBy.EventType)
	var removeList []string
	if remove != "" {
		removeList = []string{remove}
	}
	return w.sink.SetLabels(ctx, task.Ticket, []string{labelNeedsHuman}, removeList)
}

// removeTriggerLabel removes the trigger label the task reacted to on a
// terminal state (event-contract §6). No-op for P1 (issue.opened) and P3,
// which carry no trigger label, and for tasks with no ticket (P3).
func (w *Worker) removeTriggerLabel(ctx context.Context, task queue.Task) {
	label := triggerLabelForEvent(task.TriggeredBy.EventType)
	if label == "" || task.Ticket.Tracker == "" {
		return
	}
	if err := w.sink.SetLabels(ctx, task.Ticket, nil, []string{label}); err != nil {
		w.log.Error("worker: remove trigger label", "task_id", task.ID, "label", label, "err", err)
	}
}

// newQueueID generates a random artifact ID.
func newQueueID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%032d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)
}
