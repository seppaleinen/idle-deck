package worker

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/seppaleinen/idle-deck/harness"
	"github.com/seppaleinen/idle-deck/queue"
)

type stubQueue struct {
	t            *testing.T
	mu           sync.Mutex
	tasks        map[string]queue.Task
	attempts     map[string][]queueAttempt
	leases       map[string]queue.Lease
	events       chan queue.QueueEvent
	enqueued     []queue.Task
	maxAttempts  int
	nextOrdinal  int
	dequeueTimes []time.Time
}

type queueAttempt struct {
	ID      string
	Outcome queue.AttemptOutcome
}

func newStubQueue(t *testing.T) *stubQueue {
	return &stubQueue{
		t:           t,
		tasks:       make(map[string]queue.Task),
		attempts:    make(map[string][]queueAttempt),
		leases:     make(map[string]queue.Lease),
		events:     make(chan queue.QueueEvent, 10),
		maxAttempts: 3, // mirrors store.NewSQLiteQueue(dbPath, store.WithMaxAttempts(3))
	}
}

func (q *stubQueue) Enqueue(ctx context.Context, task queue.Task) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.enqueued = append(q.enqueued, task)
	q.tasks[task.ID] = task
	return nil
}

// tierRank returns a numeric rank for a task tier; lower is higher priority.
func (q *stubQueue) tierRank(t queue.TaskTier) int {
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

func (q *stubQueue) Dequeue(ctx context.Context) (queue.Lease, error) {
	// Wait for task to be enqueued
	for {
		q.mu.Lock()
		// Pick the highest-tier queued task (tier-descending, FIFO within tier).
		var bestID string
		var bestTier queue.TaskTier
		for id, task := range q.tasks {
			if task.State == queue.StateQueued {
				if bestID == "" || q.tierRank(task.Tier) < q.tierRank(bestTier) {
					bestID = id
					bestTier = task.Tier
				}
			}
		}
		if bestID != "" {
			q.nextOrdinal++
			attemptID := fmt.Sprintf("attempt-%s-%d", bestID, q.nextOrdinal)
			attempt := queueAttempt{ID: attemptID, Outcome: ""}
			q.attempts[bestID] = append(q.attempts[bestID], attempt)
			task := q.tasks[bestID]
			task.State = queue.StateRunning
			q.tasks[bestID] = task
			lease := queue.Lease{
				TaskID:    bestID,
				AttemptID: attemptID,
				ExpiresAt: time.Now().Add(30 * time.Minute).UnixNano(),
				Tier:      task.Tier,
			}
			q.leases[bestID] = lease
			q.dequeueTimes = append(q.dequeueTimes, time.Now())
			q.mu.Unlock()
			return lease, nil
		}
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return queue.Lease{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// DequeueTimes returns the timestamps of all Dequeue calls, in order.
// Used by the backoff test to assert the second dequeue was delayed.
func (q *stubQueue) DequeueTimes() []time.Time {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]time.Time, len(q.dequeueTimes))
	copy(out, q.dequeueTimes)
	return out
}

func (q *stubQueue) Ack(ctx context.Context, lease queue.Lease, attemptID string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Lease-existence check: if the lease has been reclaimed (removed from
	// the leases map), the attempt died with its worker — ErrLeaseExpired.
	if _, ok := q.leases[lease.TaskID]; !ok {
		return queue.ErrLeaseExpired
	}
	task, ok := q.tasks[lease.TaskID]
	if !ok {
		return queue.ErrNotFound
	}
	if lease.AttemptID != attemptID {
		return queue.ErrLeaseExpired
	}
	// Record outcome
	for i, att := range q.attempts[lease.TaskID] {
		if att.ID == attemptID {
			q.attempts[lease.TaskID][i].Outcome = queue.OutcomeSucceeded
			break
		}
	}
	task.State = queue.StateSucceeded
	q.tasks[lease.TaskID] = task
	delete(q.leases, lease.TaskID)
	return nil
}

func (q *stubQueue) Nack(ctx context.Context, lease queue.Lease, attemptID string, outcome queue.AttemptOutcome) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	// Lease-existence check: if the lease has been reclaimed (removed from
	// the leases map), the attempt died with its worker — ErrLeaseExpired.
	if _, ok := q.leases[lease.TaskID]; !ok {
		return queue.ErrLeaseExpired
	}
	task, ok := q.tasks[lease.TaskID]
	if !ok {
		return queue.ErrNotFound
	}
	if lease.AttemptID != attemptID {
		return queue.ErrLeaseExpired
	}
	// Record outcome
	for i, att := range q.attempts[lease.TaskID] {
		if att.ID == attemptID {
			q.attempts[lease.TaskID][i].Outcome = outcome
			break
		}
	}
	// Retry policy mirrors SQLiteQueue.Nack: retryable outcomes (retryable_failure,
	// timeout, preempted) re-queue while the attempt count is under maxAttempts;
	// every other outcome — succeeded, non-retryable_failure — is terminal
	// (escalated). This is outcome-aware, unlike the naive "2 retries then
	// escalate" form, because a non-retryable protocol violation must drop the
	// task on the first attempt.
	retryable := outcome == queue.OutcomeRetryableFailure ||
		outcome == queue.OutcomeTimeout ||
		outcome == queue.OutcomePreempted
	attemptCount := len(q.attempts[lease.TaskID])
	if retryable && attemptCount < q.maxAttempts {
		task.State = queue.StateQueued
	} else {
		task.State = queue.StateEscalated
	}
	q.tasks[lease.TaskID] = task
	delete(q.leases, lease.TaskID)
	return nil
}

// ReclaimLease simulates the queue reclaiming an expired lease: the abandoned
// attempt is recorded as timeout and the task is re-queued for another worker
// to pick up. It mirrors the SQLiteQueue.tryClaim reclaim step.
func (q *stubQueue) ReclaimLease(taskID string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	attempts := q.attempts[taskID]
	if len(attempts) > 0 {
		q.attempts[taskID][len(attempts)-1].Outcome = queue.OutcomeTimeout
	}
	if task, ok := q.tasks[taskID]; ok {
		task.State = queue.StateQueued
		q.tasks[taskID] = task
	}
	delete(q.leases, taskID)
}

func (q *stubQueue) Events(ctx context.Context) (<-chan queue.QueueEvent, error) {
	return q.events, nil
}

func (q *stubQueue) Release(ctx context.Context, lease queue.Lease) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.leases, lease.TaskID)
	return nil
}

func (q *stubQueue) GetTask(ctx context.Context, taskID string) (queue.Task, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	task, ok := q.tasks[taskID]
	if !ok {
		return queue.Task{}, queue.ErrNotFound
	}
	return task, nil
}

func (q *stubQueue) SaveArtifacts(ctx context.Context, attemptID string, artifacts []queue.Artifact) error {
	return nil
}

func (q *stubQueue) AttemptCount(ctx context.Context, taskID string) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	attempts := q.attempts[taskID]
	count := 0
	for _, a := range attempts {
		if a.Outcome != "" {
			count++
		}
	}
	return count, nil
}

type stubHarness struct {
	t              *testing.T
	mu             sync.Mutex
	outcomes       map[string]harness.AttemptOutcome
	artifacts      map[string][]harness.Artifact
	abortCh        map[string]chan struct{}
	blockResult    map[string]chan struct{}
	blockAllResult chan struct{}
}

func newStubHarness(t *testing.T) *stubHarness {
	return &stubHarness{
		outcomes:    make(map[string]harness.AttemptOutcome),
		artifacts:   make(map[string][]harness.Artifact),
		abortCh:     make(map[string]chan struct{}),
		blockResult: make(map[string]chan struct{}),
	}
}

func (h *stubHarness) SetOutcome(runID string, outcome harness.AttemptOutcome, artifacts []harness.Artifact) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.outcomes[runID] = outcome
	h.artifacts[runID] = artifacts
}

// BlockAllResults makes every Harness.Result call block until UnblockAllResults
// is called, once an outcome is available. Used by the preemption test to
// guarantee the result goroutine cannot return before the worker's select
// loop has observed a higher-tier event — the drain is the only safe moment
// to inject, and the block keeps the result goroutine alive across it.
func (h *stubHarness) BlockAllResults() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.blockAllResult = make(chan struct{})
}

func (h *stubHarness) UnblockAllResults() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.blockAllResult != nil {
		close(h.blockAllResult)
		h.blockAllResult = nil
	}
}

// isAborted reports whether Abort has been called for runID (the abortCh is
// closed). Used by the preemption test to synchronise with the worker's
// preemption select branch.
func (h *stubHarness) isAborted(runID harness.RunID) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch, ok := h.abortCh[string(runID)]
	if !ok {
		return false
	}
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func (h *stubHarness) Start(ctx context.Context, req harness.RunRequest) (harness.RunID, error) {
	runID := harness.RunID("run-" + req.AttemptID)
	h.mu.Lock()
	h.abortCh[string(runID)] = make(chan struct{})
	h.mu.Unlock()
	return runID, nil
}

func (h *stubHarness) Abort(ctx context.Context, id harness.RunID) error {
	h.mu.Lock()
	ch, ok := h.abortCh[string(id)]
	h.mu.Unlock()
	if ok {
		select {
		case <-ch:
		default:
			close(ch)
		}
	}
	return nil
}

func (h *stubHarness) Result(ctx context.Context, id harness.RunID) (harness.RunResult, error) {
	// Block until outcome is set (poll up to 10s)
	deadline := time.Now().Add(10 * time.Second)
	for {
		h.mu.Lock()
		outcome, ok := h.outcomes[string(id)]
		artifacts := h.artifacts[string(id)]
		block := h.blockResult[string(id)]
		blockAll := h.blockAllResult
		h.mu.Unlock()
		if ok {
			// If the test wants to hold the result until a signal, wait for
			// it before returning. The signal is only ever closed by the
			// test, never by the worker.
			if block != nil {
				select {
				case <-block:
				case <-ctx.Done():
					return harness.RunResult{}, ctx.Err()
				case <-time.After(10 * time.Second):
					return harness.RunResult{}, context.DeadlineExceeded
				}
			}
			if blockAll != nil {
				select {
				case <-blockAll:
				case <-ctx.Done():
					return harness.RunResult{}, ctx.Err()
				case <-time.After(10 * time.Second):
					return harness.RunResult{}, context.DeadlineExceeded
				}
			}
			return harness.RunResult{
				Outcome:   outcome,
				Artifacts: artifacts,
			}, nil
		}
		select {
		case <-ctx.Done():
			return harness.RunResult{}, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return harness.RunResult{
				Outcome:   harness.OutcomeSucceeded,
				Artifacts: nil,
			}, nil
		}
	}
}

type stubSink struct {
	t         *testing.T
	mu        sync.Mutex
	comments  []struct {
		ref  queue.TrackerRef
		body string
	}
	labels     []struct {
		ref         queue.TrackerRef
		add, remove []string
	}
	openIssues []struct {
		repo   string
		title  string
		body   string
		labels []string
	}
}

func newStubSink(t *testing.T) *stubSink {
	return &stubSink{}
}

func (s *stubSink) Comment(ctx context.Context, ref queue.TrackerRef, body string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.comments = append(s.comments, struct {
		ref  queue.TrackerRef
		body string
	}{ref, body})
	return nil
}

func (s *stubSink) SetLabels(ctx context.Context, ref queue.TrackerRef, add, remove []string) error {
	s.labels = append(s.labels, struct {
		ref         queue.TrackerRef
		add, remove []string
	}{ref, add, remove})
	return nil
}

func (s *stubSink) OpenIssue(ctx context.Context, repo string, title string, body string, labels []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.openIssues = append(s.openIssues, struct {
		repo   string
		title  string
		body   string
		labels []string
	}{repo, title, body, labels})
	return nil
}

type stubPolicy struct {
	idle bool
	err  error
}

func newStubPolicy(idle bool, err error) *stubPolicy {
	return &stubPolicy{idle: idle, err: err}
}

func (p *stubPolicy) Idle(ctx context.Context) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.idle, nil
}

func makeTask(id string, tier queue.TaskTier) queue.Task {
	return queue.Task{
		ID:           id,
		RepositoryID: "repo",
		Ticket: queue.TrackerRef{
			Tracker:      "github",
			RepositoryID: "repo",
			ExternalID:   "1",
			URL:          "https://github.com/repo/1",
		},
		Tier:   tier,
		Prompt: "test prompt",
		State:  queue.StateQueued,
		TriggeredBy: queue.TriggeredBy{
			EventType: "issue.opened",
			DedupeKey: "github:issue:repo:1:created",
		},
	}
}

// makeTaskWithTrigger creates a task whose TriggeredBy.EventType carries a
// trigger label, so the success/escalation label-removal paths (W1) are
// exercised.
func makeTaskWithTrigger(id string, tier queue.TaskTier, eventType string) queue.Task {
	t := makeTask(id, tier)
	t.TriggeredBy.EventType = eventType
	t.TriggeredBy.DedupeKey = "github:issue:repo:1:" + eventType
	return t
}

func TestSuccessP1(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	task := makeTaskWithTrigger("t1", queue.TierP1, "issue.opened")
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Start worker in background
	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// Wait for worker to dequeue and start the task
	time.Sleep(100 * time.Millisecond)

	// Check if task is running
	taskCheck, _ := q.GetTask(ctx, "t1")
	if taskCheck.State != queue.StateRunning {
		// Might already be succeeded if harness returned immediately
	}

	// The worker creates a run, we need to find the attempt ID
	// Let's wait a bit more for the run to be started
	time.Sleep(100 * time.Millisecond)

	// Get attempts for task
	q.mu.Lock()
	attempts := q.attempts["t1"]
	q.mu.Unlock()
	if len(attempts) == 0 {
		t.Fatal("no attempts recorded")
	}
	attemptID := attempts[len(attempts)-1].ID

	// Set outcome for the run
	runID := harness.RunID("run-" + attemptID)
	h.SetOutcome(string(runID), harness.OutcomeSucceeded, []harness.Artifact{
		{Kind: "pull_request", URI: "https://github.com/repo/pull/1"},
	})

	// Wait for worker to process result
	time.Sleep(500 * time.Millisecond)

	// Verify task succeeded
	taskResult, err := q.GetTask(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if taskResult.State != queue.StateSucceeded {
		t.Errorf("expected succeeded, got %s", taskResult.State)
	}

	attemptCount, _ := q.AttemptCount(ctx, "t1")
	if attemptCount < 1 {
		t.Errorf("expected >=1 attempt, got %d", attemptCount)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Log("worker did not stop in time")
	}

	// W1: on success the trigger label must be removed (event-contract §6).
	// P1's trigger is "issue.opened" → no label to remove, so no SetLabels
	// call is expected. Verify the sink was not touched.
	if len(sink.labels) != 0 {
		t.Errorf("expected no label writes on P1 success, got %d", len(sink.labels))
	}
}

// TestTriggerLabelRemovedOnSuccess verifies event-contract §6: on any
// terminal state the trigger label the worker reacted to is removed.
// The eventType here matches the real tracker format produced by
// tracker.Github.Parse: "issue.labeled:<full-label>".
func TestTriggerLabelRemovedOnSuccess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	// P2 task with the real tracker event type for idle-ready.
	task := makeTaskWithTrigger("t-trigger", queue.TierP2, "issue.labeled:idle-ready")
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// Wait for the task to be dequeued and started.
	time.Sleep(100 * time.Millisecond)
	q.mu.Lock()
	atts := q.attempts["t-trigger"]
	q.mu.Unlock()
	if len(atts) == 0 {
		t.Fatal("task was never dequeued")
	}
	attemptID := atts[len(atts)-1].ID

	runID := harness.RunID("run-" + attemptID)
	h.SetOutcome(string(runID), harness.OutcomeSucceeded, []harness.Artifact{
		{Kind: "pull_request", URI: "https://github.com/repo/pull/2"},
	})

	// Wait for success.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		taskResult, _ := q.GetTask(ctx, "t-trigger")
		if taskResult.State == queue.StateSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	taskResult, _ := q.GetTask(ctx, "t-trigger")
	if taskResult.State != queue.StateSucceeded {
		t.Errorf("expected succeeded, got %s", taskResult.State)
	}

	// W1: idle-ready must have been removed on the success path.
	found := false
	for _, l := range sink.labels {
		for _, r := range l.remove {
			if r == "idle-ready" {
				found = true
			}
		}
	}
	if !found {
		t.Error("expected idle-ready to be removed on success (event-contract §6)")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}
}

func TestRetryTwiceThenEscalate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 10*time.Millisecond, 100*time.Millisecond, log)

	task := makeTask("t2", queue.TierP2)
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	// Pre-set outcomes for all 3 attempts - the harness stub returns
	// outcomes keyed by runID = "run-<attemptID>". We need to set outcomes
	// before each attempt completes. We poll for new attempts and set outcomes.
	go func() {
		for i := 0; i < 3; i++ {
			var attemptID string
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				q.mu.Lock()
				attempts := q.attempts["t2"]
				q.mu.Unlock()
				if len(attempts) > i {
					attemptID = attempts[len(attempts)-1].ID
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if attemptID == "" {
				return
			}
			runID := harness.RunID("run-" + attemptID)
			if i < 2 {
				h.SetOutcome(string(runID), harness.OutcomeRetryableFailure, nil)
			} else {
				h.SetOutcome(string(runID), harness.OutcomeRetryableFailure, []harness.Artifact{
					{Kind: "branch", URI: "https://github.com/repo/debug/t2"},
				})
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// Wait for escalation
	time.Sleep(2000 * time.Millisecond)

	taskResult, _ := q.GetTask(ctx, "t2")
	if taskResult.State != queue.StateEscalated {
		t.Errorf("expected escalated, got %s", taskResult.State)
	}

	if len(sink.labels) == 0 {
		t.Error("expected label to be set")
	} else {
		found := false
		for _, l := range sink.labels {
			for _, add := range l.add {
				if add == "idle-needs-human" {
					found = true
					break
				}
			}
		}
		if !found {
			t.Error("expected idle-needs-human label")
		}
	}

	if len(sink.comments) == 0 {
		t.Error("expected comment to be posted")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}
}

func TestPreemptionP0OverP2(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	// Block all harness results so the P2 run cannot complete before the
	// worker's waitForResult drain+select loop has observed a higher-tier
	// event. The drain is the only safe moment to inject: injecting earlier
	// would see the event consumed by the drain, making preemption
	// non-deterministic.
	h.BlockAllResults()

	// Enqueue P2 task first.
	taskP2 := makeTask("t-p2", queue.TierP2)
	if err := q.Enqueue(ctx, taskP2); err != nil {
		t.Fatal(err)
	}

	// Start worker in background.
	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// Wait for P2 to be dequeued and started.
	var p2Lease queue.Lease
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		p2Lease, ok = q.GetLeaseForTask("t-p2")
		if ok && p2Lease.TaskID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p2Lease.TaskID == "" {
		t.Fatal("P2 was not dequeued in time")
	}

	// Set a retryable outcome for the P2 run. The result goroutine will
	// block on BlockAllResults until the test unblocks it, so the worker's
	// select loop is the only thing that can observe the P0 event.
	runIDP2 := harness.RunID("run-" + p2Lease.AttemptID)
	h.SetOutcome(string(runIDP2), harness.OutcomeRetryableFailure, nil)

	// Inject P0 task and event: P2 is now running (its result goroutine is
	// blocked on BlockAllResults), so the worker's waitForResult has already
	// passed the drain and is in the preemption select loop. Injecting now
	// guarantees the event is observed by the select, not consumed by the
	// drain.
	taskP0 := makeTask("t-p0", queue.TierP0)
	if err := q.Enqueue(ctx, taskP0); err != nil {
		t.Fatal(err)
	}
	select {
	case q.events <- queue.QueueEvent{Tier: queue.TierP0}:
	default:
	}

	// Wait for the worker to abort the P2 run.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.isAborted(runIDP2) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !h.isAborted(runIDP2) {
		t.Fatal("P2 run was not aborted after P0 event")
	}

	// Unblock the P2 result goroutine so it can return and the worker can
	// finish the preemption branch.
	h.UnblockAllResults()

	// Wait for the P2 task to be re-queued (preempted is retryable, so it
	// goes back to queued for a retry).
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		taskP2Result, _ := q.GetTask(ctx, "t-p2")
		if taskP2Result.State == queue.StateQueued {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	taskP2Result, _ := q.GetTask(ctx, "t-p2")
	if taskP2Result.State != queue.StateQueued {
		t.Errorf("P2 state after preemption: got %s, want %s", taskP2Result.State, queue.StateQueued)
	}

	// The P2 attempt must have been recorded as preempted.
	var p2AttemptOutcome queue.AttemptOutcome
	q.mu.Lock()
	for _, att := range q.attempts["t-p2"] {
		if att.ID == p2Lease.AttemptID {
			p2AttemptOutcome = att.Outcome
		}
	}
	q.mu.Unlock()
	if p2AttemptOutcome != queue.OutcomePreempted {
		t.Errorf("P2 attempt outcome: got %s, want %s", p2AttemptOutcome, queue.OutcomePreempted)
	}

	// Wait for the P0 task to be dequeued and started.
	var p0Lease queue.Lease
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var ok bool
		p0Lease, ok = q.GetLeaseForTask("t-p0")
		if ok && p0Lease.TaskID != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p0Lease.TaskID == "" {
		t.Fatal("P0 was not dequeued after preempting P2")
	}

	// Let P0 succeed.
	runIDP0 := harness.RunID("run-" + p0Lease.AttemptID)
	h.SetOutcome(string(runIDP0), harness.OutcomeSucceeded, nil)

	// Wait for P0 to succeed.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		taskP0Result, _ := q.GetTask(ctx, "t-p0")
		if taskP0Result.State == queue.StateSucceeded {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	taskP0Result, _ := q.GetTask(ctx, "t-p0")
	if taskP0Result.State != queue.StateSucceeded {
		t.Errorf("P0 state: got %s, want %s", taskP0Result.State, queue.StateSucceeded)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Log("worker did not stop in time")
	}
}

func TestLeaseExpiryHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	task := makeTask("t-lease", queue.TierP1)
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	go func() {
		_ = w.Run(ctx)
	}()

	// Wait for the task to be dequeued (lease exists).
	var attemptID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		atts := q.attempts["t-lease"]
		q.mu.Unlock()
		if len(atts) > 0 {
			attemptID = atts[len(atts)-1].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if attemptID == "" {
		t.Fatal("task was never dequeued")
	}

	// Simulate lease expiry: the queue reclaims the lease, recording the
	// abandoned attempt as timeout and re-queuing the task — exactly as
	// SQLiteQueue.tryClaim does.
	q.ReclaimLease("t-lease")

	// Provide a result so the worker's Harness.Result returns and it
	// attempts Ack. The lease is gone, so Ack must yield ErrLeaseExpired.
	h.SetOutcome("run-"+attemptID, harness.OutcomeSucceeded, nil)

	// Wait for the worker to recover from the expired lease.
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		attempts := q.attempts["t-lease"]
		if len(attempts) >= 1 && attempts[0].Outcome == queue.OutcomeTimeout {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// W4: assert attempt 1 was recorded as timeout.
	attempts := q.attempts["t-lease"]
	if len(attempts) == 0 {
		t.Fatal("no attempts recorded for lease-expiry test")
	}
	if attempts[0].Outcome != queue.OutcomeTimeout {
		t.Errorf("first attempt outcome: got %s, want %s (timeout)", attempts[0].Outcome, queue.OutcomeTimeout)
	}

	// W4: assert the task was re-queued (not terminal).
	taskResult, _ := q.GetTask(ctx, "t-lease")
	if taskResult.State != queue.StateQueued && taskResult.State != queue.StateRunning {
		t.Errorf("expected task re-queued after lease expiry, got %s", taskResult.State)
	}

	// Worker should still be alive (did not crash on the transient error).
	select {
	case <-ctx.Done():
		t.Error("context cancelled unexpectedly")
	default:
	}

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(1 * time.Second):
	}
}

func TestBackoffApplied(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	// Use short backoff for testing.
	backoffInitial := 100 * time.Millisecond
	backoffMax := 500 * time.Millisecond

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, backoffInitial, backoffMax, log)

	task := makeTask("t-backoff", queue.TierP1)
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	go func() {
		_ = w.Run(ctx)
	}()

	// Wait for the first dequeue.
	deadline := time.Now().Add(2 * time.Second)
	var firstAttemptID string
	for time.Now().Before(deadline) {
		q.mu.Lock()
		atts := q.attempts["t-backoff"]
		q.mu.Unlock()
		if len(atts) > 0 {
			firstAttemptID = atts[len(atts)-1].ID
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if firstAttemptID == "" {
		t.Fatal("task was never dequeued")
	}

	// Set a retryable outcome for the first attempt.
	h.SetOutcome("run-"+firstAttemptID, harness.OutcomeRetryableFailure, nil)

	// Wait for the task to be re-queued (Nack → queued).
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		taskResult, _ := q.GetTask(ctx, "t-backoff")
		if taskResult.State == queue.StateQueued {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Wait for the second dequeue.
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		q.mu.Lock()
		atts := q.attempts["t-backoff"]
		q.mu.Unlock()
		if len(atts) >= 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// W4: assert the second dequeue was delayed by at least the computed
	// backoff. The backoff for attempt 1 is backoffInitial * 2^(1-1) = 100ms.
	times := q.DequeueTimes()
	if len(times) < 2 {
		t.Fatalf("expected at least 2 dequeues, got %d", len(times))
	}
	gap := times[1].Sub(times[0])
	if gap < backoffInitial {
		t.Errorf("backoff not applied: second dequeue after %v, want >= %v", gap, backoffInitial)
	}

	// Let the second attempt succeed so the worker can exit cleanly.
	secondAttemptID := ""
	q.mu.Lock()
	atts := q.attempts["t-backoff"]
	if len(atts) >= 2 {
		secondAttemptID = atts[1].ID
	}
	q.mu.Unlock()
	if secondAttemptID != "" {
		h.SetOutcome("run-"+secondAttemptID, harness.OutcomeSucceeded, nil)
	}

	cancel()
	select {
	case <-ctx.Done():
	case <-time.After(1 * time.Second):
	}
}

// =====================================================================
// P3 sweep worker tests
// =====================================================================

// makeSweepTask creates a P3 sweep task (D44 — ticket is zero-value for P3).
func makeSweepTask(id string) queue.Task {
	return queue.Task{
		ID:           id,
		RepositoryID: "repo",
		Ticket:       queue.TrackerRef{}, // zero value for P3 (D44)
		Tier:         queue.TierP3,
		Prompt:       "sweep prompt",
		State:        queue.StateQueued,
		Budget:       20000,
		TimeoutSeconds: 3600,
		TriggeredBy: queue.TriggeredBy{
			EventType:  "schedule.sweep",
			DedupeKey:  "github:sweep:repo:1234567890",
			ReceivedAt: time.Now().UnixNano(),
		},
		CreatedAt: time.Now().UnixNano(),
	}
}

func TestSweepSucceedsWithReportFindings(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	task := makeSweepTask("t-sweep")
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	// Wait for the task to be dequeued and started.
	time.Sleep(100 * time.Millisecond)

	// Get the attempt ID.
	q.mu.Lock()
	atts := q.attempts["t-sweep"]
	q.mu.Unlock()
	if len(atts) == 0 {
		t.Fatal("task was never dequeued")
	}
	attemptID := atts[len(atts)-1].ID

	// Harness returns a report artifact with two findings.
	findings := []harness.Finding{
		{Title: "Stale TODO in main.go", Description: "TODO added 100 days ago", Category: "stale-TODO", Severity: "medium"},
		{Title: "Stale FIXME in old.go", Description: "FIXME added 200 days ago", Category: "stale-FIXME", Severity: "high"},
	}
	h.SetOutcome("run-"+attemptID, harness.OutcomeSucceeded, []harness.Artifact{
		{Kind: "report", URI: "artifact-store/run-sweep/report", Findings: findings},
		{Kind: "log", URI: "artifact-store/run-sweep/run.log"},
	})

	// Wait for the worker to process the result.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		taskResult, _ := q.GetTask(ctx, "t-sweep")
		if taskResult.State == queue.StateSucceeded {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	taskResult, _ := q.GetTask(ctx, "t-sweep")
	if taskResult.State != queue.StateSucceeded {
		t.Errorf("expected succeeded, got %s", taskResult.State)
	}

	// W1: OpenIssue must be called once per finding, each labelled idle-needs-human.
	calls := sink.OpenIssueCalls()
	if len(calls) != 2 {
		t.Errorf("expected 2 OpenIssue calls, got %d", len(calls))
	} else {
		for _, c := range calls {
			if len(c.labels) != 1 || c.labels[0] != "idle-needs-human" {
				t.Errorf("OpenIssue labels = %v, want [idle-needs-human]", c.labels)
			}
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}
}

// TestSweepPullRequestViolation verifies D43/D44: a sweep that returns a
// pull_request artifact is a non-retryable protocol violation — the task
// is dropped (Nack non-retryable), never escalated, and OpenIssue is never
// called.
func TestSweepPullRequestViolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	task := makeSweepTask("t-sweep-pr")
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)

	q.mu.Lock()
	atts := q.attempts["t-sweep-pr"]
	q.mu.Unlock()
	if len(atts) == 0 {
		t.Fatal("task was never dequeued")
	}
	attemptID := atts[len(atts)-1].ID

	// Harness returns a pull_request artifact — protocol violation.
	h.SetOutcome("run-"+attemptID, harness.OutcomeSucceeded, []harness.Artifact{
		{Kind: "pull_request", URI: "https://github.com/repo/pull/1"},
	})

	// Wait for the worker to process result.
	time.Sleep(500 * time.Millisecond)
	taskResult, _ := q.GetTask(ctx, "t-sweep-pr")
	// The queue marks non-retryable as escalated (terminal).
	if taskResult.State != queue.StateEscalated {
		t.Errorf("expected escalated (dropped), got %s", taskResult.State)
	}

	// No OpenIssue calls, no label writes, no comments — never escalated.
	if len(sink.OpenIssueCalls()) != 0 {
		t.Errorf("expected 0 OpenIssue calls, got %d", len(sink.OpenIssueCalls()))
	}
	if len(sink.labels) != 0 {
		t.Errorf("expected 0 label writes, got %d", len(sink.labels))
	}
	if len(sink.comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(sink.comments))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}
}

// TestSweepFeatureBranchViolation verifies D43/D44/D15: a sweep that returns
// a feature branch is a non-retryable protocol violation — the task is
// dropped, never escalated, and OpenIssue is never called.
func TestSweepFeatureBranchViolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	task := makeSweepTask("t-sweep-branch")
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)

	q.mu.Lock()
	atts := q.attempts["t-sweep-branch"]
	q.mu.Unlock()
	if len(atts) == 0 {
		t.Fatal("task was never dequeued")
	}
	attemptID := atts[len(atts)-1].ID

	// Harness returns a feature branch — protocol violation.
	bp := "feature"
	h.SetOutcome("run-"+attemptID, harness.OutcomeSucceeded, []harness.Artifact{
		{Kind: "branch", BranchPurpose: &bp, URI: "https://github.com/repo/tree/feature/1"},
	})

	// Wait for the worker to process result.
	time.Sleep(500 * time.Millisecond)
	taskResult, _ := q.GetTask(ctx, "t-sweep-branch")
	if taskResult.State != queue.StateEscalated {
		t.Errorf("expected escalated (dropped), got %s", taskResult.State)
	}

	// No OpenIssue calls, no label writes, no comments.
	if len(sink.OpenIssueCalls()) != 0 {
		t.Errorf("expected 0 OpenIssue calls, got %d", len(sink.OpenIssueCalls()))
	}
	if len(sink.labels) != 0 {
		t.Errorf("expected 0 label writes, got %d", len(sink.labels))
	}
	if len(sink.comments) != 0 {
		t.Errorf("expected 0 comments, got %d", len(sink.comments))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}
}

// TestSweepNeverEscalates verifies D45: a sweep task never escalates,
// even on a failure outcome. The sweep Nacks and drops the task
// without calling escalate (no comment, no label, no debug branch).
func TestSweepNeverEscalates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	q := newStubQueue(t)
	h := newStubHarness(t)
	sink := newStubSink(t)
	policy := newStubPolicy(true, nil)

	log := slog.New(slog.DiscardHandler)
	w := NewWorker(q, h, sink, policy, 1, 30*time.Second, 10*time.Minute, log)

	task := makeSweepTask("t-sweep-fail")
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		_ = w.Run(ctx)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)

	q.mu.Lock()
	atts := q.attempts["t-sweep-fail"]
	q.mu.Unlock()
	if len(atts) == 0 {
		t.Fatal("task was never dequeued")
	}
	attemptID := atts[len(atts)-1].ID

	// Harness returns a non-retryable failure on the sweep run.
	h.SetOutcome("run-"+attemptID, harness.OutcomeNonRetryableFailure, []harness.Artifact{
		{Kind: "report", URI: "artifact-store/run-sweep/report"},
	})

	// Wait for the worker to process result.
	time.Sleep(500 * time.Millisecond)
	taskResult, _ := q.GetTask(ctx, "t-sweep-fail")
	// The queue marks non-retryable as escalated (terminal).
	if taskResult.State != queue.StateEscalated {
		t.Errorf("expected escalated (dropped), got %s", taskResult.State)
	}

	// D45: sweep tasks never escalate — no comment, no label, no debug branch.
	if len(sink.comments) != 0 {
		t.Errorf("expected 0 escalation comments, got %d", len(sink.comments))
	}
	if len(sink.labels) != 0 {
		t.Errorf("expected 0 escalation labels, got %d", len(sink.labels))
	}

	cancel()
	select {
	case <-done:
	case <-time.After(1 * time.Second):
	}
}
// Helper for tests
func (q *stubQueue) GetLeaseForTask(taskID string) (queue.Lease, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	lease, ok := q.leases[taskID]
	return lease, ok
}

// Helper for tests: get the recorded OpenIssue calls.
func (s *stubSink) OpenIssueCalls() []struct {
	repo   string
	title  string
	body   string
	labels []string
} {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]struct {
		repo   string
		title  string
		body   string
		labels []string
	}{}, s.openIssues...)
}
