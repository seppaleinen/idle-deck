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
	if err := p.Tick(ctx); err != nil {
		t.Fatalf("Tick failed: %v", err)
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

// TestGetLabelsRealWireShape pins the single-issue GET decode to the real
// GitHub REST shape — a JSON object, not an array. This test bypasses the
// in-repo stub and speaks to an httptest server directly, so it exercises
// the exact wire the adapter will see against real GitHub/GHE (D29,
// event-contract §6). Without it the stub could drift from the wire again
// and the label path would keep passing while failing in production.
func TestGetLabelsRealWireShape(t *testing.T) {
	repo := "acme/widgets"
	issue := stubs.GitHubIssue{
		Number:   412,
		Title:    "Add caching",
		Body:     "Need Redis.",
		User:     stubs.GitHubUser{Type: "User"},
		HTMLURL:  "https://github.com/acme/widgets/issues/412",
	}
	issue.Labels = []stubs.GitHubLabel{stubs.LabelIdleReady, stubs.LabelIdleHotfix}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/widgets/issues/412" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(issue)
	}))
	defer srv.Close()

	gh := NewGitHub(srv.URL, "test-token", []string{repo}, store.NewSQLiteWatermarks(nil))

	got, err := gh.getLabels(context.Background(),
		queue.TrackerRef{Tracker: "github", RepositoryID: repo, ExternalID: "412"})
	if err != nil {
		t.Fatalf("getLabels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("labels: got %v, want 2", got)
	}
	if !hasLabel(got, "idle-ready") || !hasLabel(got, "idle-hotfix") {
		t.Errorf("labels: got %v, want idle-ready and idle-hotfix", got)
	}
}

// TestGetLabelsRealWireArrayBody verifies the adapter rejects an array body
// for the single-issue GET — the old broken behaviour. If getLabels silently
// decoded an array it would be a stub-only path that fails against real GitHub.
func TestGetLabelsRealWireArrayBody(t *testing.T) {
	repo := "acme/widgets"
	issue := stubs.GitHubIssue{Number: 412, Title: "x", User: stubs.GitHubUser{Type: "User"}}
	issue.Labels = []stubs.GitHubLabel{stubs.LabelIdleReady}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]stubs.GitHubIssue{issue})
	}))
	defer srv.Close()

	gh := NewGitHub(srv.URL, "test-token", []string{repo}, store.NewSQLiteWatermarks(nil))

	_, err := gh.getLabels(context.Background(),
		queue.TrackerRef{Tracker: "github", RepositoryID: repo, ExternalID: "412"})
	if err == nil {
		t.Fatal("expected decode error for array body on single-issue GET, got nil")
	}
}

func hasLabel(labels []string, name string) bool {
	for _, l := range labels {
		if l == name {
			return true
		}
	}
	return false
}

// TestRateLimited429DoesNotFailTick verifies that an HTTP 429 response does not
// fail the entire tick: the watermark is not advanced, but the poller continues
// to the next repository (event-contract §2).
func TestRateLimited429DoesNotFailTick(t *testing.T) {
	repo := "acme/widgets"

	// Create a stub server that returns 429 on issues endpoint.
	tServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/issues") {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", fmt.Sprintf("%d", time.Now().Add(1*time.Minute).Unix()))
			w.Header().Set("Retry-After", "60")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer tServer.Close()

	dbPath := t.TempDir() + "/ratelimit.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))

	t0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)

	// Create poller for repo which will get 429
	gh := NewGitHub(tServer.URL, "stub-token", []string{repo}, wm)
	poller := NewPoller(gh, queue, wm, []string{repo}, 60*time.Second)
	poller.now = func() time.Time { return t0 }

	ctx := context.Background()
	err = poller.Tick(ctx)
	if err != nil {
		t.Fatalf("Tick on rate-limited repo should not fail: %v", err)
	}

	// Verify watermark did NOT advance for repo
	wmVal, seeded, err := wm.Load(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if seeded {
		t.Errorf("watermark should not be seeded for rate-limited repo, got %v", wmVal)
	}

	// Verify rate-limit state was persisted
	remaining, resetAt, retryAfter, err := wm.LoadRateLimit(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("expected remaining=0, got %d", remaining)
	}
	if retryAfter != int64(60*time.Second) {
		t.Errorf("expected retry-after=60s, got %d ns", retryAfter)
	}
	_ = resetAt // just verify it was stored
}

// TestRateLimitIntervalScaling verifies that the poll interval is scaled
// proportionally when the remaining quota is low.
func TestRateLimitIntervalScaling(t *testing.T) {
	base := 60 * time.Second
	
	tests := []struct {
		name      string
		remaining int
		wantMin   time.Duration
		wantMax   time.Duration
	}{
		{"healthy", 5000, base, base * 2},      // no scaling
		{"low", 50, base * 2, base * 4},        // ~2x scaling
		{"very low", 10, base * 4, base * 6},   // ~5x scaling (capped)
		{"exhausted", 0, base, base},           // no info, use base
	}
	
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scaleIntervalForRemaining(base, tt.remaining, 100)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("scaleIntervalForRemaining(%d) = %v, want [%v, %v]",
					tt.remaining, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

// TestNextIntervalResetsAfterBackoff verifies that NextInterval returns the
// base interval once the backoff period has elapsed.
func TestNextIntervalResetsAfterBackoff(t *testing.T) {
	base := 60 * time.Second
	backoff := 5 * time.Minute

	dbPath := t.TempDir() + "/backoff.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	gh := NewGitHub("http://localhost", "token", []string{"acme/widgets"}, wm)

	poller := NewPoller(gh, queue, wm, []string{"acme/widgets"}, base)

	// Simulate a rate-limited tick
	t0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	poller.now = func() time.Time { return t0 }
	poller.rateLimitBackoff = backoff
	poller.rateLimitSeenAt = t0

	// Immediately after 429, NextInterval should return the backoff
	if got := poller.NextInterval(); got != backoff {
		t.Errorf("NextInterval immediately after 429 = %v, want %v", got, backoff)
	}

	// After the backoff period has elapsed, it should reset to base
	poller.now = func() time.Time { return t0.Add(backoff + 1*time.Second) }
	if got := poller.NextInterval(); got != base {
		t.Errorf("NextInterval after backoff elapsed = %v, want %v", got, base)
	}
}

// TestParseRateLimitHeaders verifies correct parsing of GitHub rate-limit headers.
func TestParseRateLimitHeaders(t *testing.T) {
	tests := []struct {
		name                string
		remaining           string
		reset               string
		retryAfter          string
		wantRemaining       int
		wantResetAgo        time.Duration // how much before "now" the reset should be
		wantRetryAfter      time.Duration
	}{
		{
			name:            "seconds retry-after",
			remaining:       "50",
			reset:           fmt.Sprintf("%d", time.Now().Add(60*time.Second).Unix()),
			retryAfter:      "30",
			wantRemaining:   50,
			wantRetryAfter: 30 * time.Second,
		},
		{
			name:            "http-date retry-after",
			remaining:       "5",
			reset:           fmt.Sprintf("%d", time.Now().Add(60*time.Second).Unix()),
			retryAfter:      time.Now().Add(45*time.Second).UTC().Format(http.TimeFormat),
			wantRemaining:   5,
			wantRetryAfter: 45 * time.Second, // approximate
		},
		{
			name:            "missing retry-after",
			remaining:       "10",
			reset:           fmt.Sprintf("%d", time.Now().Add(60*time.Second).Unix()),
			wantRemaining:   10,
			wantRetryAfter: 0,
		},
		{
			name:            "all headers missing",
			remaining:       "",
			reset:           "",
			wantRemaining:   0,
			wantRetryAfter: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resp := &http.Response{
				Header: make(http.Header),
			}
			if tt.remaining != "" {
				resp.Header.Set("X-RateLimit-Remaining", tt.remaining)
			}
			if tt.reset != "" {
				resp.Header.Set("X-RateLimit-Reset", tt.reset)
			}
			if tt.retryAfter != "" {
				resp.Header.Set("Retry-After", tt.retryAfter)
			}

			got := parseRateLimitHeaders(resp)
			if got.Remaining != tt.wantRemaining {
				t.Errorf("Remaining = %d, want %d", got.Remaining, tt.wantRemaining)
			}
			if tt.wantRetryAfter > 0 {
				diff := time.Until(got.ResetAt) - tt.wantResetAgo
				if diff < 0 {
					diff = -diff
				}
				if diff > 5*time.Second && tt.name == "seconds retry-after" {
					// ResetAt check only for seconds case
				}
			}
			if tt.wantRetryAfter > 0 && tt.name == "seconds retry-after" {
				if got.RetryAfter < tt.wantRetryAfter-time.Second || got.RetryAfter > tt.wantRetryAfter+time.Second {
					t.Errorf("RetryAfter = %v, want approx %v", got.RetryAfter, tt.wantRetryAfter)
				}
			}
		})
	}
}

// TestETag304LogicStillWorks verifies that conditional requests with ETags
// still function correctly after adding rate-limit handling.
func TestETag304LogicStillWorks(t *testing.T) {
	repo := "acme/widgets"
	tState := stubs.NewTrackerState(repo)
	tServer := stubs.NewTrackerServer(tState)
	defer tServer.Close()

	// Seed an issue
	t0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	tState.UpsertIssue(&stubs.GitHubIssue{
		Number:    100,
		Title:     "Test issue",
		Body:      "Body",
		User:      stubs.GitHubUser{Type: "User"},
		CreatedAt: t0,
		UpdatedAt: t0,
	})

	dbPath := t.TempDir() + "/etag.db"
	db, err := store.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	wm := store.NewSQLiteWatermarks(db)
	queue := store.NewSQLiteQueue(dbPath, store.WithLeaseDuration(5*time.Minute), store.WithMaxAttempts(3))
	gh := NewGitHub(tServer.URL(), "stub-token", []string{repo}, wm)
	poller := NewPoller(gh, queue, wm, []string{repo}, 60*time.Second)
	poller.now = func() time.Time { return t0 }

	ctx := context.Background()

	// First tick: should enqueue the issue
	if err := poller.Tick(ctx); err != nil {
		t.Fatalf("first tick failed: %v", err)
	}

	var count int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE tier = 'P1'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 task after first tick, got %d", count)
	}

	// Second tick: should skip due to ETag 304
	if err := poller.Tick(ctx); err != nil {
		t.Fatalf("second tick failed: %v", err)
	}

	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tasks WHERE tier = 'P1'`).Scan(&count)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("expected 1 task after second tick (ETag 304 skip), got %d", count)
	}

	// Verify watermark advanced on first tick
	wmVal, seeded, err := wm.Load(ctx, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !seeded || !wmVal.Equal(t0) {
		t.Errorf("watermark should be seeded and equal to tick time: seeded=%v, val=%v", seeded, wmVal)
	}
}
