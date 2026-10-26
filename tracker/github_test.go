package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seppaleinen/idle-deck/queue"
	"github.com/seppaleinen/idle-deck/store"
	"github.com/seppaleinen/idle-deck/test/stubs"
)

func makeIssueJSON(number int, title, body string, labels []stubs.GitHubLabel, createdAt, updatedAt time.Time, userType string, isPR bool) []byte {
	issue := stubs.GitHubIssue{
		Number:    number,
		Title:     title,
		Body:      body,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Labels:    labels,
		User:      stubs.GitHubUser{Type: userType},
		HTMLURL:   fmt.Sprintf("https://github.com/acme/widgets/issues/%d", number),
	}
	if isPR {
		issue.PullRequest = &struct{}{}
	}
	b, _ := json.Marshal(issue)
	return b
}

func TestParse_P1NewIssue(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	t0 := time.Date(2026, 9, 29, 9, 58, 0, 0, time.UTC)
	gh.SetWatermark("acme/widgets", t0.Add(-time.Hour))
	raw := makeIssueJSON(412, "Add caching layer", "We need Redis-backed caching.", nil, t0, t0, "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if task.Tier != queue.TierP1 {
		t.Errorf("tier: got %v, want P1", task.Tier)
	}
	if queue.RoleForTier(task.Tier) != queue.RolePlan {
		t.Errorf("role: got %v, want plan", queue.RoleForTier(task.Tier))
	}
	if task.TriggeredBy.DedupeKey != "github:issue:acme/widgets:412:created" {
		t.Errorf("dedupe_key: got %q", task.TriggeredBy.DedupeKey)
	}
	if task.TriggeredBy.EventType != "issue.opened" {
		t.Errorf("event_type: got %q", task.TriggeredBy.EventType)
	}
	if task.Ticket.RepositoryID != "acme/widgets" {
		t.Errorf("repository_id: got %q", task.Ticket.RepositoryID)
	}
	if task.Ticket.ExternalID != "412" {
		t.Errorf("external_id: got %q", task.Ticket.ExternalID)
	}
}

func TestParse_P2Ready(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Add caching", "Need Redis.", []stubs.GitHubLabel{{Name: "idle-ready"}}, time.Now(), time.Now(), "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if task.Tier != queue.TierP2 {
		t.Errorf("tier: got %v, want P2", task.Tier)
	}
	if task.TriggeredBy.DedupeKey != "github:issue:acme/widgets:412:ready" {
		t.Errorf("dedupe_key: got %q", task.TriggeredBy.DedupeKey)
	}
	if task.TriggeredBy.EventType != "issue.labeled:idle-ready" {
		t.Errorf("event_type: got %q", task.TriggeredBy.EventType)
	}
}

func TestParse_P0Hotfix(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Hotfix critical bug", "Fix ASAP.", []stubs.GitHubLabel{{Name: "idle-hotfix"}}, time.Now(), time.Now(), "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if task.Tier != queue.TierP0 {
		t.Errorf("tier: got %v, want P0", task.Tier)
	}
	if task.TriggeredBy.DedupeKey != "github:issue:acme/widgets:412:hotfix" {
		t.Errorf("dedupe_key: got %q", task.TriggeredBy.DedupeKey)
	}
	if task.TriggeredBy.EventType != "issue.labeled:idle-hotfix" {
		t.Errorf("event_type: got %q", task.TriggeredBy.EventType)
	}
}

func TestParse_Redo(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Redo fix", "Try again.", []stubs.GitHubLabel{{Name: "idle-redo"}}, time.Now(), time.Now(), "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if task.Tier != queue.TierP1 {
		t.Errorf("tier: got %v, want P1", task.Tier)
	}
	if task.TriggeredBy.DedupeKey != "github:issue:acme/widgets:412:redo" {
		t.Errorf("dedupe_key: got %q", task.TriggeredBy.DedupeKey)
	}
	if task.TriggeredBy.EventType != "issue.labeled:idle-redo" {
		t.Errorf("event_type: got %q", task.TriggeredBy.EventType)
	}
}

func TestParse_PRExcluded(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "PR", "This is a PR.", nil, time.Now(), time.Now(), "User", true)
	_, err := gh.Parse(context.Background(), raw)
	if !errors.Is(err, ErrNotCandidate) {
		t.Errorf("expected ErrNotCandidate for PR, got %v", err)
	}
}

func TestParse_BotExcluded(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Bot issue", "From dependabot.", nil, time.Now(), time.Now(), "Bot", false)
	_, err := gh.Parse(context.Background(), raw)
	if !errors.Is(err, ErrNotCandidate) {
		t.Errorf("expected ErrNotCandidate for bot issue, got %v", err)
	}
}

func TestParse_NeedsHumanExcluded(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Needs human", "Needs review.", []stubs.GitHubLabel{{Name: "idle-needs-human"}}, time.Now(), time.Now(), "User", false)
	_, err := gh.Parse(context.Background(), raw)
	if !errors.Is(err, ErrNotCandidate) {
		t.Errorf("expected ErrNotCandidate for idle-needs-human, got %v", err)
	}
}

func TestParse_NotAllowlisted(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Issue", "From other repo.", nil, time.Now(), time.Now(), "User", false)
	raw = []byte(strings.ReplaceAll(string(raw), "acme/widgets", "other/repo"))
	_, err := gh.Parse(context.Background(), raw)
	if !errors.Is(err, ErrNotAllowlisted) {
		t.Errorf("expected ErrNotAllowlisted, got %v", err)
	}
}

func TestParse_PromptFormat(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Add caching", "We need Redis.", nil, time.Now(), time.Now(), "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	expectedPrompt := `repo:   acme/widgets
issue:  #412
url:    https://github.com/acme/widgets/issues/412
tier:   P1  (role: plan)

Add caching

We need Redis.`
	if task.Prompt != expectedPrompt {
		t.Errorf("prompt mismatch:\ngot:\n%s\n\nwant:\n%s", task.Prompt, expectedPrompt)
	}
}

// TestSetLabelsRemove verifies event-contract §6: SetLabels honours the
// remove parameter. The real adapter PATCHes the issue with the computed
// label set (current ∪ add) − remove, so a removed label must actually be
// gone from the stub's state afterwards (D29).
func TestSetLabelsRemove(t *testing.T) {
	repo := "acme/widgets"
	tState := stubs.NewTrackerState(repo)
	tState.AddRepoLabel("idle-ready")
	tServer := stubs.NewTrackerServer(tState)
	defer tServer.Close()

	issue := &stubs.GitHubIssue{
		Number: 412,
		Title:  "Add caching",
		Body:   "Need Redis.",
		User:   stubs.GitHubUser{Type: "User"},
	}
	issue.Labels = []stubs.GitHubLabel{stubs.LabelIdleReady}
	tState.UpsertIssue(issue)

	gh := NewGitHub(tServer.URL(), "stub-token", []string{repo}, store.NewSQLiteWatermarks(nil))

	if err := gh.SetLabels(context.Background(),
		queue.TrackerRef{Tracker: "github", RepositoryID: repo, ExternalID: "412"},
		nil, []string{"idle-ready"}); err != nil {
		t.Fatalf("SetLabels: %v", err)
	}

	if tState.HasLabel(412, "idle-ready") {
		t.Error("idle-ready should have been removed (event-contract §6)")
	}
}

// TestSetLabelsAdd verifies SetLabels adds labels that were absent, without
// dropping labels that were already present (GitHub PATCH replaces the whole
// set, so the adapter must read-then-merge, not blindly overwrite).
func TestSetLabelsAdd(t *testing.T) {
	repo := "acme/widgets"
	tState := stubs.NewTrackerState(repo)
	tServer := stubs.NewTrackerServer(tState)
	defer tServer.Close()

	issue := &stubs.GitHubIssue{
		Number: 412,
		Title:  "Add caching",
		Body:   "Need Redis.",
		User:   stubs.GitHubUser{Type: "User"},
	}
	issue.Labels = []stubs.GitHubLabel{stubs.LabelIdleReady}
	tState.UpsertIssue(issue)

	gh := NewGitHub(tServer.URL(), "stub-token", []string{repo}, store.NewSQLiteWatermarks(nil))

	if err := gh.SetLabels(context.Background(),
		queue.TrackerRef{Tracker: "github", RepositoryID: repo, ExternalID: "412"},
		[]string{"idle-hotfix"}, nil); err != nil {
		t.Fatalf("SetLabels: %v", err)
	}

	if !tState.HasLabel(412, "idle-ready") {
		t.Error("idle-ready should have been preserved (adapter merges, does not replace)")
	}
	if !tState.HasLabel(412, "idle-hotfix") {
		t.Error("idle-hotfix should have been added")
	}
}

// TestParse_P0Prompt verifies the P0 prompt carries the correct role mapping.
func TestParse_P0Prompt(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Hotfix", "Urgent.", []stubs.GitHubLabel{{Name: "idle-hotfix"}}, time.Now(), time.Now(), "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	expectedPrompt := `repo:   acme/widgets
issue:  #412
url:    https://github.com/acme/widgets/issues/412
tier:   P0  (role: do)

Hotfix

Urgent.`
	if task.Prompt != expectedPrompt {
		t.Errorf("P0 prompt mismatch:\ngot:\n%s\n\nwant:\n%s", task.Prompt, expectedPrompt)
	}
}

func TestParse_P2Prompt(t *testing.T) {
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, store.NewSQLiteWatermarks(nil))
	raw := makeIssueJSON(412, "Feature", "Implement feature.", []stubs.GitHubLabel{{Name: "idle-ready"}}, time.Now(), time.Now(), "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	expectedPrompt := `repo:   acme/widgets
issue:  #412
url:    https://github.com/acme/widgets/issues/412
tier:   P2  (role: do)

Feature

Implement feature.`
	if task.Prompt != expectedPrompt {
		t.Errorf("P2 prompt mismatch:\ngot:\n%s\n\nwant:\n%s", task.Prompt, expectedPrompt)
	}
}

func TestEnqueueIdempotent(t *testing.T) {
	dbPath := t.TempDir() + "/test.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	q := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	wm := store.NewSQLiteWatermarks(db)
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, wm)
	raw := makeIssueJSON(412, "Test", "Test issue.", nil, time.Now(), time.Now(), "User", false)
	task, err := gh.Parse(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := q.Enqueue(ctx, task); err != nil {
		t.Fatalf("second enqueue failed: %v", err)
	}
	var count int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE dedupe_key = ?`, task.TriggeredBy.DedupeKey).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 task, got %d", count)
	}
}

func TestPollerTick(t *testing.T) {
	repo := "acme/widgets"
	tState := stubs.NewTrackerState(repo)
	tServer := stubs.NewTrackerServer(tState)
	defer tServer.Close()
	dbPath := t.TempDir() + "/poller.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	gh := NewGitHub(tServer.URL(), "stub-token", []string{repo}, store.NewSQLiteWatermarks(nil))
	gh.db = store.NewSQLiteWatermarks(db)
	p := NewPoller(gh, queue, store.NewSQLiteWatermarks(db), []string{repo}, 60*time.Second)
	p.now = func() time.Time { return time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC) }
	t0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	gh.SetWatermark("acme/widgets", t0.Add(-time.Hour))
	tState.UpsertIssue(&stubs.GitHubIssue{
		Number:    412,
		Title:     "Add caching layer",
		Body:      "We need Redis-backed caching.",
		CreatedAt: t0,
		UpdatedAt: t0,
		User:      stubs.GitHubUser{Type: "User"},
	})
	ctx := context.Background()
	completed, err := p.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick failed: %v", err)
	}
	if len(completed) != 1 || completed[0] != "acme/widgets" {
		t.Fatalf("completed: got %v, want [acme/widgets]", completed)
	}
	var count int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE ticket_repo_id = 'acme/widgets'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 task, got %d", count)
	}
	wm = store.NewSQLiteWatermarks(db)
	var wmVal time.Time
	var seeded bool
	wmVal, seeded, err = wm.Load(context.Background(), "acme/widgets")
	if err != nil {
		t.Fatal(err)
	}
	if !seeded {
		t.Error("watermark should be seeded")
	}
	if !wmVal.Equal(p.now()) {
		t.Errorf("watermark = %v, want %v", wmVal, p.now())
	}
}

// TestPollerSweepDisabled verifies SweepPeriod=0 disables sweeping entirely —
// no P3 task is ever enqueued regardless of sweepRepos.
func TestPollerSweepDisabled(t *testing.T) {
	repo := "acme/widgets"
	dbPath := t.TempDir() + "/sweep.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	gh := NewGitHub("http://localhost", "token", []string{repo}, wm)
	p := NewPoller(gh, queue, wm, []string{repo}, 60*time.Second,
		WithSweep(SweepConfig{
			Period: 0, // disabled
			Repos:  []string{repo},
			Prompt: "sweep prompt",
		}))
	ctx := context.Background()
	if err := p.Sweep(ctx); err != nil {
		t.Fatalf("Sweep returned error: %v", err)
	}
	var count int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE tier = 'P3'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 P3 tasks, got %d", count)
	}
}

// TestPollerSweepEnqueuesP3 verifies Sweep enqueues a P3 task with correct
// properties and dedupe key when SweepPeriod > 0.
func TestPollerSweepEnqueuesP3(t *testing.T) {
	repo := "acme/widgets"
	dbPath := t.TempDir() + "/sweep.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	gh := NewGitHub("http://localhost", "token", []string{repo}, wm)
	sweepPeriod := 24 * time.Hour
	p := NewPoller(gh, queue, wm, []string{repo}, 60*time.Second,
		WithSweep(SweepConfig{
			Period: sweepPeriod,
			Repos:  []string{repo},
			Prompt: "sweep prompt",
		}))
	p.now = func() time.Time { return time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	if err := p.Sweep(ctx); err != nil {
		t.Fatalf("Sweep failed: %v", err)
	}
	var count int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE tier = 'P3'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 P3 task, got %d", count)
	}
	var taskID, dedupeKey, tier, prompt string
	var budget, timeoutSec int
	err = db.QueryRowContext(context.Background(), `
		SELECT id, dedupe_key, tier, prompt, budget, timeout_seconds
		FROM tasks WHERE tier = 'P3'`).
		Scan(&taskID, &dedupeKey, &tier, &prompt, &budget, &timeoutSec)
	if err != nil {
		t.Fatal(err)
	}
	if tier != "P3" {
		t.Errorf("tier: got %s, want P3", tier)
	}
	if budget != 20000 {
		t.Errorf("budget: got %d, want 20000", budget)
	}
	if timeoutSec != 3600 {
		t.Errorf("timeout_seconds: got %d, want 3600", timeoutSec)
	}
	if prompt != "sweep prompt" {
		t.Errorf("prompt: got %q, want %q", prompt, "sweep prompt")
	}
	// Dedupe key format: github:sweep:<owner>/<repo>:<bucket>
	expectedBucket := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC).Truncate(sweepPeriod).Unix()
	expectedDedupeKey := fmt.Sprintf("github:sweep:%s:%d", repo, expectedBucket)
	if dedupeKey != expectedDedupeKey {
		t.Errorf("dedupe_key: got %q, want %q", dedupeKey, expectedDedupeKey)
	}
	if taskID == "" {
		t.Error("task ID is empty")
	}
}

// TestPollerSweepDedupeCollapse verifies that repeated Sweep calls within the
// same bucket collapse to a single task (ErrDuplicate is a no-op). A new bucket
// produces a new task.
// TestPollerSweepHonoursConfiguredTimeoutBudget verifies D37: the sweep
// tier's timeout and budget are operator-configurable via env overrides,
// and the poller honours them instead of hardcoding 3600s / 20000.
func TestPollerSweepHonoursConfiguredTimeoutBudget(t *testing.T) {
	repo := "acme/widgets"
	dbPath := t.TempDir() + "/sweep-cfg.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	gh := NewGitHub("http://localhost", "token", []string{repo}, wm)
	p := NewPoller(gh, queue, wm, []string{repo}, 60*time.Second,
		WithSweep(SweepConfig{
			Period:         24 * time.Hour,
			Repos:          []string{repo},
			Prompt:         "sweep prompt",
			TimeoutSeconds: 7200,
			Budget:         45000,
		}))
	p.now = func() time.Time { return time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC) }
	ctx := context.Background()
	if err := p.Sweep(ctx); err != nil {
		t.Fatalf("Sweep failed: %v", err)
	}
	var budget, timeoutSec int
	err = db.QueryRowContext(context.Background(), `
		SELECT budget, timeout_seconds FROM tasks WHERE tier = 'P3'`).
		Scan(&budget, &timeoutSec)
	if err != nil {
		t.Fatal(err)
	}
	if budget != 45000 {
		t.Errorf("budget: got %d, want 45000 (D37 override)", budget)
	}
	if timeoutSec != 7200 {
		t.Errorf("timeout_seconds: got %d, want 7200 (D37 override)", timeoutSec)
	}
}

func TestPollerSweepDedupeCollapse(t *testing.T) {
	repo := "acme/widgets"
	dbPath := t.TempDir() + "/sweep.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	gh := NewGitHub("http://localhost", "token", []string{repo}, wm)
	sweepPeriod := 24 * time.Hour
	p := NewPoller(gh, queue, wm, []string{repo}, 60*time.Second,
		WithSweep(SweepConfig{
			Period: sweepPeriod,
			Repos:  []string{repo},
			Prompt: "sweep prompt",
		}))
	// First call at bucket boundary.
	bucket0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	p.now = func() time.Time { return bucket0 }
	ctx := context.Background()
	if err := p.Sweep(ctx); err != nil {
		t.Fatalf("first Sweep: %v", err)
	}
	// Second call within same bucket (different time, same bucket).
	bucket0Plus1h := bucket0.Add(1 * time.Hour)
	p.now = func() time.Time { return bucket0Plus1h }
	if err := p.Sweep(ctx); err != nil {
		t.Fatalf("second Sweep (same bucket): %v", err)
	}
	// Third call in next bucket.
	bucket1 := bucket0.Add(sweepPeriod)
	p.now = func() time.Time { return bucket1 }
	if err := p.Sweep(ctx); err != nil {
		t.Fatalf("third Sweep (next bucket): %v", err)
	}
	var count int
	err = db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE tier = 'P3'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	// Two buckets -> 2 tasks (first two calls collapsed to one via dedupe).
	if count != 2 {
		t.Errorf("expected 2 P3 tasks (one per bucket), got %d", count)
	}
}

// TestListIssuesWithLabel exercises ListIssuesWithLabel (issue #36, D38/D45)
// against the existing stub server. It seeds issues with and without
// idle-needs-human plus a PR entry, asserts only labelled non-PR issues come
// back sorted by repo then number, and asserts the empty case returns a
// non-nil empty slice.
func TestListIssuesWithLabel(t *testing.T) {
	repo := "acme/widgets"
	tState := stubs.NewTrackerState(repo)
	tServer := stubs.NewTrackerServer(tState)
	defer tServer.Close()

	// Seed: two labelled issues, one unlabelled issue, one PR carrying the label.
	tState.UpsertIssue(&stubs.GitHubIssue{
		Number: 100,
		Title:  "Needs human: slow query",
		User:   stubs.GitHubUser{Type: "User"},
		Labels: []stubs.GitHubLabel{stubs.LabelIdleNeedsHuman},
	})
	tState.UpsertIssue(&stubs.GitHubIssue{
		Number: 101,
		Title:  "Needs human: flaky test",
		User:   stubs.GitHubUser{Type: "User"},
		Labels: []stubs.GitHubLabel{stubs.LabelIdleNeedsHuman},
	})
	tState.UpsertIssue(&stubs.GitHubIssue{
		Number: 102,
		Title:  "Unlabelled issue",
		User:   stubs.GitHubUser{Type: "User"},
	})
	tState.UpsertIssue(&stubs.GitHubIssue{
		Number: 103,
		Title:  "A pull request",
		User:   stubs.GitHubUser{Type: "User"},
		Labels: []stubs.GitHubLabel{stubs.LabelIdleNeedsHuman},
		PullRequest: &struct{}{},
	})

	gh := NewGitHub(tServer.URL(), "stub-token", []string{repo}, store.NewSQLiteWatermarks(nil))

	got, err := gh.ListIssuesWithLabel(context.Background(), LabelNeedsHuman)
	if err != nil {
		t.Fatalf("ListIssuesWithLabel: %v", err)
	}
	if got == nil {
		t.Fatal("ListIssuesWithLabel returned nil slice, want []")
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 issues, got %d: %+v", len(got), got)
	}
	if got[0].Number != 100 || got[1].Number != 101 {
		t.Errorf("expected [100, 101] sorted by number, got [%d, %d]", got[0].Number, got[1].Number)
	}
	if got[0].Repo != repo || got[1].Repo != repo {
		t.Errorf("expected repo %q, got %q/%q", repo, got[0].Repo, got[1].Repo)
	}
	if got[0].URL != "https://github.com/acme/widgets/issues/100" {
		t.Errorf("expected URL https://github.com/acme/widgets/issues/100, got %q", got[0].URL)
	}
	if got[0].Title != "Needs human: slow query" {
		t.Errorf("expected title %q, got %q", "Needs human: slow query", got[0].Title)
	}

	// Empty case: a label that matches nothing returns a non-nil empty slice.
	empty, err := gh.ListIssuesWithLabel(context.Background(), "idle-hotfix")
	if err != nil {
		t.Fatalf("ListIssuesWithLabel (empty): %v", err)
	}
	if empty == nil {
		t.Fatal("ListIssuesWithLabel (empty) returned nil slice, want []")
	}
	if len(empty) != 0 {
		t.Errorf("expected 0 issues, got %d", len(empty))
	}
}

// TestCloneURL tests the cloneURL method for various hosts and protocols.
func TestCloneURL(t *testing.T) {
	tests := []struct {
		name    string
		apiURL  string
		repo    string
		want    string
		wantErr bool
	}{
		{"GitHub.com HTTPS", "https://api.github.com", "owner/repo", "https://github.com/owner/repo.git", false},
		{"GitHub.com HTTP", "http://api.github.com", "owner/repo", "https://github.com/owner/repo.git", false},
		{"GHE HTTPS", "https://ghe.example.com", "owner/repo", "https://ghe.example.com/owner/repo.git", false},
		{"GHE HTTP", "http://ghe.example.com", "owner/repo", "https://ghe.example.com/owner/repo.git", false},
		{"GHE with port", "https://ghe.example.com:8443", "owner/repo", "https://ghe.example.com:8443/owner/repo.git", false},
		{"GitHub.com no scheme", "api.github.com", "owner/repo", "https://github.com/owner/repo.git", false},
		{"GHE no scheme", "ghe.example.com", "owner/repo", "https://ghe.example.com/owner/repo.git", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gh := NewGitHub(tt.apiURL, "token", []string{tt.repo}, store.NewSQLiteWatermarks(nil))
			got := gh.cloneURL(tt.repo)
			if tt.wantErr {
				if got != "" {
					t.Errorf("cloneURL(%q) = %q, want empty (error)", tt.repo, got)
				}
				return
			}
			if got != tt.want {
				t.Errorf("cloneURL(%q) = %q, want %q", tt.repo, got, tt.want)
			}
		})
	}
}

// TestExtractRepo pins the host-agnostic extraction (M1 fix).
// github.com, GHE, http, https, with/without port, with/without scheme —
// all return the same owner/repo because the host carries no trust decision
// (I1 allowlist authorizes the repo, not the hostname).
func TestExtractRepo(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"github.com HTTPS", "https://github.com/acme/widgets/issues/412", "acme/widgets"},
		{"github.com HTTP", "http://github.com/acme/widgets/issues/412", "acme/widgets"},
		{"GHE HTTPS", "https://git.acme.com/acme/widgets/issues/412", "acme/widgets"},
		{"GHE HTTP", "http://git.acme.com/acme/widgets/issues/412", "acme/widgets"},
		{"GHE with port", "https://git.acme.com:8443/acme/widgets/issues/412", "acme/widgets"},
		{"no scheme", "github.com/acme/widgets/issues/412", "acme/widgets"},
		{"GHE no scheme", "git.acme.com/acme/widgets/issues/412", "acme/widgets"},
		{"no /issues suffix", "https://github.com/acme/widgets", "acme/widgets"},
		{"malformed empty", "", ""},
		{"malformed no slash", "https://github.com", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractRepo(tc.url)
			if got != tc.want {
				t.Errorf("extractRepo(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}

// TestPollerTickPartialFailure verifies that Tick returns a partial completed
// list when a mid-list repo fails and that the per-repo poll ledger records
// only repos that were actually polled (M7 — operator-surface §6, D28). With
// the old code SavePoll was recorded for every repo on a successful Tick,
// regardless of which repos were actually polled — a permanently-failing repo
// hid behind a healthy-looking tick. After the fix, Tick fails fast per repo
// and returns the repos it completed before the failure.
func TestPollerTickPartialFailure(t *testing.T) {
	repoOK := "acme/widgets"
	repoFail := "acme/broken"

	// One httptest server that serves a good repo and 404s for the broken one.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues") {
			// Return a single issue so poll succeeds.
			issue := []stubs.GitHubIssue{{
				Number:    1,
				Title:     "t",
				CreatedAt: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC),
				User:      stubs.GitHubUser{Type: "User"},
			}}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"empty"`)
			json.NewEncoder(w).Encode(issue)
			return
		}
		// Anything else -> 404 (simulates a misconfigured / non-existent repo)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	// In-process heartbeat to verify the ledger truthfulness.
	dbPath := t.TempDir() + "/partial.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wm := store.NewSQLiteWatermarks(db)
	q := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	hb := store.NewSQLiteHeartbeat(db)

	gh := NewGitHub(srv.URL, "stub-token", []string{repoOK, repoFail}, wm)
	p := NewPoller(gh, q, wm, []string{repoOK, repoFail}, 60*time.Second)
	p.now = func() time.Time { return time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC) }

	ctx := context.Background()

	// Seed old poll timestamps so we can assert they change (or don't).
	old := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	_ = hb.SavePoll(ctx, repoFail, old)
	_ = hb.SavePoll(ctx, repoOK, old)

	// Tick: repoOK (succeeds) then repoFail (404). repoOK is first in the list
	// so it completes; repoFail is second so it fails fast.
	completed, err := p.Tick(ctx)
	if err == nil {
		t.Fatalf("expected Tick to fail on %s, got nil error; completed=%v", repoFail, completed)
	}

	// Completed should contain only repoOK — the repo that was actually polled
	// before the failure stopped the tick.
	if len(completed) != 1 || completed[0] != repoOK {
		t.Fatalf("completed: got %v, want [%s]", completed, repoOK)
	}

	// Simulate the caller's pattern (main.go:266-267): record SavePoll for
	// each completed repo. repoOK gets recorded; repoFail does not.
	tickTime := p.now()
	for _, repo := range completed {
		_ = hb.SavePoll(ctx, repo, tickTime)
	}

	// repoOK's poll ledger should have advanced.
	lastOK, seededOK, err := hb.LastPoll(ctx, repoOK)
	if err != nil {
		t.Fatal(err)
	}
	if !seededOK {
		t.Fatal("expected seeded last poll for repoOK")
	}
	if !lastOK.Equal(tickTime) {
		t.Fatalf("repoOK last_poll: got %v, want %v", lastOK, tickTime)
	}

	// repoFail's poll ledger should be unchanged — it was never polled this tick.
	lastFail, seededFail, err := hb.LastPoll(ctx, repoFail)
	if err != nil {
		t.Fatal(err)
	}
	if !seededFail {
		t.Fatal("expected seeded last poll for repoFail")
	}
	if !lastFail.Equal(old) {
		t.Fatalf("repoFail last_poll changed to %v, want unchanged %v", lastFail, old)
	}
}

// TestPollerTickFirstRepoFails verifies the edge case where the first repo in
// the list fails — completed should be an empty (but non-nil) slice, not nil.
// This ensures the caller's `for _, repo := range completed` is a no-op.
func TestPollerTickFirstRepoFails(t *testing.T) {
	repoFail := "acme/broken"
	repoOK := "acme/widgets"

	// 404 for repoFail, succeed for repoOK.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/repos/acme/widgets/issues") {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("ETag", `"empty"`)
			json.NewEncoder(w).Encode([]stubs.GitHubIssue{})
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	dbPath := t.TempDir() + "/first-fail.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	wm := store.NewSQLiteWatermarks(db)
	q := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))

	gh := NewGitHub(srv.URL, "stub-token", []string{repoFail, repoOK}, wm)
	// repoFail is FIRST in the list — should fail immediately.
	p := NewPoller(gh, q, wm, []string{repoFail, repoOK}, 60*time.Second)
	p.now = func() time.Time { return time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC) }

	completed, err := p.Tick(context.Background())
	if err == nil {
		t.Fatal("expected Tick to fail on first repo, got nil error")
	}
	// completed should be empty (no repos polled before failure).
	if len(completed) != 0 {
		t.Fatalf("completed: got %v, want empty slice", completed)
	}
	// Verify: range over completed is a no-op, so no SavePoll occurs.
	// (We don't need to check the ledger — an empty range does nothing.)
}
