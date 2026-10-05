package stubs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitHubIssue represents an issue from the GitHub API.
type GitHubIssue struct {
	Number      int           `json:"number"`
	Title       string        `json:"title"`
	Body        string        `json:"body"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	Labels      []GitHubLabel `json:"labels"`
	User        GitHubUser    `json:"user"`
	PullRequest *struct{}     `json:"pull_request,omitempty"`
	HTMLURL     string        `json:"html_url"`
}

// HasLabel reports whether the issue carries the given label.
func (i GitHubIssue) HasLabel(name string) bool {
	for _, l := range i.Labels {
		if l.Name == name {
			return true
		}
	}
	return false
}

// GitHubLabel represents a label on an issue.
type GitHubLabel struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// GitHubUser represents a GitHub user.
type GitHubUser struct {
	Type string `json:"type"` // "User" or "Bot"
}

// TrackerState holds the mutable state for the tracker stub server.
type TrackerState struct {
	mu           sync.Mutex
	Repo         string
	Issues       map[int]*GitHubIssue
	Watermark    time.Time
	Seeded       bool
	NextIssueNum int
	clock        func() time.Time
}

// NewTrackerState creates a new tracker state for a repo.
func NewTrackerState(repo string) *TrackerState {
	return &TrackerState{
		Repo:         repo,
		Issues:       make(map[int]*GitHubIssue),
		NextIssueNum: 0,
		clock:        func() time.Time { return time.Now().UTC() },
	}
}

// SetClock overrides the internal clock for deterministic tests.
func (s *TrackerState) SetClock(clock func() time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clock = clock
}

// UpsertIssue adds or updates an issue.
func (s *TrackerState) UpsertIssue(issue *GitHubIssue) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue.Number == 0 {
		s.NextIssueNum++
		issue.Number = s.NextIssueNum
	}
	now := s.clock()
	if issue.CreatedAt.IsZero() {
		issue.CreatedAt = now
	}
	if issue.UpdatedAt.IsZero() {
		issue.UpdatedAt = issue.CreatedAt
	}
	if issue.Labels == nil {
		issue.Labels = []GitHubLabel{}
	}
	if issue.HTMLURL == "" && s.Repo != "" {
		issue.HTMLURL = fmt.Sprintf("https://github.com/%s/issues/%d", s.Repo, issue.Number)
	}
	s.Issues[issue.Number] = issue
}

// RemoveIssue removes an issue.
func (s *TrackerState) RemoveIssue(num int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.Issues, num)
}

// SetLabels replaces the labels on an issue and bumps UpdatedAt.
func (s *TrackerState) SetLabels(num int, labels []GitHubLabel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue, ok := s.Issues[num]; ok {
		issue.Labels = labels
		issue.UpdatedAt = s.clock()
	}
}

// AddLabel adds a label to an issue if not present and bumps UpdatedAt.
func (s *TrackerState) AddLabel(num int, label GitHubLabel) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue, ok := s.Issues[num]; ok {
		for _, l := range issue.Labels {
			if l.Name == label.Name {
				return
			}
		}
		issue.Labels = append(issue.Labels, label)
		issue.UpdatedAt = s.clock()
	}
}

// RemoveLabel removes a label from an issue and bumps UpdatedAt.
func (s *TrackerState) RemoveLabel(num int, labelName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue, ok := s.Issues[num]; ok {
		var newLabels []GitHubLabel
		for _, l := range issue.Labels {
			if l.Name != labelName {
				newLabels = append(newLabels, l)
			}
		}
		if len(newLabels) != len(issue.Labels) {
			issue.Labels = newLabels
			issue.UpdatedAt = s.clock()
		}
	}
}

// HasLabel checks if an issue has a label.
func (s *TrackerState) HasLabel(num int, labelName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if issue, ok := s.Issues[num]; ok {
		for _, l := range issue.Labels {
			if l.Name == labelName {
				return true
			}
		}
	}
	return false
}

// AdvanceWatermark advances the watermark to the given time.
// Called after a full pagination sweep completes (§2): advances to the tick's
// start timestamp, so consecutive ticks overlap by design.
func (s *TrackerState) AdvanceWatermark(t time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.After(s.Watermark) {
		s.Watermark = t
	}
}

// TrackerServer wraps an httptest.Server with its state.
type TrackerServer struct {
	*httptest.Server
	State *TrackerState
}

// NewTrackerServer creates a new tracker stub server.
func NewTrackerServer(state *TrackerState) *TrackerServer {
	mux := http.NewServeMux()
	server := &TrackerServer{
		Server: httptest.NewServer(mux),
		State:  state,
	}

	mux.HandleFunc("/repos/", server.handleRepos)
	mux.HandleFunc("/health", server.handleHealth)
	mux.HandleFunc("/", server.handleRoot)

	return server
}

// handleRoot is a catch-all for the base URL (e.g., /).
func (s *TrackerServer) handleRoot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "service": "tracker-stub"})
}

// URL returns the base URL of the tracker server.
func (s *TrackerServer) URL() string {
	return s.Server.URL
}

func (s *TrackerServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (s *TrackerServer) handleRepos(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse /repos/{owner}/{repo}/issues
	path := strings.TrimPrefix(r.URL.Path, "/repos/")
	parts := strings.Split(path, "/")
	if len(parts) < 3 || parts[2] != "issues" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	repo := parts[0] + "/" + parts[1]
	if repo != s.State.Repo {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	// Parse query parameters (event-contract §8)
	query := r.URL.Query()
	stateParam := query.Get("state")
	labelsParam := query.Get("labels")
	sinceParam := query.Get("since")
	sortParam := query.Get("sort")
	directionParam := query.Get("direction")
	perPageParam := query.Get("per_page")
	pageParam := query.Get("page")

	if stateParam != "" && stateParam != "open" {
		http.Error(w, "only open state supported", http.StatusBadRequest)
		return
	}

	// Parse since (watermark) — RFC3339 (§2)
	var since time.Time
	if sinceParam != "" {
		var err error
		since, err = time.Parse(time.RFC3339, sinceParam)
		if err != nil {
			http.Error(w, "invalid since parameter", http.StatusBadRequest)
			return
		}
	}

	// Parse per_page (default 100, §8)
	perPage := 100
	if perPageParam != "" {
		if p, err := strconv.Atoi(perPageParam); err == nil && p > 0 {
			perPage = p
		}
	}

	// Parse page (default 1)
	page := 1
	if pageParam != "" {
		if p, err := strconv.Atoi(pageParam); err == nil && p > 0 {
			page = p
		}
	}

	// Parse labels (comma-separated)
	var labelFilters []string
	if labelsParam != "" {
		labelFilters = strings.Split(labelsParam, ",")
	}

	// Conditional request headers (§0, cost)
	ifModifiedSince := r.Header.Get("If-Modified-Since")
	ifNoneMatch := r.Header.Get("If-None-Match")

	// Collect matching issues
	s.State.mu.Lock()
	var matching []GitHubIssue
	for _, issue := range s.State.Issues {
		// Filter by labels (§8: one label per query)
		if len(labelFilters) > 0 {
			hasLabel := false
			for _, label := range issue.Labels {
				for _, lf := range labelFilters {
					if label.Name == lf {
						hasLabel = true
						break
					}
				}
				if hasLabel {
					break
				}
			}
			if !hasLabel {
				continue
			}
		}

		// Filter by since: updated_at >= since (§2: since filters on updated_at)
		if !since.IsZero() && issue.UpdatedAt.Before(since) {
			continue
		}

		// Filter out pull requests (§8: the issues endpoint returns PRs; a PR is not a task)
		if issue.PullRequest != nil {
			continue
		}

		matching = append(matching, *issue)
	}
	s.State.mu.Unlock()

	// Sort: §2 says ascending by updated; default newest-first
	if sortParam == "updated" && directionParam == "asc" {
		sort.Slice(matching, func(i, j int) bool {
			return matching[i].UpdatedAt.Before(matching[j].UpdatedAt)
		})
	} else {
		sort.Slice(matching, func(i, j int) bool {
			return matching[i].UpdatedAt.After(matching[j].UpdatedAt)
		})
	}

	// Compute ETag and Last-Modified for conditional requests (§0)
	var lastModified time.Time
	if len(matching) > 0 {
		lastModified = matching[0].UpdatedAt
		for _, issue := range matching {
			if issue.UpdatedAt.After(lastModified) {
				lastModified = issue.UpdatedAt
			}
		}
	}

	// ETag is stable for the same data (max updated_at). Empty → fixed etag.
	etag := `"` + lastModified.UTC().Format(time.RFC3339Nano) + `"`
	if lastModified.IsZero() {
		etag = `"empty"`
	}

	// Check conditional requests (§0, cost)
	if ifNoneMatch != "" && ifNoneMatch == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if ifModifiedSince != "" {
		if ims, err := time.Parse(http.TimeFormat, ifModifiedSince); err == nil {
			if !lastModified.IsZero() && !lastModified.After(ims) {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}

	// Pagination: page * per_page (§8)
	start := (page - 1) * perPage
	end := start + perPage
	if start > len(matching) {
		start = len(matching)
	}
	if end > len(matching) {
		end = len(matching)
	}
	pageIssues := matching[start:end]

	// Build response with ETag, Last-Modified, and Link headers
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("ETag", etag)
	if !lastModified.IsZero() {
		w.Header().Set("Last-Modified", lastModified.Format(http.TimeFormat))
	}

	// Link: rel="next" for pages > per_page (§2: following Link: rel="next")
	if end < len(matching) {
		nextURL := buildNextURL(r, page+1)
		w.Header().Set("Link", fmt.Sprintf("<%s>; rel=\"next\"", nextURL))
	}

	json.NewEncoder(w).Encode(pageIssues)
}

// buildNextURL builds an absolute URL for the next page (GitHub returns absolute URLs in Link).
func buildNextURL(r *http.Request, page int) string {
	u := *r.URL
	q := u.Query()
	q.Set("page", strconv.Itoa(page))
	u.RawQuery = q.Encode()
	// Build absolute URL from the request (httptest uses http://host)
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s%s", scheme, r.Host, u.String())
}

// Label vocabulary (D29). All labels are lowercase, hyphenated, idle- prefixed.
var (
	LabelIdleHotfix     = GitHubLabel{Name: "idle-hotfix", Color: "ff0000"}
	LabelIdleReady      = GitHubLabel{Name: "idle-ready", Color: "00ff00"}
	LabelIdleRedo       = GitHubLabel{Name: "idle-redo", Color: "0000ff"}
	LabelIdleNeedsHuman = GitHubLabel{Name: "idle-needs-human", Color: "ffff00"}
)
