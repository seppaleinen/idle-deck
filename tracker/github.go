package tracker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
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

// cloneURL maps an owner/repo string to an https:// clone URL using the
// tracker's apiURL as the source of truth for the host.
//   - apiURL host github.com → clone host github.com
//   - apiURL host other (GHE) → use apiURL host directly (API and clone share host)
//   - SSH (git@) is not supported; returns empty string
func (g *GitHub) cloneURL(repo string) string {
	// Handle URLs without scheme by prepending https://
	apiURL := g.apiURL
	if !strings.Contains(apiURL, "://") {
		apiURL = "https://" + apiURL
	}
	u, err := url.Parse(apiURL)
	if err != nil {
		return ""
	}
	var host string
	switch u.Hostname() {
	case "api.github.com":
		host = "github.com"
	default:
		// For GHE or other instances, API and clone share a host
		host = u.Hostname()
	}
	// Preserve port if present
	if u.Port() != "" {
		host = net.JoinHostPort(host, u.Port())
	}
	return fmt.Sprintf("https://%s/%s.git", host, repo)
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
		RepositoryURL: g.cloneURL(repo),
		Ticket: queue.TrackerRef{
			Tracker:      "github",
			RepositoryID: repo,
			ExternalID:   fmt.Sprintf("%d", issue.Number),
			URL:          issue.HTMLURL,
		},
		Tier:   tier,
		Prompt: BuildPrompt(repo, issue.Number, issue.HTMLURL, tier, issue.Title, issue.Body),
		State:  queue.StateQueued,
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
//
// The host is stripped generically, not matched against github.com: a
// GitHub Enterprise instance serves the same URL shape on its own host, and
// the host carries no trust decision — the allowlist gate at I1 authorizes
// the repo, not the hostname. Matching github.com here would silently
// corrupt the repo ref for any non-github.com deployment.
func extractRepo(htmlURL string) string {
	s := htmlURL
	if idx := strings.Index(s, "://"); idx >= 0 {
		s = s[idx+3:]
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		s = s[idx+1:]
	}
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

// PRState implements TrackerIssueLister.PRState (dashboard review-inbox #36).
// It fetches a pull request via the GitHub API and returns a normalized state:
// "merged", "draft", "open", or "closed". Errors are returned as-is; the
// dashboard degrades gracefully when PR state cannot be determined.
// The PR URL must be a GitHub/GHE URL of the form:
// https://github.com/owner/repo/pull/123.
func (g *GitHub) PRState(ctx context.Context, prURL string) (string, error) {
	owner, repo, number, err := parsePRURL(prURL)
	if err != nil {
		return "", fmt.Errorf("tracker: pr state: invalid URL %s: %w", prURL, err)
	}
	// The API path is the same for GitHub.com and GHE. g.apiURL already
	// points to the correct base (api.github.com or GHE api host).
	endpoint := fmt.Sprintf("%s/repos/%s/%s/pulls/%d", g.apiURL, owner, repo, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", fmt.Errorf("tracker: pr state request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("tracker: pr state request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("tracker: pr state %s/%s#%d: %d %s", owner, repo, number, resp.StatusCode, string(b))
	}
	var pr struct {
		State    string `json:"state"`    // "open" | "closed"
		Draft    bool   `json:"draft"`
		Merged   bool   `json:"merged"`   // true if merged
		MergedAt *string `json:"merged_at"` // nil if not merged
	}
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return "", fmt.Errorf("tracker: pr state decode: %w", err)
	}
	switch {
	case pr.Merged:
		return "merged", nil
	case pr.State == "open" && pr.Draft:
		return "draft", nil
	case pr.State == "open":
		return "open", nil
	default:
		return "closed", nil
	}
}

// parsePRURL extracts owner/repo/number from a GitHub/GHE pull request URL.
// Expected format: https://host/owner/repo/pull/123
func parsePRURL(u string) (owner, repo string, number int, err error) {
	p, err := url.Parse(u)
	if err != nil {
		return "", "", 0, err
	}
	segs := strings.Split(strings.Trim(p.Path, "/"), "/")
	if len(segs) != 4 || segs[2] != "pull" {
		return "", "", 0, fmt.Errorf("unexpected path %s", p.Path)
	}
	n, err := strconv.Atoi(segs[3])
	if err != nil {
		return "", "", 0, fmt.Errorf("invalid pull number %q", segs[3])
	}
	return segs[0], segs[1], n, nil
}

type IssueSummary struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	URL    string `json:"url"`
}

// ListIssuesWithLabel returns all OPEN issues carrying the given label,
// across every allowlisted repository (D30). The dashboard review-inbox
// uses this for its questions section with LabelNeedsHuman (D29, D43, D45).
// Results are sorted by repo, then issue number, for deterministic output.
func (g *GitHub) ListIssuesWithLabel(ctx context.Context, label string) ([]IssueSummary, error) {
	var out []IssueSummary
	for repo := range g.allowlist {
		issues, err := g.listIssuesForRepo(ctx, repo, label)
		if err != nil {
			return nil, err
		}
		out = append(out, issues...)
	}
	if out == nil {
		out = []IssueSummary{}
	}
	sortIssuesByRepoThenNumber(out)
	return out, nil
}

// listIssuesForRepo performs one GET against the issues endpoint of a single
// allowlisted repo, filtered by the given label. It skips pull requests (the
// issues endpoint returns PRs; keep only real issues) and returns a non-nil
// slice on success. Any HTTP status >= 400 fails fast with a descriptive
// error (the dashboard surfaces a 500 — simplest correct behaviour).
func (g *GitHub) listIssuesForRepo(ctx context.Context, repo, label string) ([]IssueSummary, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/issues?state=open&labels=%s&per_page=100",
		g.apiURL, repo, url.QueryEscape(label))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("tracker: list issues %s: %w", repo, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("tracker: list issues %s: status %d: %s", repo, resp.StatusCode, string(b))
	}
	var issues []githubIssue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, fmt.Errorf("tracker: list issues %s: decode: %w", repo, err)
	}
	var out []IssueSummary
	for _, iss := range issues {
		if iss.PullRequest != nil {
			continue
		}
		out = append(out, IssueSummary{
			Repo:   repo,
			Number: iss.Number,
			Title:  iss.Title,
			URL:    iss.HTMLURL,
		})
	}
	if out == nil {
		out = []IssueSummary{}
	}
	return out, nil
}

// sortIssuesByRepoThenNumber sorts in place by Repo, then Number, for
// deterministic dashboard output.
func sortIssuesByRepoThenNumber(issues []IssueSummary) {
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Repo != issues[j].Repo {
			return issues[i].Repo < issues[j].Repo
		}
		return issues[i].Number < issues[j].Number
	})
}

// ValidateRead performs one authenticated read against the tracker
// (operator-surface §5 step 2): confirms the PAT is valid and has Issues
// read access. It GETs the first configured repo's issue list; a 200 means
// the credential is good. 401/403 is an auth failure; 404 means the repo
// is not accessible.
func (g *GitHub) ValidateRead(ctx context.Context) error {
	if len(g.allowlist) == 0 {
		return nil
	}
	repo := ""
	for r := range g.allowlist {
		repo = r
		break
	}
	endpoint := fmt.Sprintf("%s/repos/%s/issues?per_page=1", g.apiURL, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("tracker: read: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("tracker: read: status %d: %s", resp.StatusCode, string(b))
}

// LabelExists reports whether the given label exists in the repository.
// Used by `idle-deck check` step 5 (operator-surface §5) to report which of
// the four D29 labels are present and print `gh label create` for missing ones.
// Returns (false, nil) on 404 (label absent); an error on any other failure.
func (g *GitHub) LabelExists(ctx context.Context, repo, label string) (bool, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/labels/%s", g.apiURL, repo, url.QueryEscape(label))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		b, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("label %s: %d: %s", label, resp.StatusCode, string(b))
	}
}
