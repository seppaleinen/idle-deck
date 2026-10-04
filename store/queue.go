package store

import (
	"context"
	"crypto/rand"
	"database/sql"
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
	db     *sql.DB
	now    nowFunc
	lease  time.Duration
	maxA   int
	mu     sync.Mutex
	active *activeLease
	events chan queue.QueueEvent
	notify chan struct{}
	notifyM sync.Mutex
}

type activeLease struct {
	taskID string
	tier   queue.TaskTier
}

func NewSQLiteQueue(dbPath string, opts ...func(*SQLiteQueue)) *SQLiteQueue {
	q := &SQLiteQueue{
		now:    defaultNow,
		lease:  30 * time.Minute,
		events: make(chan queue.QueueEvent, 1),
		notify: make(chan struct{}, 1),
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
func WithLeaseDuration(d time.Duration) func(*SQLiteQueue) { return func(q *SQLiteQueue) { q.lease = d } }
func WithMaxAttempts(n int) func(*SQLiteQueue) { return func(q *SQLiteQueue) { q.maxA = n } }

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
	tierRank := func(t queue.TaskTier) int {
		switch t {
		case queue.TierP0: return 0
		case queue.TierP1: return 1
		case queue.TierP2: return 2
		case queue.TierP3: return 3
		default: return 99
		}
	}
	const qInsert = `
INSERT OR IGNORE INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id,
	ticket_external_id, ticket_url, tier, prompt, payload, timeout_seconds,
	budget, state, event_type, dedupe_key, received_at, created_at, derived_from)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?)`
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
	)
	if err != nil {
		return fmt.Errorf("store: enqueue insert: %w", err)
	}
	q.mu.Lock()
	al := q.active
	evTier := task.Tier
	q.mu.Unlock()
	if al != nil && tierRank(evTier) < tierRank(al.tier) {
		select {
		case q.events <- queue.QueueEvent{Tier: evTier}:
		default:
		}
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
		now.Format(time.RFC3339Nano), cutoff.Format(time.RFC3339Nano)); err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: reclaim: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE tasks SET state = 'queued'
WHERE state = 'running' AND id IN (
  SELECT a.task_id FROM attempts a
  WHERE a.outcome = 'timeout' AND a.started_at IS NOT NULL AND a.started_at < ?)`,
		cutoff.Format(time.RFC3339Nano)); err != nil {
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
	var taskTier int
	err = tx.QueryRowContext(ctx, `
SELECT id, tier,
       CASE tier WHEN 'P0' THEN 0 WHEN 'P1' THEN 1 WHEN 'P2' THEN 2 WHEN 'P3' THEN 3 ELSE 99 END,
       created_at
  FROM tasks
 WHERE state = 'queued'
 ORDER BY CASE tier WHEN 'P0' THEN 0 WHEN 'P1' THEN 1 WHEN 'P2' THEN 2 WHEN 'P3' THEN 3 ELSE 99 END ASC, created_at ASC, id ASC
 LIMIT 1
`).Scan(&taskID, &tier, &taskTier, &created)
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
	var ordinal int
	err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(ordinal), 0) + 1 FROM attempts WHERE task_id = ?`, taskID).Scan(&ordinal)
	if err != nil {
		tx.Rollback()
		return queue.Lease{}, fmt.Errorf("store: ordinal scan: %w", err)
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO attempts (id, task_id, ordinal, outcome, started_at, timeout_seconds, remote_session_id)
VALUES (?, ?, ?, '', ?, ?, '')`,
		attemptID, taskID, ordinal, now.Format(time.RFC3339Nano), -1)
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
		now.Format(time.RFC3339Nano), attemptID, lease.TaskID)
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
	select {
	case <-q.events:
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
		string(outcome), now.Format(time.RFC3339Nano), attemptID, lease.TaskID)
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
	select {
	case <-q.events:
	default:
	}
	return nil
}

func (q *SQLiteQueue) Events(ctx context.Context) (<-chan queue.QueueEvent, error) {
	return q.events, nil
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