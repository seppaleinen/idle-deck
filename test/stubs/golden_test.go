package stubs

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/seppaleinen/idle-deck/queue"
	"github.com/seppaleinen/idle-deck/store"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Test helpers: HTTP clients that exercise the stub seam just as a real
// adapter would (URL building, pagination, conditional requests, outcome).
// ---------------------------------------------------------------------------

var httpClient = &http.Client{Timeout: 5 * time.Second}

// queryIssues performs GET /repos/{owner}/{repo}/issues with the given params,
// follows Link: rel="next" pagination, and returns all results.
func queryIssues(t *testing.T, baseURL, ownerRepo string, params url.Values) ([]GitHubIssue, string) {
	t.Helper()
	u := fmt.Sprintf("%s/repos/%s/issues", baseURL, ownerRepo)
	if params != nil {
		u = u + "?" + params.Encode()
	}

	var all []GitHubIssue
	var linkHdr string

	for {
		resp, err := httpClient.Get(u)
		if err != nil {
			t.Fatalf("queryIssues: GET %s: %v", u, err)
		}
		if resp.StatusCode == http.StatusNotModified {
			resp.Body.Close()
			return all, linkHdr
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("queryIssues: GET %s: %s", u, resp.Status)
		}
		linkHdr = resp.Header.Get("Link")

		var page []GitHubIssue
		if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
			t.Fatalf("queryIssues: decode: %v", err)
		}
		resp.Body.Close()

		all = append(all, page...)
		if linkHdr == "" {
			break
		}
		u = extractNextLink(linkHdr)
	}
	return all, linkHdr
}

// queryIssuesPage performs a single GET (no pagination follow) and returns
// the raw response body, status, and Link header.
func queryIssuesPage(t *testing.T, baseURL, ownerRepo string, params url.Values) ([]GitHubIssue, int, string) {
	t.Helper()
	u := fmt.Sprintf("%s/repos/%s/issues", baseURL, ownerRepo)
	if params != nil {
		u = u + "?" + params.Encode()
	}

	resp, err := httpClient.Get(u)
	if err != nil {
		t.Fatalf("queryIssuesPage: GET %s: %v", u, err)
	}
	defer resp.Body.Close()

	var page []GitHubIssue
	json.NewDecoder(resp.Body).Decode(&page)
	return page, resp.StatusCode, resp.Header.Get("Link")
}

// extractNextLink parses a Link header and returns the next URL.
func extractNextLink(link string) string {
	for _, part := range strings.Split(link, ",") {
		part = strings.TrimSpace(part)
		if strings.Contains(part, `rel="next"`) {
			start := strings.Index(part, "<") + 1
			end := strings.Index(part, ">")
			if start > 0 && end > start {
				return part[start:end]
			}
		}
	}
	return ""
}

// queryIssuesConditional performs a GET with conditional-request headers.
func queryIssuesConditional(t *testing.T, baseURL, ownerRepo, since, etag, ims string) (int, string) {
	t.Helper()
	params := url.Values{"state": {"open"}}
	if since != "" {
		params.Set("since", since)
	}
	u := fmt.Sprintf("%s/repos/%s/issues?%s", baseURL, ownerRepo, params.Encode())

	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if ims != "" {
		req.Header.Set("If-Modified-Since", ims)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("ETag")
}

// getETag returns the ETag from a simple tracker query.
func getETag(t *testing.T, baseURL, ownerRepo, since string) string {
	t.Helper()
	params := url.Values{"state": {"open"}}
	if since != "" {
		params.Set("since", since)
	}
	u := fmt.Sprintf("%s/repos/%s/issues?%s", baseURL, ownerRepo, params.Encode())
	resp, err := httpClient.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.Header.Get("ETag")
}

// --- Harness HTTP helpers ---

type runStart struct {
	RunID  string `json:"run_id"`
	Status string `json:"status"`
}

func startRun(t *testing.T, baseURL string, req map[string]any) runStart {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpClient.Post(baseURL+"/v1/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("startRun: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("startRun: status=%d body=%s", resp.StatusCode, b)
	}
	var result runStart
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

func getRun(t *testing.T, baseURL, runID string) map[string]any {
	t.Helper()
	resp, err := httpClient.Get(baseURL + "/v1/runs/" + runID)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("getRun: decode %q: %v", b, err)
	}
	return result
}

func deleteRun(t *testing.T, baseURL, runID string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1/runs/"+runID, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

func checkHealth(t *testing.T, baseURL string) HealthResponse {
	t.Helper()
	resp, err := httpClient.Get(baseURL + "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var h HealthResponse
	json.NewDecoder(resp.Body).Decode(&h)
	return h
}

// --- Store helper ---

func newTestQueue(t *testing.T) (*store.SQLiteQueue, *sql.DB) {
	t.Helper()
	dbPath := t.TempDir() + "/golden.db"
	s := store.NewSQLiteQueue(dbPath,
		store.WithLeaseDuration(5*time.Minute),
		store.WithMaxAttempts(3),
	)
	// The queue owns its db handle (unexported); we open a separate connection
	// for read-only verification queries.
	verifier, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open verifier: %v", err)
	}
	t.Cleanup(func() { verifier.Close() })
	return s, verifier
}

// --- Outcome mapping (remote-contract §6) ---

// mapWireOutcome implements the adapter's wire→AttemptOutcome mapping.
// remote-contract §6 — "Wire outcome → AttemptOutcome mapping".
func mapWireOutcome(state string, code *string) queue.AttemptOutcome {
	switch state {
	case "completed":
		return queue.OutcomeSucceeded
	case "failed":
		// Environment-class codes are retryable; task-class are not.
		if code != nil {
			switch *code {
			case "agent_error", "server_error", "setup_error":
				return queue.OutcomeRetryableFailure
			case "test_failure", "budget_exceeded":
				return queue.OutcomeNonRetryableFailure
			}
		}
		return queue.OutcomeRetryableFailure
	case "timed_out":
		return queue.OutcomeTimeout
	case "cancelled":
		// Steered client-side to preempted (D16/I7)
		return queue.OutcomePreempted
	default:
		return queue.OutcomeNonRetryableFailure
	}
}

func strPtr(s string) *string { return &s }

// trackerRef builds a TrackerRef for the test repo.
func trackerRef(repo string, num int) queue.TrackerRef {
	return queue.TrackerRef{
		Tracker:      "github",
		RepositoryID: "repo-1",
		ExternalID:   strconv.Itoa(num),
		URL:          fmt.Sprintf("https://github.com/%s/issues/%d", repo, num),
	}
}

// buildPrompt derives the task prompt per event-contract §7:
//
//	repo:   owner/repo
//	issue:  #123
//	url:    https://github.com/owner/repo/issues/123
//	tier:   P2  (role: do)
//
//	<issue title>
//
//	<issue body>
func buildPrompt(role queue.TaskRole, repo string, issue GitHubIssue) string {
	var sb strings.Builder
	sb.WriteString("repo:   ")
	sb.WriteString(repo)
	sb.WriteString("\nissue:  #")
	sb.WriteString(strconv.Itoa(issue.Number))
	sb.WriteString("\nurl:    ")
	sb.WriteString(issue.HTMLURL)
	sb.WriteString("\ntier:   ")
	switch role {
	case queue.RolePlan:
		sb.WriteString("P1  (role: plan)")
	case queue.RoleDo:
		sb.WriteString("P2  (role: do)")
	case queue.RoleSweep:
		sb.WriteString("P3  (role: sweep)")
	default:
		sb.WriteString(string(role))
	}
	sb.WriteString("\n\n")
	sb.WriteString(issue.Title)
	sb.WriteString("\n\n")
	sb.WriteString(issue.Body)
	return sb.String()
}

// ============================================================================
// Tracker stub tests
// ============================================================================

// TestTrackerConditionalRequests verifies 304 on If-None-Match and If-Modified-Since.
func TestTrackerConditionalRequests(t *testing.T) {
	state := NewTrackerState("acme/widgets")
	server := NewTrackerServer(state)
	defer server.Close()
	base := server.URL()

	updated := time.Date(2026, 9, 29, 14, 0, 0, 0, time.UTC)
	state.UpsertIssue(&GitHubIssue{
		Number:    412,
		Title:     "Test issue",
		Body:      "body",
		CreatedAt: updated.Add(-time.Hour),
		UpdatedAt: updated,
		User:      GitHubUser{Type: "User"},
	})

	since := updated.Add(-2 * time.Hour).Format(time.RFC3339)
	u := fmt.Sprintf("%s/repos/acme/widgets/issues?state=open&since=%s", base, since)

	resp, err := httpClient.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first: got %d, want 200", resp.StatusCode)
	}
	etag := resp.Header.Get("ETag")
	lastMod := resp.Header.Get("Last-Modified")
	resp.Body.Close()
	if etag == "" {
		t.Fatal("no ETag")
	}

	// If-None-Match match → 304
	status, _ := queryIssuesConditional(t, base, "acme/widgets", since, etag, "")
	if status != http.StatusNotModified {
		t.Errorf("If-None-Match match: got %d, want 304", status)
	}

	// If-Modified-Since match → 304
	status, _ = queryItemsConditional(t, base, "acme/widgets", since, "", lastMod)
	if status != http.StatusNotModified {
		t.Errorf("If-Modified-Since match: got %d, want 304", status)
	}

	// If-Modified-Since in the past → 200
	status, _ = queryItemsConditional(t, base, "acme/widgets", since, "",
		updated.Add(-time.Hour).Format(http.TimeFormat))
	if status != http.StatusOK {
		t.Errorf("If-Modified-Since past: got %d, want 200", status)
	}
}

// queryItemsConditional is an alias to avoid name collision.
func queryItemsConditional(t *testing.T, baseURL, ownerRepo, since, etag, ims string) (int, string) {
	return queryItemsConditionalImpl(t, baseURL, ownerRepo, since, etag, ims)
}

func queryItemsConditionalImpl(t *testing.T, baseURL, ownerRepo, since, etag, ims string) (int, string) {
	t.Helper()
	params := url.Values{"state": {"open"}}
	if since != "" {
		params.Set("since", since)
	}
	u := fmt.Sprintf("%s/repos/%s/issues?%s", baseURL, ownerRepo, params.Encode())

	req, _ := http.NewRequest(http.MethodGet, u, nil)
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	if ims != "" {
		req.Header.Set("If-Modified-Since", ims)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("ETag")
}

// TestTrackerPullRequestFilter verifies PRs are excluded.
func TestTrackerPullRequestFilter(t *testing.T) {
	state := NewTrackerState("acme/widgets")
	server := NewTrackerServer(state)
	defer server.Close()
	base := server.URL()

	now := time.Now().UTC()
	state.UpsertIssue(&GitHubIssue{Number: 1, Title: "Issue", CreatedAt: now, UpdatedAt: now})
	state.UpsertIssue(&GitHubIssue{
		Number:      2,
		Title:       "PR",
		CreatedAt:   now,
		UpdatedAt:   now,
		PullRequest: &struct{}{},
	})

	issues, _ := queryIssues(t, base, "acme/widgets",
		url.Values{"state": {"open"}, "since": {"2000-01-01T00:00:00Z"}})
	if len(issues) != 1 || issues[0].Number != 1 {
		t.Errorf("expected issue #1 only (PR filtered), got %v", issues)
	}
}

// TestTrackerPagination verifies Link: rel="next" for pages > per_page.
func TestTrackerPagination(t *testing.T) {
	state := NewTrackerState("acme/widgets")
	server := NewTrackerServer(state)
	defer server.Close()
	base := server.URL()

	now := time.Now().UTC()
	for i := 1; i <= 150; i++ {
		state.UpsertIssue(&GitHubIssue{
			Number:    i,
			Title:     fmt.Sprintf("I%d", i),
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		})
	}

	// Page 1 with per_page=100 should have Link: rel="next"
	issues, status, link := queryIssuesPage(t, base, "acme/widgets",
		url.Values{"state": {"open"}, "since": {"0001-01-01T00:00:00Z"}, "per_page": {"100"}, "page": {"1"}})
	if status != http.StatusOK {
		t.Fatalf("page 1: status %d", status)
	}
	if len(issues) != 100 {
		t.Fatalf("page 1: got %d issues, want 100", len(issues))
	}
	if !strings.Contains(link, `rel="next"`) {
		t.Errorf("page 1: missing Link rel=next, got %q", link)
	}

	// Follow pagination to get all 150
	all, _ := queryIssues(t, base, "acme/widgets",
		url.Values{"state": {"open"}, "since": {"0001-01-01T00:00:00Z"}, "per_page": {"50"}})
	if len(all) != 150 {
		t.Errorf("pagination total: got %d, want 150", len(all))
	}
}

// TestTrackerLabelQueries verifies label-based queries compose with since.
func TestTrackerLabelQueries(t *testing.T) {
	state := NewTrackerState("acme/widgets")
	server := NewTrackerServer(state)
	defer server.Close()
	base := server.URL()

	now := time.Now().UTC()
	state.UpsertIssue(&GitHubIssue{Number: 101, Title: "H", CreatedAt: now, UpdatedAt: now, Labels: []GitHubLabel{LabelIdleHotfix}})
	state.UpsertIssue(&GitHubIssue{Number: 102, Title: "R", CreatedAt: now, UpdatedAt: now, Labels: []GitHubLabel{LabelIdleReady}})
	state.UpsertIssue(&GitHubIssue{Number: 103, Title: "D", CreatedAt: now, UpdatedAt: now, Labels: []GitHubLabel{LabelIdleRedo}})
	state.UpsertIssue(&GitHubIssue{Number: 104, Title: "P", CreatedAt: now, UpdatedAt: now, Labels: []GitHubLabel{}})

	since := now.Add(-time.Hour).Format(time.RFC3339)

	for _, tc := range []struct {
		label string
		want  int
	}{
		{"idle-hotfix", 1}, {"idle-ready", 1}, {"idle-redo", 1},
	} {
		issues, _ := queryIssues(t, base, "acme/widgets",
			url.Values{"state": {"open"}, "labels": {tc.label}, "since": {since}})
		if len(issues) != tc.want {
			t.Errorf("label %s: got %d, want %d", tc.label, len(issues), tc.want)
		}
	}

	// since composition: future since excludes all
	future := now.Add(time.Hour).Format(time.RFC3339)
	issues, _ := queryIssues(t, base, "acme/widgets",
		url.Values{"state": {"open"}, "labels": {"idle-hotfix"}, "since": {future}})
	if len(issues) != 0 {
		t.Errorf("future since: got %d, want 0", len(issues))
	}
}

// TestTrackerWatermarkOverlap verifies watermark advances to tick start and
// overlap by design allows re-observation (event-contract §2).
func TestTrackerWatermarkOverlap(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 9, 0, 0, 0, time.UTC)
	state := NewTrackerState("acme/widgets")
	state.SetClock(func() time.Time { return t0 })
	server := NewTrackerServer(state)
	defer server.Close()
	base := server.URL()

	state.UpsertIssue(&GitHubIssue{Number: 1, Title: "A", CreatedAt: t0, UpdatedAt: t0})
	state.UpsertIssue(&GitHubIssue{Number: 2, Title: "B", CreatedAt: t0.Add(30 * time.Second), UpdatedAt: t0.Add(30 * time.Second)})
	state.UpsertIssue(&GitHubIssue{Number: 3, Title: "C", CreatedAt: t0.Add(60 * time.Second), UpdatedAt: t0.Add(60 * time.Second)})

	// Tick starts at t0+60s; watermark = t0
	tickStart := t0.Add(60 * time.Second)
	issues, _ := queryIssues(t, base, "acme/widgets",
		url.Values{"state": {"open"}, "sort": {"updated"}, "direction": {"asc"}, "since": {t0.Format(time.RFC3339)}, "per_page": {"100"}})
	if len(issues) != 3 {
		t.Fatalf("tick 1: got %d, want 3", len(issues))
	}

	// Watermark advances to tick start → overlap (§2)
	state.AdvanceWatermark(tickStart)
	if !state.Watermark.Equal(tickStart) {
		t.Errorf("watermark = %v, want %v", state.Watermark, tickStart)
	}

	// Next tick queries since=tickStart; issue 3 (updated=tickStart) is in the overlap window
	issues2, _ := queryIssues(t, base, "acme/widgets",
		url.Values{"state": {"open"}, "sort": {"updated"}, "direction": {"asc"}, "since": {state.Watermark.Format(time.RFC3339)}, "per_page": {"100"}})
	if len(issues2) != 1 || issues2[0].Number != 3 {
		t.Errorf("overlap tick: got %v, want just issue 3", issues2)
	}
}

// ============================================================================
// Harness stub tests
// ============================================================================

func TestHarnessIdempotency(t *testing.T) {
	state := NewHarnessState("stub-model")
	server := NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	attemptID := "a1b2c3d4-1111-0000-0000-000000000001"
	body := map[string]any{
		"attempt_id": attemptID, "role": "plan", "prompt": "x",
		"budget": 5000, "timeout_seconds": 900,
		"repository": RepositoryInfoForTest(), "ticket": TicketInfoForTest("42"),
	}

	first := startRun(t, base, body)
	if first.Status != "running" {
		t.Fatalf("first: status=%q", first.Status)
	}

	// Duplicate attempt_id → 200 + same run_id (remote-contract §3)
	raw, _ := json.Marshal(body)
	resp, err := httpClient.Post(base+"/v1/runs", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("duplicate: got %d, want 200", resp.StatusCode)
	}
	var second runStart
	json.NewDecoder(resp.Body).Decode(&second)
	resp.Body.Close()
	if second.RunID != first.RunID {
		t.Errorf("duplicate run_id: %s vs %s", second.RunID, first.RunID)
	}

	// Different attempt_id → different run_id
	body["attempt_id"] = "different-attempt"
	third := startRun(t, base, body)
	if third.RunID == first.RunID {
		t.Error("different attempt_id should produce different run_id")
	}
}

func TestHarnessRunningToTerminal(t *testing.T) {
	state := NewHarnessState("stub-model")
	server := NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	rid := startRun(t, base, map[string]any{
		"attempt_id": "a2", "role": "do", "prompt": "x", "budget": 100000,
		"timeout_seconds": 10800, "repository": RepositoryInfoForTest(),
		"ticket": TicketInfoForTest("77"),
	})

	branch := "feature"
	state.CompleteRun(rid.RunID,
		&HarnessOutcome{State: "completed"},
		[]HarnessArtifact{
			{Kind: "pull_request", URI: "https://github.com/acme/widgets/pull/89"},
			{Kind: "branch", BranchPurpose: &branch, URI: "https://github.com/acme/widgets/tree/feature/77-x"},
			{Kind: "log", URI: "artifact-store/" + rid.RunID + "/run.log"},
		})

	result := getRun(t, base, rid.RunID)
	outcome := result["outcome"].(map[string]any)
	if outcome["state"] != "completed" {
		t.Errorf("state: got %v, want completed", outcome["state"])
	}

	artifacts := result["artifacts"].([]any)
	if len(artifacts) != 3 {
		t.Fatalf("artifacts: got %d, want 3", len(artifacts))
	}

	// branch_purpose only on branch kind (remote-contract §6)
	for _, a := range artifacts {
		am := a.(map[string]any)
		if am["kind"] == "branch" {
			if am["branch_purpose"] != "feature" {
				t.Errorf("branch purpose: %v", am["branch_purpose"])
			}
		} else {
			if bp, ok := am["branch_purpose"]; ok && bp != nil {
				t.Errorf("non-branch kind %s has branch_purpose: %v", am["kind"], bp)
			}
		}
	}
}

func TestHarnessAbort(t *testing.T) {
	state := NewHarnessState("stub-model")
	server := NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	// Unknown run → 404 (remote-contract §7)
	if code := deleteRun(t, base, "nope"); code != http.StatusNotFound {
		t.Errorf("DELETE unknown: got %d, want 404", code)
	}

	rid := startRun(t, base, map[string]any{
		"attempt_id": "a3", "role": "do", "prompt": "x", "budget": 20000,
		"timeout_seconds": 3600, "repository": RepositoryInfoForTest(), "ticket": nil,
	})

	// DELETE running → 204 (remote-contract §4: confirmed dead)
	if code := deleteRun(t, base, rid.RunID); code != http.StatusNoContent {
		t.Errorf("DELETE running: got %d, want 204", code)
	}

	// After abort: terminal, cancelled
	result := getRun(t, base, rid.RunID)
	outcome := result["outcome"].(map[string]any)
	if outcome["state"] != "cancelled" {
		t.Errorf("after abort: state=%v, want cancelled", outcome["state"])
	}

	// DELETE again on terminal → 204 (idempotent)
	if code := deleteRun(t, base, rid.RunID); code != http.StatusNoContent {
		t.Errorf("DELETE already-dead: got %d, want 204", code)
	}
}

func TestHarnessHealthReachability(t *testing.T) {
	state := NewHarnessState("stub-gpt-4o")
	server := NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	// Default: reachable
	h := checkHealth(t, base)
	if !h.Reachable {
		t.Error("health: expected reachable")
	}
	if h.Model != "stub-gpt-4o" {
		t.Errorf("model=%q", h.Model)
	}

	// Model down but process alive → NOT idle (D31)
	state.SetHealth(false, "stub-gpt-4o")
	h2 := checkHealth(t, base)
	if h2.Reachable {
		t.Error("health: expected unreachable when model down")
	}
}

func TestHarnessOutcomeMapping(t *testing.T) {
	cases := []struct {
		name     string
		state    string
		code     *string
		expected queue.AttemptOutcome
	}{
		{"completed", "completed", nil, queue.OutcomeSucceeded},
		{"failed-retryable", "failed", strPtr("agent_error"), queue.OutcomeRetryableFailure},
		{"failed-non-retryable", "failed", strPtr("test_failure"), queue.OutcomeNonRetryableFailure},
		{"timed_out", "timed_out", nil, queue.OutcomeTimeout},
		{"cancelled", "cancelled", nil, queue.OutcomePreempted},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapWireOutcome(tc.state, tc.code)
			if got != tc.expected {
				t.Errorf("wire %s/%v → %s, want %s", tc.state, *tc.code, got, tc.expected)
			}
		})
	}
}

// ============================================================================
// Golden path: full lifecycle driven through the stubs + real queue
// ============================================================================

// TestGoldenPath exercises the end-to-end lifecycle described in operator-surface §9:
// a P1 issue appears → a plan run returns a comment artifact → the operator applies
// idle-ready → a P2 run returns a pull_request artifact and the trigger label is
// removed → the run escalates, so idle-needs-human is applied and a debug branch
// is preserved → idle-redo produces a new task with derived_from and a fresh retry
// budget.
//
// The stubs implement the GitHub and remote contracts faithfully, so the test
// exercises the same seam (URL building, pagination, conditional requests,
// outcome mapping, dedupe keying, label lifecycle) that real adapters would.
func TestGoldenPath(t *testing.T) {
	repo := "acme/widgets"
	t0 := time.Date(2026, 9, 29, 9, 0, 0, 0, time.UTC)
	// Watermark seeded before the issue was created
	wmSeeded := t0.Add(-time.Hour)

	// --- Stubs ---
	tState := NewTrackerState(repo)
	tServer := NewTrackerServer(tState)
	defer tServer.Close()
	tBase := tServer.URL()

	hState := NewHarnessState("stub-gpt-4o")
	hServer := NewHarnessServer(hState)
	defer hServer.Close()
	hBase := hServer.URL()

	// --- Real queue ---
	q, db := newTestQueue(t)

	// Use a timeout context to catch any blocking issues
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	t.Logf("STEP 0: Setup complete")

	// Seed a P1 issue (human-authored, no PR, created after the watermark)
	p1Num := 412
	tState.UpsertIssue(P1Issue(p1Num, "Add caching layer", "We need Redis-backed caching.", t0, t0))
	tState.AdvanceWatermark(wmSeeded)

	// =====================================================================
	// STEP 1: P1 ingestion — a new issue becomes a plan task
	// =====================================================================

	// Query 4 (event-contract §8): watermark query returns all open issues
	// updated since the watermark, sorted ascending.
	p1Candidates, _ := queryIssues(t, tBase, repo,
		url.Values{
			"state":     {"open"},
			"sort":      {"updated"},
			"direction": {"asc"},
			"since":     {tState.Watermark.Format(time.RFC3339)},
			"per_page":  {"100"},
		})

	// Adapter-side P1 filters (event-contract §3):
	//   created_at > watermark  (newly filed)
	//   user.type != "Bot"      (no bot-authored issues)
	//   no idle-needs-human     (D45: loop-prevention, don't re-ingest own artifacts)
	var p1Tasks []queue.Task
	for _, issue := range p1Candidates {
		if !issue.CreatedAt.After(tState.Watermark) {
			continue
		}
		if issue.User.Type == "Bot" {
			continue
		}
		if issue.HasLabel("idle-needs-human") {
			continue
		}
		role := queue.RoleForTier(queue.TierP1)
		p1Tasks = append(p1Tasks, queue.Task{
			ID:             "task-p1-" + strconv.Itoa(issue.Number),
			RepositoryID:   "repo-1",
			Ticket:         trackerRef(repo, issue.Number),
			Tier:           queue.TierP1,
			Prompt:         buildPrompt(role, repo, issue),
			Budget:         5000,
			TimeoutSeconds: 900,
			TriggeredBy: queue.TriggeredBy{
				EventType:  "issue.opened",
				DedupeKey:  fmt.Sprintf("github:issue:%s:%d:created", repo, issue.Number),
				ReceivedAt: t0.UnixNano(),
			},
			CreatedAt: t0.UnixNano(),
		})
	}

	if len(p1Tasks) != 1 {
		t.Fatalf("P1 ingestion: expected 1 task, got %d", len(p1Tasks))
	}

	// Watermark advances to tick start (event-contract §2: advance to tick start, overlap by design)
	tState.AdvanceWatermark(t0)

	// Enqueue (idempotent per dedupe_key — re-enqueueing is a no-op)
	if err := q.Enqueue(ctx, p1Tasks[0]); err != nil {
		t.Fatalf("enqueue P1: %v", err)
	}
	if err := q.Enqueue(ctx, p1Tasks[0]); err != nil {
		t.Fatalf("re-enqueue P1 (idempotent): %v", err)
	}

	// Dequeue with timeout
	t.Logf("STEP 1: Dequeue P1")
	deqCtx, deqCancel := context.WithTimeout(ctx, 5*time.Second)
	lease, err := q.Dequeue(deqCtx)
	deqCancel()
	if err != nil {
		t.Fatalf("dequeue P1: %v", err)
	}
	if lease.TaskID != p1Tasks[0].ID {
		t.Errorf("dequeued task: got %s, want %s", lease.TaskID, p1Tasks[0].ID)
	}

	started := startRun(t, hBase, map[string]any{
		"attempt_id":      lease.AttemptID,
		"role":            "plan",
		"prompt":          p1Tasks[0].Prompt,
		"budget":          5000,
		"timeout_seconds": 900,
		"repository":      RepositoryInfoForTest(),
		"ticket":          TicketInfoForTest(strconv.Itoa(p1Num)),
	})

	hState.CompleteRun(started.RunID,
		&HarnessOutcome{State: "completed"},
		[]HarnessArtifact{
			{Kind: "comment", URI: fmt.Sprintf("https://github.com/%s/issues/%d#issuecomment-1", repo, p1Num), Body: "Clarifying questions..."},
			{Kind: "log", URI: "artifact-store/" + started.RunID + "/run.log"},
		})

	result := getRun(t, hBase, started.RunID)
	outcome := result["outcome"].(map[string]any)
	if mapWireOutcome(outcome["state"].(string), nil) != queue.OutcomeSucceeded {
		t.Error("P1 outcome mapping: want succeeded")
	}

	// Verify comment artifact (P1 plan → comment)
	haveComment := false
	for _, a := range result["artifacts"].([]any) {
		am := a.(map[string]any)
		if am["kind"] == "comment" {
			haveComment = true
			if !strings.Contains(am["body"].(string), "Clarifying") {
				t.Errorf("comment body: %v", am["body"])
			}
		}
	}
	if !haveComment {
		t.Error("P1 run missing comment artifact")
	}

	// Ack (I2 satisfied)
	t.Logf("STEP 1: Ack P1")
	if err := q.Ack(ctx, lease, lease.AttemptID); err != nil {
		t.Fatalf("ack P1: %v", err)
	}
	t.Logf("STEP 1: Done")

	// =====================================================================
	// STEP 2: idle-ready → P2 run → PR artifact → idle-ready removed on terminal
	// =====================================================================
	// Apply idle-ready label (GitHub bumps updated_at)
	tState.AddLabel(p1Num, LabelIdleReady)

	// Query 2 (event-contract §8): idle-ready candidates since watermark
	p2Issues, _ := queryIssues(t, tBase, repo,
		url.Values{"state": {"open"}, "labels": {"idle-ready"}, "since": {tState.Watermark.Format(time.RFC3339)}})
	if len(p2Issues) != 1 || p2Issues[0].Number != p1Num {
		t.Fatalf("P2 query: got %v", p2Issues)
	}

	p2Task := queue.Task{
		ID:             "task-p2-" + strconv.Itoa(p1Num),
		RepositoryID:   "repo-1",
		Ticket:         trackerRef(repo, p1Num),
		Tier:           queue.TierP2,
		Prompt:         buildPrompt(queue.RoleForTier(queue.TierP2), repo, p2Issues[0]),
		Budget:         100000,
		TimeoutSeconds: 10800,
		TriggeredBy: queue.TriggeredBy{
			EventType:  "issue.labeled:idle-ready",
			DedupeKey:  fmt.Sprintf("github:issue:%s:%d:ready", repo, p1Num),
			ReceivedAt: time.Now().UnixNano(),
		},
		CreatedAt: time.Now().UnixNano(),
	}
	if err := q.Enqueue(ctx, p2Task); err != nil {
		t.Fatalf("enqueue P2: %v", err)
	}

	lease2, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("dequeue P2: %v", err)
	}
	started2 := startRun(t, hBase, map[string]any{
		"attempt_id":      lease2.AttemptID,
		"role":            "do",
		"prompt":          p2Task.Prompt,
		"budget":          100000,
		"timeout_seconds": 10800,
		"repository":      RepositoryInfoForTest(),
		"ticket":          TicketInfoForTest(strconv.Itoa(p1Num)),
	})

	branchPurpose := "feature"
	branchName := fmt.Sprintf("feature/%d-add-caching", p1Num)
	hState.CompleteRun(started2.RunID,
		&HarnessOutcome{State: "completed"},
		[]HarnessArtifact{
			{Kind: "pull_request", URI: fmt.Sprintf("https://github.com/%s/pull/%d", repo, 89)},
			{Kind: "branch", BranchPurpose: &branchPurpose, URI: fmt.Sprintf("https://github.com/%s/tree/%s", repo, branchName)},
			{Kind: "log", URI: "artifact-store/" + started2.RunID + "/run.log"},
		})

	result2 := getRun(t, hBase, started2.RunID)
	// I4: a succeeded P2 attempt has a pull_request artifact
	havePR := false
	for _, a := range result2["artifacts"].([]any) {
		am := a.(map[string]any)
		if am["kind"] == "pull_request" {
			havePR = true
		}
	}
	if !havePR {
		t.Error("P2 run missing pull_request artifact (I4 violation)")
	}

	// Label lifecycle (§6): trigger label removed on terminal state
	tState.RemoveLabel(p1Num, "idle-ready")
	if tState.HasLabel(p1Num, "idle-ready") {
		t.Error("idle-ready should have been removed on terminal state")
	}

	// Ack P2
	if err := q.Ack(ctx, lease2, lease2.AttemptID); err != nil {
		t.Fatalf("ack P2: %v", err)
	}

	// =====================================================================
	// STEP 3: Escalation — P2 fails, 2 retries exhausted → escalate
	// =====================================================================
	// Re-apply idle-ready to simulate a new escalation scenario
	tState.AddLabel(p1Num, LabelIdleReady)

	escalateTask := queue.Task{
		ID:             "task-esc-" + strconv.Itoa(p1Num),
		RepositoryID:   "repo-1",
		Ticket:         trackerRef(repo, p1Num),
		Tier:           queue.TierP2,
		Prompt:         buildPrompt(queue.RoleForTier(queue.TierP2), repo, p2Issues[0]),
		Budget:         100000,
		TimeoutSeconds: 10800,
		TriggeredBy: queue.TriggeredBy{
			EventType:  "issue.labeled:idle-ready",
			DedupeKey:  fmt.Sprintf("github:issue:%s:%d:ready-esc", repo, p1Num),
			ReceivedAt: time.Now().UnixNano(),
		},
		CreatedAt: time.Now().UnixNano(),
	}
	if err := q.Enqueue(ctx, escalateTask); err != nil {
		t.Fatalf("enqueue escalate: %v", err)
	}

	// Two retries (maxAttempts=3) then escalate per D14
	for attempt := 0; attempt < 3; attempt++ {
		deqCtx, deqCancel := context.WithTimeout(ctx, 5*time.Second)
		el, err := q.Dequeue(deqCtx)
		deqCancel()
		if err != nil {
			t.Fatalf("dequeue attempt %d: %v", attempt, err)
		}

		run := startRun(t, hBase, map[string]any{
			"attempt_id":      el.AttemptID,
			"role":            "do",
			"prompt":          escalateTask.Prompt,
			"budget":          100000,
			"timeout_seconds": 10800,
			"repository":      RepositoryInfoForTest(),
			"ticket":          TicketInfoForTest(strconv.Itoa(p1Num)),
		})

		isLast := attempt == 2
		if isLast {
			// Final failure → escalate. Debug branch preserved (I5)
			dbPurpose := "debug"
			hState.CompleteRun(run.RunID,
				&HarnessOutcome{State: "failed", Code: strPtr("agent_error"), Message: "Agent error"},
				[]HarnessArtifact{
					{Kind: "branch", BranchPurpose: &dbPurpose, URI: fmt.Sprintf("https://github.com/%s/tree/debug/%s", repo, escalateTask.ID)},
					{Kind: "log", URI: "artifact-store/" + run.RunID + "/partial.log"},
				})
		} else {
			hState.CompleteRun(run.RunID,
				&HarnessOutcome{State: "failed", Code: strPtr("agent_error"), Message: "Transient"},
				[]HarnessArtifact{
					{Kind: "log", URI: "artifact-store/" + run.RunID + "/run.log"},
				})
		}

		// Nack with retryable failure
		if err := q.Nack(ctx, el, el.AttemptID, queue.OutcomeRetryableFailure); err != nil {
			t.Fatalf("nack attempt %d: %v", attempt, err)
		}
	}

	// Verify task escalated
	var escState string
	db.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id = ?", escalateTask.ID).
		Scan(&escState)
	if escState != "escalated" {
		t.Errorf("escalated state: got %q, want escalated", escState)
	}

	// Apply idle-needs-human on escalation (D14)
	tState.AddLabel(p1Num, LabelIdleNeedsHuman)

	// =====================================================================
	// STEP 4: idle-redo → new task with derived_from, fresh retry budget
	// =====================================================================
	tState.AddLabel(p1Num, LabelIdleRedo)

	// Query 3: idle-redo candidates
	redoIssues, _ := queryIssues(t, tBase, repo,
		url.Values{"state": {"open"}, "labels": {"idle-redo"}, "since": {tState.Watermark.Format(time.RFC3339)}})
	if len(redoIssues) != 1 {
		t.Fatalf("redo query: got %d, want 1", len(redoIssues))
	}

	// Redefinition: new task with derived_from (ontology §Redefinition)
	redoTask := queue.Task{
		ID:             "task-redo-" + strconv.Itoa(p1Num),
		RepositoryID:   "repo-1",
		Ticket:         trackerRef(repo, p1Num),
		Tier:           queue.TierP1, // redefinition defaults to P1
		Prompt:         buildPrompt(queue.RoleForTier(queue.TierP1), repo, redoIssues[0]),
		Budget:         5000,
		TimeoutSeconds: 900,
		TriggeredBy: queue.TriggeredBy{
			EventType:  "issue.labeled:idle-redo",
			DedupeKey:  fmt.Sprintf("github:issue:%s:%d:redo", repo, p1Num),
			ReceivedAt: time.Now().UnixNano(),
		},
		DerivedFrom: escalateTask.ID, // lineage pointer to the escalated task
		CreatedAt:   time.Now().UnixNano(),
	}
	if err := q.Enqueue(ctx, redoTask); err != nil {
		t.Fatalf("enqueue redo: %v", err)
	}

	// Verify derived_from persisted (I10: escalated task stays escalated forever)
	var derivedFrom string
	db.QueryRowContext(ctx, "SELECT derived_from FROM tasks WHERE id = ?", redoTask.ID).
		Scan(&derivedFrom)
	if derivedFrom != escalateTask.ID {
		t.Errorf("derived_from: got %q, want %q", derivedFrom, escalateTask.ID)
	}

	var redoState string
	db.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id = ?", redoTask.ID).
		Scan(&redoState)
	if redoState != "queued" {
		t.Errorf("redo task state: got %q, want queued", redoState)
	}

	// Verify dedupe key stored correctly
	var dk string
	db.QueryRowContext(ctx, "SELECT dedupe_key FROM tasks WHERE id = ?", redoTask.ID).
		Scan(&dk)
	if dk != fmt.Sprintf("github:issue:%s:%d:redo", repo, p1Num) {
		t.Errorf("redo dedupe_key: got %q", dk)
	}

	// Verify the escalated task is still escalated (I10: never returns to queued)
	var escStateAgain string
	db.QueryRowContext(ctx, "SELECT state FROM tasks WHERE id = ?", escalateTask.ID).
		Scan(&escStateAgain)
	if escStateAgain != "escalated" {
		t.Errorf("escalated task state changed: got %q, want escalated (I10)", escStateAgain)
	}

	// =====================================================================
	// Cross-cutting assertions
	// =====================================================================

	// D45: P1 skips issues carrying idle-needs-human — the issue now has both
	// idle-redo and idle-needs-human labels. Re-run the P1 query and apply
	// the adapter-side filter; the issue must be excluded.
	p1After, _ := queryIssues(t, tBase, repo,
		url.Values{
			"state":     {"open"},
			"sort":      {"updated"},
			"direction": {"asc"},
			"since":     {tState.Watermark.Format(time.RFC3339)},
			"per_page":  {"100"},
		})
	found := false
	for _, issue := range p1After {
		if issue.Number == p1Num {
			found = true
			// Adapter-side filter: created_at > watermark, not bot, no idle-needs-human
			if issue.CreatedAt.After(tState.Watermark) &&
				issue.User.Type != "Bot" &&
				!issue.HasLabel("idle-needs-human") {
				t.Error("P1 candidate with idle-needs-human passed adapter filter (D45 violation)")
			}
			break
		}
	}
	if !found {
		t.Error("P1 query should return the issue (raw query), but adapter filter must exclude it")
	}

	// Conditional requests work against the tracker throughout
	etag := getETag(t, tBase, repo, tState.Watermark.Format(time.RFC3339))
	if etag == "" {
		t.Error("expected ETag after golden path")
	}
	condStatus, _ := queryItemsConditional(t, tBase, repo,
		tState.Watermark.Format(time.RFC3339), etag, "")
	if condStatus != http.StatusNotModified {
		t.Errorf("conditional after golden path: got %d, want 304", condStatus)
	}

	// Watermark has advanced past the initial seed
	if !tState.Watermark.After(wmSeeded) {
		t.Error("watermark should have advanced past the initial seed")
	}
}
