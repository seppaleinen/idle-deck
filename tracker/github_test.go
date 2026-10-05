package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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