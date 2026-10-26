package store

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	"github.com/seppaleinen/idle-deck/queue"
)

func newTestStore(t *testing.T) *SQLiteQueue {
	t.Helper()
	dbPath := t.TempDir() + "/test.db"
	s := NewSQLiteQueue(dbPath, WithLeaseDuration(50*time.Millisecond))
	t.Cleanup(func() { s.db.Close() })
	return s
}

func makeTask(t *testing.T, id string, tier queue.TaskTier, dedupeKey string) queue.Task {
	t.Helper()
	now := time.Now().UnixNano()
	return queue.Task{
		ID:           id,
		RepositoryID: "repo-1",
		Ticket:       queue.TrackerRef{Tracker: "github", RepositoryID: "repo-1", ExternalID: "1", URL: "https://github.com/acme/test/issues/1"},
		Tier:         tier,
		Prompt:       "test prompt",
		TriggeredBy: queue.TriggeredBy{
			EventType:  "test",
			DedupeKey:  dedupeKey,
			ReceivedAt: time.Now().UnixNano(),
		},
		CreatedAt: now,
	}
}

// TestDequeueBlocksAndNoDoubleClaim tests that Dequeue atomically claims
// exactly one task — no double-claim path exists.
func TestDequeueBlocksAndNoDoubleClaim(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	s := newTestStore(t)
	task := makeTask(t, "t1", queue.TierP2, "key1")
	if err := s.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	var lease1, lease2 queue.Lease
	var err1, err2 error
	done := make(chan struct{}, 2)

	go func() {
		l, e := s.Dequeue(ctx)
		lease1, err1 = l, e
		done <- struct{}{}
	}()
	go func() {
		l, e := s.Dequeue(ctx)
		lease2, err2 = l, e
		done <- struct{}{}
	}()

	<-done
	<-done

	// Exactly one should succeed, the other should either block (timeout) or error
	// We don't care which goroutine gets the lease, just that there's no double-claim
	successCount := 0
	if err1 == nil {
		successCount++
	}
	if err2 == nil {
		successCount++
	}
	if successCount != 1 {
		t.Errorf("expected exactly one successful dequeue, got %d (err1=%v err2=%v)", successCount, err1, err2)
	}
	if successCount == 1 && lease1.TaskID == lease2.TaskID && lease1.TaskID != "" {
		t.Errorf("double claim: both got %s", lease1.TaskID)
	}
}

// TestLeaseExpiryRecordsTimeout tests that a lease expiry is recorded as a timeout
// outcome, the attempt count is consumed, and the task is re-queued. Reclaim only
// happens inside Dequeue (no background goroutine per the lease contract).
func TestLeaseExpiryRecordsTimeout(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	task := makeTask(t, "t1", queue.TierP2, "key2")
	if err := s.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Dequeue(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_ = lease
	time.Sleep(150 * time.Millisecond)
	// Trigger reclaim by calling Dequeue again — reclaim happens inside Dequeue
	// We don't check the returned lease; we just need reclaim to fire.
	// After the call, attempt 1 should be timeout.
	_, _ = s.Dequeue(ctx)

	var outcome string
	err = s.db.QueryRowContext(ctx, "SELECT outcome FROM attempts WHERE task_id = ? AND ordinal = 1", task.ID).Scan(&outcome)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "timeout" {
		t.Errorf("expected outcome = timeout, got %q", outcome)
	}
	// The task is re-claimed by the second Dequeue, so state is 'running'.
	// The important assertion is that attempt 1 outcome is 'timeout'.
}

// TestEnqueueIdempotentSameKeyDifferentID is the real invariant: two tasks
// with the same dedupe_key but different IDs must not coexist. The existing
// TestEnqueueIdempotent only enqueues the same task twice, which exercises the
// primary-key path; this one exercises the dedupe-key path that the
// boundaries contract requires (boundaries.md §2).
func TestEnqueueIdempotentSameKeyDifferentID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Same dedupe_key, different IDs — the second must be ignored.
	if err := s.Enqueue(ctx, makeTask(t, "t1", queue.TierP2, "sharedkey")); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, makeTask(t, "t2", queue.TierP0, "sharedkey")); err != nil {
		t.Fatal(err)
	}

	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE dedupe_key = ?`, "sharedkey").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 task with dedupe_key, got %d — Enqueue must be idempotent per key regardless of task ID", count)
	}

	// The surviving task is the first one enqueued.
	var id string
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM tasks WHERE dedupe_key = ?`, "sharedkey").Scan(&id); err != nil {
		t.Fatal(err)
	}
	if id != "t1" {
		t.Errorf("surviving task = %q, want t1 (first enqueue wins)", id)
	}
}

func TestEnqueueIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	task := makeTask(t, "t1", queue.TierP2, "dupkey")
	if err := s.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}

	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE dedupe_key = ?`, task.TriggeredBy.DedupeKey).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 task with dedupe_key, got %d", count)
	}
}

// TestOrdering tests that P0 is dequeued before P2, and FIFO within tier.
func TestOrdering(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Enqueue(ctx, makeTask(t, "p2a", queue.TierP2, "p2a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, makeTask(t, "p0a", queue.TierP0, "p0a")); err != nil {
		t.Fatal(err)
	}
	if err := s.Enqueue(ctx, makeTask(t, "p2b", queue.TierP2, "p2b")); err != nil {
		t.Fatal(err)
	}

	l1, err := s.Dequeue(ctx)
	if err != nil || l1.TaskID != "p0a" {
		t.Errorf("expected P0 first, got %s (err=%v)", l1.TaskID, err)
	}
	l2, err := s.Dequeue(ctx)
	if err != nil || l2.TaskID != "p2a" {
		t.Errorf("expected P2a second, got %s (err=%v)", l2.TaskID, err)
	}
	l3, err := s.Dequeue(ctx)
	if err != nil || l3.TaskID != "p2b" {
		t.Errorf("expected P2b third, got %s (err=%v)", l3.TaskID, err)
	}
}

// TestEventsPreemption tests that a higher-tier enqueue emits an event on Events.
func TestEventsPreemption(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if err := s.Enqueue(ctx, makeTask(t, "low", queue.TierP2, "low")); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Dequeue(ctx)
	if err != nil {
		t.Fatal(err)
	}

	events, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	if err := s.Enqueue(ctx, makeTask(t, "high", queue.TierP0, "high")); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		if ev.Tier != queue.TierP0 {
			t.Errorf("expected P0 event, got %v", ev.Tier)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no preemption event received")
	}
	s.Ack(ctx, lease, lease.AttemptID)
}

// TestMigrationsForwardOnly tests that schema migrations are forward-only and
// preserve old data. Run with: go test ./store -run TestMigrationsForwardOnly -count=1
func TestMigrationsForwardOnly(t *testing.T) {
	dbPath := t.TempDir() + "/migrate.db"
	ctx := context.Background()

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Create an old schema matching the actual v1 (without v2/v3/v4+ additions).
	// This is NOT a fantasy minimal schema — it matches what v1 produces so that
	// migrations v2-v9 see the columns they expect.
	_, err = db.ExecContext(ctx, `
CREATE TABLE tasks (
	id               TEXT PRIMARY KEY,
	repository_id    TEXT NOT NULL,
	ticket_tracker   TEXT NOT NULL,
	ticket_repo_id   TEXT NOT NULL,
	ticket_external_id  TEXT NOT NULL,
	ticket_url       TEXT NOT NULL,
	tier             TEXT NOT NULL,
	prompt           TEXT NOT NULL,
	payload          TEXT NOT NULL DEFAULT '',
	timeout_seconds  INTEGER NOT NULL DEFAULT 0,
	budget           INTEGER NOT NULL DEFAULT 0,
	state            TEXT NOT NULL DEFAULT 'queued',
	event_type       TEXT NOT NULL,
	dedupe_key       TEXT NOT NULL,
	received_at      INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX idx_tasks_dedupe ON tasks (dedupe_key);
`)
	if err != nil {
		t.Fatal(err)
	}

	_, err = db.ExecContext(ctx, `INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url, tier, prompt, state, event_type, dedupe_key, received_at, created_at) VALUES ('old', 'repo', 'gh', 'repo', '1', 'https://x/1', 'P2', 'old', 'queued', 'test', 'oldkey', 1, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()

	db2, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()

	if err := migrate(ctx, db2); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	var version int
	err = db2.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&version)
	if err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Errorf("schema version = %d, want %d", version, SchemaVersion)
	}

	var count int
	err = db2.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE id = 'old'`).Scan(&count)
	if err != nil || count != 1 {
		t.Errorf("old task not preserved: count=%d err=%v", count, err)
	}

	hasArtifacts := false
	rows, _ := db2.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='artifacts'`)
	if rows.Next() {
		hasArtifacts = true
	}
	rows.Close()
	if !hasArtifacts {
		t.Error("artifacts table not created by migration")
	}

	hasVisits := false
	rows, _ = db2.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name='dashboard_visits'`)
	if rows.Next() {
		hasVisits = true
	}
	rows.Close()
	if !hasVisits {
		t.Error("dashboard_visits table not created by migration (v10)")
	}
}

// TestDashboardVisitsStore exercises the review-inbox marker (issue #36):
// GetLastSeen returns zero time before any update; UpdateLastSeen upserts
// the single marker row and GetLastSeen reads it back.
func TestDashboardVisitsStore(t *testing.T) {
	ctx := context.Background()
	dbPath := t.TempDir() + "/visits.db"
	db, err := Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	v := NewSQLiteDashboardVisits(db)

	// Never seen: zero time, no error.
	t0, err := v.GetLastSeen(ctx)
	if err != nil {
		t.Fatalf("GetLastSeen before update: %v", err)
	}
	if !t0.IsZero() {
		t.Errorf("GetLastSeen before update = %v, want zero", t0)
	}

	// UpdateLastSeen upserts.
	now := time.Now().UTC()
	if err := v.UpdateLastSeen(ctx, now); err != nil {
		t.Fatalf("UpdateLastSeen: %v", err)
	}
	t1, err := v.GetLastSeen(ctx)
	if err != nil {
		t.Fatalf("GetLastSeen after update: %v", err)
	}
	if !t1.Equal(now) {
		t.Errorf("GetLastSeen after update = %v, want %v", t1, now)
	}

	// UpdateLastSeen is idempotent per row (ON CONFLICT upsert).
	later := now.Add(time.Hour)
	if err := v.UpdateLastSeen(ctx, later); err != nil {
		t.Fatalf("UpdateLastSeen second: %v", err)
	}
	t2, err := v.GetLastSeen(ctx)
	if err != nil {
		t.Fatalf("GetLastSeen after second update: %v", err)
	}
	if !t2.Equal(later) {
		t.Errorf("GetLastSeen after second update = %v, want %v", t2, later)
	}

	// Only one marker row exists.
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM dashboard_visits`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 dashboard_visits row, got %d", count)
	}
}

// TestEventsFanOut verifies that concurrent Events callers each receive
// every broadcast event (acceptance criterion 1 from H3 fix).
func TestEventsFanOut(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Dequeue a P2 task so an active lease exists (required for Enqueue to broadcast).
	if err := s.Enqueue(ctx, makeTask(t, "low", queue.TierP2, "lowkey")); err != nil {
		t.Fatal(err)
	}
	lease, err := s.Dequeue(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Two subscribers.
	ev1, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ev2, err := s.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Enqueue a P0 task — both subscribers must observe it.
	if err := s.Enqueue(ctx, makeTask(t, "high", queue.TierP0, "highkey")); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	seen1, seen2 := false, false
	wg.Add(2)
	go func() {
		defer wg.Done()
		for ev := range ev1 {
			if ev.Tier == queue.TierP0 {
				seen1 = true
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for ev := range ev2 {
			if ev.Tier == queue.TierP0 {
				seen2 = true
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for fan-out events")
	}

	if !seen1 {
		t.Error("subscriber 1 did not observe the P0 event")
	}
	if !seen2 {
		t.Error("subscriber 2 did not observe the P0 event")
	}

	// Cleanup: cancelling ctx must release both subscribers.
	cancel()

	// Wait for subscriber goroutines to exit and close channels.
	time.Sleep(50 * time.Millisecond)

	// Verify no subscriber growth: after cancel, the subscriber map is empty.
	s.subsMu.Lock()
	n := len(s.subscribers)
	s.subsMu.Unlock()
	if n != 0 {
		t.Errorf("expected 0 subscribers after ctx cancel, got %d", n)
	}

	s.Ack(ctx, lease, lease.AttemptID)
}
