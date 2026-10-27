package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/seppaleinen/idle-deck/queue"
)

func newID() string {
	b := make([]byte, 16)
	_, err := rand.Read(b)
	if err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("%x", b)[:32]
}

type SQLiteQueue struct {
	db      *sql.DB
	now     nowFunc
	lease   time.Duration
	maxA    int
	mu      sync.Mutex
	active  *activeLease
	notify  chan struct{}
	notifyM sync.Mutex
	// subscribers is a map of per-subscription channels for fan-out.
	// Each call to Events(ctx) creates a new channel, registers it here,
	// and removes it on context cancellation.
	// Protected by subsMu.
	subscribers map[chan queue.QueueEvent]struct{}
	subsMu      sync.Mutex
}

type activeLease struct {
	taskID string
	tier   queue.TaskTier
}

func NewSQLiteQueue(dbPath string, opts ...func(*SQLiteQueue)) *SQLiteQueue {
	q := &SQLiteQueue{
		now:         defaultNow,
		lease:       30 * time.Minute,
		notify:      make(chan struct{}, 1),
		subscribers: make(map[chan queue.QueueEvent]struct{}),
	}
	for _, opt := range opts {
		opt(q)
	}
	ctx := context.Background()
	db, err := Open(ctx, dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "store: open DB: %v\n", err)
		os.Exit(1)
	}
	q.db = db
	return q
}

func WithNow(f func() time.Time) func(*SQLiteQueue) { return func(q *SQLiteQueue) { q.now = f } }
func WithLeaseDuration(d time.Duration) func(*SQLiteQueue) {
	return func(q *SQLiteQueue) { q.lease = d }
}
func WithMaxAttempts(n int) func(*SQLiteQueue) { return func(q *SQLiteQueue) { q.maxA = n } }

// broadcastEvent fans out an event to all registered subscriber channels.
// The send is non-blocking: if a subscriber's channel is full, the event is
// dropped for that subscriber (same semantics as the former 1-element buffer,
// but per-subscriber). This preserves the "never block Enqueue" invariant.
func (q *SQLiteQueue) broadcastEvent(ev queue.QueueEvent) {
	q.subsMu.Lock()
	defer q.subsMu.Unlock()
	for ch := range q.subscribers {
		select {
		case ch <- ev:
		default:
		}
	}
}

func (q *SQLiteQueue) Enqueue(ctx context.Context, task queue.Task) error {
	if task.ID == "" {
		return fmt.Errorf("store: task id required")
	}
	if task.TriggeredBy.DedupeKey == "" {
		return fmt.Errorf("store: dedupe_key required")
	}
	t := q.now()
	if task.CreatedAt == 0 {
		task.CreatedAt = t.UnixNano()
	}
	const qInsert = `
INSERT OR IGNORE INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id,
	ticket_external_id, ticket_url, tier, prompt, payload, timeout_seconds,
	budget, state, event_type, dedupe_key, received_at, created_at, derived_from,
	repository_url)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?)`
	_, err := q.db.ExecContext(ctx, qInsert,
		task.ID,
		task.RepositoryID,
		task.Ticket.Tracker,
		task.Ticket.RepositoryID,
		task.Ticket.ExternalID,
		task.Ticket.URL,
		task.Tier,
		task.Prompt,
		task.Payload,
		task.TimeoutSeconds,
		task.Budget,
		task.TriggeredBy.EventType,
		task.TriggeredBy.DedupeKey,
		task.TriggeredBy.ReceivedAt,
		task.CreatedAt,
		task.DerivedFrom,
		task.RepositoryURL,
	)
	if err != nil {
		return fmt.Errorf("store: enqueue insert: %w", err)
	}
	q.mu.Lock()
	al := q.active
	evTier := task.Tier
	q.mu.Unlock()
	if al != nil && queue.TierRank(evTier) < queue.TierRank(al.tier) {
		q.broadcastEvent(queue.QueueEvent{Tier: evTier})
	}
	q.notifyM.Lock()
	notify := q.notify
	q.notifyM.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
	return nil
}

func (q *SQLiteQueue) Dequeue(ctx context.Context) (queue.Lease, error) {
	q.mu.Lock()
	select {
	case <-q.notify:
	default:
	}
	q.mu.Unlock()
	for {
		lease, err := q.tryClaim(ctx)
		if err == nil {
			return lease, nil
		}
		if err != queue.ErrNoWork {
			return queue.Lease{}, err
		}
		select {
		case <-ctx.Done():
			return queue.Lease{}, ctx.Err()
		case <-q.notify:
		}
	}
}

func (q *SQLiteQueue) tryClaim(ctx context.Context) (queue.Lease, error) {
	// Step 1: Reclaim expired leases in a separate transaction. This must
	// commit before the claim scan, so that a reclaim that finds no work to
	// claim still persists (otherwise the deferred Rollback would undo it).
	now := q.now()
	cutoff := now.Add(-q.lease)
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return queue.Lease{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE attempts
   SET outcome = 'timeout', finished_at = ?
WHERE outcome = '' AND started_at IS NOT NULL AND started_at < ?`,
		now.UnixNano(), cutoff.UnixNano()); err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: reclaim: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET state = 'queued'
WHERE state = 'running' AND id IN (
  SELECT a.task_id FROM attempts a
  WHERE a.outcome = 'timeout' AND a.started_at IS NOT NULL AND a.started_at < ?)`,
		cutoff.UnixNano()); err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: reclaim requeue: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return queue.Lease{}, fmt.Errorf("store: reclaim commit: %w", err)
	}

	// Step 2: Claim the highest-tier queued task atomically.
	tx, err = q.db.BeginTx(ctx, nil)
	if err != nil {
		return queue.Lease{}, err
	}
	defer tx.Rollback()
	var taskID, tier string
	var created int64
	var taskTier, taskTimeout int
	rankingSQL := queue.TierOrderingSQL()
	err = tx.QueryRowContext(ctx, fmt.Sprintf(`
SELECT id, tier,
       %s,
       created_at,
       timeout_seconds
  FROM tasks
 WHERE state = 'queued'
 ORDER BY %s ASC, created_at ASC, id ASC
 LIMIT 1
`, rankingSQL, rankingSQL)).Scan(&taskID, &tier, &taskTier, &created, &taskTimeout)
	if err == sql.ErrNoRows {
		tx.Rollback()
		return queue.Lease{}, queue.ErrNoWork
	}
	if err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: claim scan: %w", err)
	}
	attemptID := newID()
	expiry := now.Add(q.lease)
	// Effective timeout (D17, D37): task override wins; otherwise the tier
	// default from queue.TierMetaTable. Recorded on the attempt so the
	// attempts ledger is truthful (M3: no more -1 sentinel).
	effectiveTimeout := taskTimeout
	if effectiveTimeout <= 0 {
		effectiveTimeout = queue.DefaultTimeoutForTier(queue.TaskTier(tier))
	}
	var ordinal int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), 0) + 1 FROM attempts WHERE task_id = ?`, taskID).Scan(&ordinal)
	if err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: ordinal scan: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO attempts (id, task_id, ordinal, outcome, started_at, timeout_seconds, remote_session_id)
VALUES (?, ?, ?, '', ?, ?, '')`,
		attemptID, taskID, ordinal, now.UnixNano(), taskTimeout)
	if err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: attempt insert: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'running' WHERE id = ? AND state = 'queued'`, taskID)
	if err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: set running: %w", err)
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return queue.Lease{}, queue.ErrDoubleClaim
	}
	q.mu.Lock()
	q.active = &activeLease{taskID: taskID, tier: queue.TaskTier(tier)}
	q.mu.Unlock()
	if err := tx.Commit(); err != nil {
		return queue.Lease{}, fmt.Errorf("store: commit: %w", err)
	}
	q.notifyM.Lock()
	notify := q.notify
	q.notifyM.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
	return queue.Lease{
		TaskID:    taskID,
		AttemptID: attemptID,
		ExpiresAt: expiry.UnixNano(),
		Tier:      queue.TaskTier(tier),
	}, nil
}

func (q *SQLiteQueue) Ack(ctx context.Context, lease queue.Lease, attemptID string) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := q.now()
	res, err := tx.ExecContext(ctx, `
UPDATE attempts
   SET outcome = 'succeeded', finished_at = ?
WHERE id = ? AND task_id = ? AND outcome = ''
`,
		now.UnixNano(), attemptID, lease.TaskID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: ack update attempt: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return queue.ErrLeaseExpired
	}
	res2, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'succeeded' WHERE id = ?`, lease.TaskID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: ack update task: %w", err)
	}
	n2, _ := res2.RowsAffected()
	if n2 == 0 {
		tx.Rollback()
		return fmt.Errorf("store: no task %q updated", lease.TaskID)
	}
	q.mu.Lock()
	if q.active != nil && q.active.taskID == lease.TaskID {
		q.active = nil
	}
	q.mu.Unlock()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: ack commit: %w", err)
	}
	q.notifyM.Lock()
	notify := q.notify
	q.mu.Lock()
	if q.active != nil && q.active.taskID == lease.TaskID {
		q.active = nil
	}
	q.mu.Unlock()
	q.notifyM.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
	return nil
}

func (q *SQLiteQueue) Nack(ctx context.Context, lease queue.Lease, attemptID string, outcome queue.AttemptOutcome) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := q.now()
	res, err := tx.ExecContext(ctx, `
UPDATE attempts
   SET outcome = ?, finished_at = ?
WHERE id = ? AND task_id = ? AND outcome = ''
`,
		string(outcome), now.UnixNano(), attemptID, lease.TaskID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: nack update attempt: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return queue.ErrLeaseExpired
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id = ?`, lease.TaskID).Scan(&count)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: nack count: %w", err)
	}
	retryable := outcome == queue.OutcomeRetryableFailure ||
		outcome == queue.OutcomeTimeout ||
		outcome == queue.OutcomePreempted
	var state queue.TaskState
	if retryable && count < q.maxA {
		state = queue.StateQueued
	} else {
		state = queue.StateEscalated
	}
	res, err = tx.ExecContext(ctx, `UPDATE tasks SET state = ? WHERE id = ?`, state, lease.TaskID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: nack update task: %w", err)
	}
	n2, _ := res.RowsAffected()
	if n2 == 0 {
		tx.Rollback()
		return fmt.Errorf("store: no task %q updated", lease.TaskID)
	}
	q.mu.Lock()
	if q.active != nil && q.active.taskID == lease.TaskID {
		q.active = nil
	}
	q.mu.Unlock()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: nack commit: %w", err)
	}
	q.notifyM.Lock()
	notify := q.notify
	q.notifyM.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
	return nil
}

func (q *SQLiteQueue) Events(ctx context.Context) (<-chan queue.QueueEvent, error) {
	ch := make(chan queue.QueueEvent, 16)
	q.subsMu.Lock()
	q.subscribers[ch] = struct{}{}
	q.subsMu.Unlock()
	// Unregister and close the channel when the caller's context is done.
	// This prevents unbounded subscriber growth on churn.
	go func() {
		<-ctx.Done()
		q.subsMu.Lock()
		delete(q.subscribers, ch)
		q.subsMu.Unlock()
		close(ch)
	}()
	return ch, nil
}

// DB returns the underlying *sql.DB handle. It is used by main.go to construct
// the watermark store and to run read-only status queries. The queue retains
// ownership of the handle: callers must not close it.
func (q *SQLiteQueue) DB() *sql.DB {
	return q.db
}

// CurrentAttempt returns the active attempt for a running task (the one
// with outcome = ”). Returns queue.ErrNotFound if the task has no running
// attempt. Used by dashboard cancel actions to route through the queue
// seam rather than raw SQL (D24, I10).
func (q *SQLiteQueue) CurrentAttempt(ctx context.Context, taskID string) (queue.AttemptRef, error) {
	var ref queue.AttemptRef
	var ordinal int
	err := q.db.QueryRowContext(ctx, `
		SELECT id, ordinal FROM attempts
		WHERE task_id = ? AND outcome = ''
		ORDER BY ordinal DESC LIMIT 1
	`, taskID).Scan(&ref.AttemptID, &ordinal)
	if err == sql.ErrNoRows {
		return queue.AttemptRef{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.AttemptRef{}, fmt.Errorf("store: current attempt: %w", err)
	}
	ref.TaskID = taskID
	ref.Ordinal = ordinal
	return ref, nil
}

// CancelTask atomically records a user-initiated cancellation on a running
// task's current attempt. It honours the lease (D24): the attempt is
// recorded as preempted, consuming one retry (D17), and the task follows
// normal retry/escalation (D14). Returns queue.ErrNotFound if the task is
// not running. This is the seam method a dashboard cancel must use — never
// a raw SQL write to tasks/attempts.
func (q *SQLiteQueue) CancelTask(ctx context.Context, taskID string) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := q.now()
	res, err := tx.ExecContext(ctx, `
		UPDATE attempts
		   SET outcome = 'preempted', finished_at = ?
		WHERE task_id = ? AND outcome = ''
	`, now.UnixNano(), taskID)
	if err != nil {
		return fmt.Errorf("store: cancel attempt: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return queue.ErrNotFound
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id = ?`, taskID).Scan(&count)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: cancel count: %w", err)
	}
	// Preempted is retryable (D17): re-queue if under maxAttempts, escalate
	// otherwise. This is the same path a P0 preemption uses (D16).
	var state queue.TaskState
	if count < q.maxA {
		state = queue.StateQueued
	} else {
		state = queue.StateEscalated
	}
	res, err = tx.ExecContext(ctx, `UPDATE tasks SET state = ? WHERE id = ?`, state, taskID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: cancel task: %w", err)
	}
	n2, _ := res.RowsAffected()
	if n2 == 0 {
		tx.Rollback()
		return fmt.Errorf("store: no task %q updated", taskID)
	}
	q.mu.Lock()
	if q.active != nil && q.active.taskID == taskID {
		q.active = nil
	}
	q.mu.Unlock()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: cancel commit: %w", err)
	}
	q.notifyM.Lock()
	notify := q.notify
	q.notifyM.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
	return nil
}

// GetTask returns the full Task for the given id. The worker fetches task
// details (prompt, budget, ticket, tier) after dequeuing, because Dequeue
// returns only a Lease.
func (q *SQLiteQueue) GetTask(ctx context.Context, taskID string) (queue.Task, error) {
	var t queue.Task
	var ticketTracker, ticketRepoID, ticketExternalID, ticketURL string
	var triggeredEventType, triggeredDedupeKey string
	var triggeredReceivedAt int64
	var derivedFrom sql.NullString
	var repositoryURL sql.NullString
	var eventType string
	var dedupeKey string
	var receivedAt int64
	var createdAt int64
	err := q.db.QueryRowContext(ctx, `
SELECT id, repository_id, ticket_tracker, ticket_repo_id,
       ticket_external_id, ticket_url, tier, prompt, payload,
       timeout_seconds, budget, state, event_type, dedupe_key,
       received_at, created_at, triggered_by_event_type, triggered_by_dedupe_key,
       triggered_by_received_at, repository_url
  FROM tasks WHERE id = ?`, taskID).Scan(
		&t.ID, &t.RepositoryID, &ticketTracker, &ticketRepoID,
		&ticketExternalID, &ticketURL, &t.Tier, &t.Prompt, &t.Payload,
		&t.TimeoutSeconds, &t.Budget, &t.State, &eventType, &dedupeKey,
		&receivedAt, &createdAt, &triggeredEventType, &triggeredDedupeKey,
		&triggeredReceivedAt, &repositoryURL,
	)
	if err == sql.ErrNoRows {
		return queue.Task{}, queue.ErrNotFound
	}
	if err != nil {
		return queue.Task{}, fmt.Errorf("store: get task: %w", err)
	}
	t.Ticket = queue.TrackerRef{
		Tracker:      ticketTracker,
		RepositoryID: ticketRepoID,
		ExternalID:   ticketExternalID,
		URL:          ticketURL,
	}
	t.TriggeredBy = queue.TriggeredBy{
		EventType:  triggeredEventType,
		DedupeKey:  triggeredDedupeKey,
		ReceivedAt: triggeredReceivedAt,
	}
	if derivedFrom.Valid {
		t.DerivedFrom = derivedFrom.String
	}
	if repositoryURL.Valid {
		t.RepositoryURL = repositoryURL.String
	}
	return t, nil
}

// SaveArtifacts persists harness-returned artifacts for an attempt. It is
// idempotent: re-saving the same attempt's artifacts is a no-op (the worker
// may call it on both the success and the escalation paths).
func (q *SQLiteQueue) SaveArtifacts(ctx context.Context, attemptID string, artifacts []queue.Artifact) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := q.now()
	for _, a := range artifacts {
		if a.ID == "" {
			a.ID = newID()
		}
		if a.ProducedAt == 0 {
			a.ProducedAt = now.UnixNano()
		}
		var bp sql.NullString
		if a.BranchPurpose != "" {
			bp.String = string(a.BranchPurpose)
			bp.Valid = true
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO artifacts (id, attempt_id, kind, branch_purpose, uri, produced_at)
VALUES (?, ?, ?, ?, ?, ?)`,
			a.ID, attemptID, string(a.Kind), bp, a.URI, a.ProducedAt)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("store: save artifact: %w", err)
		}
	}
	return tx.Commit()
}

// AttemptCount returns the number of attempts with a non-empty outcome for a task.
func (q *SQLiteQueue) AttemptCount(ctx context.Context, taskID string) (int, error) {
	var count int
	err := q.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM attempts WHERE task_id = ? AND outcome != ''`, taskID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("store: attempt count: %w", err)
	}
	return count, nil
}

func (q *SQLiteQueue) Release(ctx context.Context, lease queue.Lease) error {
	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `
DELETE FROM attempts WHERE id = ? AND outcome = '',
    task_id = ?`, lease.AttemptID, lease.TaskID)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("store: release delete: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		tx.Rollback()
		return queue.ErrLeaseExpired
	}
	if _, err := tx.ExecContext(ctx, `UPDATE tasks SET state = 'queued' WHERE id = ?`, lease.TaskID); err != nil {
		tx.Rollback()
		return fmt.Errorf("store: release re-queue: %w", err)
	}
	q.mu.Lock()
	if q.active != nil && q.active.taskID == lease.TaskID {
		q.active = nil
	}
	q.mu.Unlock()
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: release commit: %w", err)
	}
	q.notifyM.Lock()
	notify := q.notify
	q.notifyM.Unlock()
	select {
	case notify <- struct{}{}:
	default:
	}
	return nil
}

// WatermarkStore provides per-repository watermark cursor access
// for the tracker adapter (event-contract §2).
type WatermarkStore interface {
	// Load returns the watermark for the given repo.
	// Returns zero time and false if no watermark has been seeded.
	Load(ctx context.Context, repo string) (time.Time, bool, error)
	// Save upserts the watermark for the given repo.
	// The watermark is advanced to the tick start (event-contract §2).
	Save(ctx context.Context, repo string, t time.Time) error
	// LoadETag returns the ETag string for a specific query type within a repo.
	// Returns empty string if no ETag has been stored.
	LoadETag(ctx context.Context, repo string, queryID string) (string, error)
	// SaveETag persists the ETag for a specific query type within a repo.
	// The ETag is stored alongside the watermark.
	SaveETag(ctx context.Context, repo string, queryID string, etag string) error
}

// SQLiteWatermarks implements WatermarkStore using the tracker_watermarks table.
type SQLiteWatermarks struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteWatermarks(db *sql.DB) *SQLiteWatermarks {
	return &SQLiteWatermarks{db: db, now: time.Now}
}

func (w *SQLiteWatermarks) Load(ctx context.Context, repo string) (time.Time, bool, error) {
	var s string
	err := w.db.QueryRowContext(ctx,
		`SELECT watermark FROM tracker_watermarks WHERE repo = ?`, repo).Scan(&s)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("watermark: load: %w", err)
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("watermark: parse: %w", err)
	}
	return t, true, nil
}

func (w *SQLiteWatermarks) Save(ctx context.Context, repo string, t time.Time) error {
	_, err := w.db.ExecContext(ctx, `
INSERT INTO tracker_watermarks (repo, watermark, updated_at)
VALUES (?, ?, ?)
ON CONFLICT(repo) DO UPDATE SET
    watermark = excluded.watermark,
    updated_at = excluded.updated_at`,
		repo, t.Format(time.RFC3339Nano), w.now().UnixNano())
	if err != nil {
		return fmt.Errorf("watermark: save: %w", err)
	}
	return nil
}

func (w *SQLiteWatermarks) LoadETag(ctx context.Context, repo string, queryID string) (string, error) {
	var etagJSON sql.NullString
	err := w.db.QueryRowContext(ctx,
		`SELECT etag FROM tracker_watermarks WHERE repo = ?`, repo).Scan(&etagJSON)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("watermark: load etag: %w", err)
	}
	if !etagJSON.Valid || etagJSON.String == "" {
		return "", nil
	}
	var etags map[string]string
	if err := json.Unmarshal([]byte(etagJSON.String), &etags); err != nil {
		return "", fmt.Errorf("watermark: parse etag json: %w", err)
	}
	return etags[queryID], nil
}

func (w *SQLiteWatermarks) SaveETag(ctx context.Context, repo string, queryID string, etag string) error {
	if etag == "" {
		return nil
	}
	// Load existing etags
	var etagJSON sql.NullString
	err := w.db.QueryRowContext(ctx,
		`SELECT etag FROM tracker_watermarks WHERE repo = ?`, repo).Scan(&etagJSON)

	etags := make(map[string]string)
	if err == nil && etagJSON.Valid && etagJSON.String != "" {
		if err := json.Unmarshal([]byte(etagJSON.String), &etags); err == nil {
			// use existing
		}
	} else if err != nil && err != sql.ErrNoRows {
		return fmt.Errorf("watermark: load etag for save: %w", err)
	}

	etags[queryID] = etag
	updatedJSON, err := json.Marshal(etags)
	if err != nil {
		return fmt.Errorf("watermark: marshal etag json: %w", err)
	}

	_, err = w.db.ExecContext(ctx, `
UPDATE tracker_watermarks SET etag = ? WHERE repo = ?`,
		string(updatedJSON), repo)
	if err != nil {
		return fmt.Errorf("watermark: save etag: %w", err)
	}
	return nil
}

// HeartbeatStore provides daemon liveness + run-lock state (operator-surface §6).
// The heartbeat row answers both "is it alive" and "how long has it been silent"
// for free, with no new mechanism (operator-surface §6).
type HeartbeatStore interface {
	// Beat records a heartbeat at the given time. Always succeeds (upsert).
	Beat(ctx context.Context, t time.Time) error
	// LastBeat returns the most recent heartbeat time. Zero time if none.
	LastBeat(ctx context.Context) (time.Time, error)
	// TakeRunLock atomically claims the run lock (idempotent: returns true if
	// it was already held by this caller). Used by the daemon to mark itself
	// as the active run holder.
	TakeRunLock(ctx context.Context) (bool, error)
	// ReleaseRunLock clears the run lock.
	ReleaseRunLock(ctx context.Context) error
	// RunLockHeld reports whether the run lock is currently held.
	RunLockHeld(ctx context.Context) (bool, error)
	// LastPoll returns the last successful poll timestamp for a repo.
	LastPoll(ctx context.Context, repo string) (time.Time, bool, error)
	// SavePoll records a successful poll for a repo.
	SavePoll(ctx context.Context, repo string, t time.Time) error
}

// SQLiteHeartbeat implements HeartbeatStore on the same SQLite queue database.
type SQLiteHeartbeat struct {
	db  *sql.DB
	now func() time.Time
}

func NewSQLiteHeartbeat(db *sql.DB) *SQLiteHeartbeat {
	return &SQLiteHeartbeat{db: db, now: time.Now}
}

func (h *SQLiteHeartbeat) Beat(ctx context.Context, t time.Time) error {
	_, err := h.db.ExecContext(ctx, `
INSERT INTO heartbeat (id, beat_at, run_lock) VALUES (1, ?, 0)
ON CONFLICT(id) DO UPDATE SET beat_at = excluded.beat_at`,
		t.UnixNano())
	return err
}

func (h *SQLiteHeartbeat) LastBeat(ctx context.Context) (time.Time, error) {
	var n int64
	err := h.db.QueryRowContext(ctx, `SELECT beat_at FROM heartbeat WHERE id = 1`).Scan(&n)
	if err == sql.ErrNoRows {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("heartbeat: last beat: %w", err)
	}
	return time.Unix(0, n), nil
}

func (h *SQLiteHeartbeat) TakeRunLock(ctx context.Context) (bool, error) {
	var held int
	err := h.db.QueryRowContext(ctx, `SELECT run_lock FROM heartbeat WHERE id = 1`).Scan(&held)
	if err == sql.ErrNoRows {
		_, err = h.db.ExecContext(ctx, `INSERT INTO heartbeat (id, beat_at, run_lock) VALUES (1, ?, 1)`, h.now().UnixNano())
		return err == nil, err
	}
	if err != nil {
		return false, fmt.Errorf("heartbeat: take lock: %w", err)
	}
	if held != 0 {
		return false, nil
	}
	_, err = h.db.ExecContext(ctx, `UPDATE heartbeat SET run_lock = 1 WHERE id = 1`)
	return err == nil, err
}

func (h *SQLiteHeartbeat) ReleaseRunLock(ctx context.Context) error {
	_, err := h.db.ExecContext(ctx, `UPDATE heartbeat SET run_lock = 0 WHERE id = 1`)
	return err
}

func (h *SQLiteHeartbeat) RunLockHeld(ctx context.Context) (bool, error) {
	var held int
	err := h.db.QueryRowContext(ctx, `SELECT run_lock FROM heartbeat WHERE id = 1`).Scan(&held)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("heartbeat: lock held: %w", err)
	}
	return held != 0, nil
}

func (h *SQLiteHeartbeat) LastPoll(ctx context.Context, repo string) (time.Time, bool, error) {
	var n int64
	err := h.db.QueryRowContext(ctx, `SELECT last_poll FROM tracker_polls WHERE repo = ?`, repo).Scan(&n)
	if err == sql.ErrNoRows {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("poll: last: %w", err)
	}
	return time.Unix(0, n), true, nil
}

func (h *SQLiteHeartbeat) SavePoll(ctx context.Context, repo string, t time.Time) error {
	_, err := h.db.ExecContext(ctx, `
INSERT INTO tracker_polls (repo, last_poll) VALUES (?, ?)
ON CONFLICT(repo) DO UPDATE SET last_poll = excluded.last_poll`,
		repo, t.UnixNano())
	return err
}
