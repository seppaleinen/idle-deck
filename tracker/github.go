package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/seppaleinen/idle-deck/queue"
	"github.com/seppaleinen/idle-deck/store"
)

// Sentinel errors for the tracker adapter.
var (
	ErrNotCandidate   = errors.New("tracker: not a candidate issue (PR, bot, or idle-needs-human)")
	ErrNotAllowlisted = errors.New("tracker: repository not in allowlist (I1)")
)

// githubIssue mirrors the GitHub REST API issue shape (event-contract §8).
type githubIssue struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Labels      []githubLabel `json:"labels"`
	User        githubUser    `json:"user"`
	PullRequest *struct{}     `json:"pull_request,omitempty"`
	HTMLURL     string        `json:"html_url"`
}

type githubLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"` // ignored but present in GitHub response
}

type githubUser struct {
	Type string `json:"type"` // "User" or "Bot"
}

// GitHub implements TrackerSource and TrackerSink (D25, ADR 0012).
// One concrete type for both interfaces in the MVP (D13).
type GitHub struct {
	apiURL    string
	token     string
	allowlist map[string]bool
	wm        store.WatermarkStore
	db        *store.SQLiteWatermarks
}

// NewGitHub creates a GitHub tracker adapter.
// apiURL is the base URL (IDLE_DECK_GITHUB_API), token is the PAT,
// allowlist is IDLE_DECK_REPOS (D30), wm is the watermark store.
func NewGitHub(apiURL, token string, allowlist []string, wm store.WatermarkStore) *GitHub {
	al := make(map[string]bool, len(allowlist))
	for _, r := range allowlist {
		al[r] = true
	}
	return &GitHub{
		apiURL:    apiURL,
		token:     token,
		allowlist: al,
		wm:        wm,
	}
}

// SetWatermark seeds/advances the watermark for a repo (event-contract §2).
// Used in tests to set the initial watermark before a tick.
func (g *GitHub) SetWatermark(repo string, t time.Time) {
	if g.db != nil {
		_ = g.db.Save(context.Background(), repo, t)
	}
}

// Parse implements TrackerSource.Parse: turns raw GitHub issue JSON into a
// derived, allowlist-checked queue.Task (event-contract §3, §8).
func (g *GitHub) Parse(ctx context.Context, raw []byte) (queue.Task, error) {
	var issue githubIssue
	if err := json.Unmarshal(raw, &issue); err != nil {
		return queue.Task{}, fmt.Errorf("tracker: parse: %w", err)
	}

	repo := extractRepo(issue.HTMLURL)
	if repo == "" {
		return queue.Task{}, ErrNotCandidate
	}

	// I1: allowlist gate at ingestion.
	if !g.allowlist[repo] {
		return queue.Task{}, ErrNotAllowlisted
	}

	// PR filter: issues endpoint returns PRs as issues; a PR is not a task.
	if issue.PullRequest != nil {
		return queue.Task{}, ErrNotCandidate
	}

	labels := make(map[string]bool, len(issue.Labels))
	for _, l := range issue.Labels {
		labels[l.Name] = true
	}

	var trigger string
	var tier queue.TaskTier
	switch {
	case labels[LabelHotfix]:
		trigger = "hotfix"
		tier = queue.TierP0
	case labels[LabelReady]:
		trigger = "ready"
		tier = queue.TierP2
	case labels[LabelRedo]:
		trigger = "redo"
		tier = queue.TierP1
	default:
		// P1: issue creation. Two exclusions:
		if issue.User.Type == "Bot" {
			return queue.Task{}, ErrNotCandidate
		}
		// D45 loop-freedom: never re-ingest idle-deck's own writes.
		if labels[LabelNeedsHuman] {
			return queue.Task{}, ErrNotCandidate
		}
		trigger = "created"
		tier = queue.TierP1
	}

	dedupeKey := fmt.Sprintf("github:issue:%s:%d:%s", repo, issue.Number, trigger)
	eventType := "issue.opened"
	if trigger != "created" {
		eventType = fmt.Sprintf("issue.labeled:%s", triggerLabel(trigger))
	}

	task := queue.Task{
		ID:           uuid.New().String(),
		RepositoryID: repo,
		Ticket: queue.TrackerRef{
			Tracker:      "github",
			RepositoryID: repo,
			ExternalID:   fmt.Sprintf("%d", issue.Number),
			URL:          issue.HTMLURL,
		},
		Tier:    tier,
		Prompt:  BuildPrompt(repo, issue.Number, issue.HTMLURL, tier, issue.Title, issue.Body),
		State:   queue.StateQueued,
		TriggeredBy: queue.TriggeredBy{
			EventType:  eventType,
			DedupeKey:  dedupeKey,
			ReceivedAt: time.Now().UnixNano(),
		},
	}
	return task, nil
}

// triggerLabel maps a trigger string to its D29 label constant.
func triggerLabel(trigger string) string {
	switch trigger {
	case "hotfix":
		return LabelHotfix
	case "ready":
		return LabelReady
	case "redo":
		return LabelRedo
	default:
		return trigger
	}
}

// extractRepo pulls "owner/repo" from a GitHub issue HTML URL.
// e.g. https://github.com/acme/widgets/issues/412 → acme/widgets
func extractRepo(htmlURL string) string {
	s := strings.TrimPrefix(htmlURL, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 {
		return ""
	}
	return parts[0] + "/" + parts[1]
}

// Comment implements TrackerSink.Comment: posts a comment on an issue.
func (g *GitHub) Comment(ctx context.Context, ref queue.TrackerRef, body string) error {
	// POST /repos/{owner}/{repo}/issues/{number}/comments
	// Implemented in poller.go via HTTP; this is the sink entry point.
	return g.postComment(ctx, ref, body)
}

// SetLabels implements TrackerSink.SetLabels: idempotent add/remove of labels.
func (g *GitHub) SetLabels(ctx context.Context, ref queue.TrackerRef, add, remove []string) error {
	return g.patchLabels(ctx, ref, add, remove)
}

// OpenIssue implements TrackerSink.OpenIssue: creates an issue for P3 sweep
// findings (ADR 0020, D43).
func (g *GitHub) OpenIssue(ctx context.Context, repo string, title, body string, labels []string) error {
	return g.postIssue(ctx, repo, title, body, labels)
}