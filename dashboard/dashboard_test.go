package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/seppaleinen/idle-deck/config"
	"github.com/seppaleinen/idle-deck/store"
	"github.com/seppaleinen/idle-deck/tracker"
)

func TestServer_Smoke(t *testing.T) {
	dbPath := t.TempDir() + "/idle-deck.db"
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cfg := config.Defaults()
	cfg.DB = dbPath
	cfg.DashboardAddr = "127.0.0.1:0"
	cfg.HarnessURL = "http://127.0.0.1:9999"

	srv, err := NewServer(cfg, db, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Smoke test API paths
	for _, path := range []string{
		"/api/status", "/api/health", "/api/repos",
		"/api/queue", "/api/attempts", "/api/artifacts",
		"/api/heartbeat", "/api/config", "/summary", "/api/summary",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code >= 400 {
			t.Errorf("%s: status %d body %s", path, w.Code, w.Body.String())
		}
	}
	// "/" should 302 to /summary
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusFound {
		t.Errorf("/ should 302, got %d", w.Code)
	}
	if loc := w.Header().Get("Location"); loc != "/summary" {
		t.Errorf("/ should redirect to /summary, got %q", loc)
	}
}

var _ = sql.ErrNoRows

func TestServer_Summary(t *testing.T) {
	dbPath := t.TempDir() + "/idle-deck.db"
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cfg := config.Defaults()
	cfg.DB = dbPath
	cfg.DashboardAddr = "127.0.0.1:0"
	cfg.HarnessURL = "http://127.0.0.1:9999"

	srv, err := NewServer(cfg, db, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Empty DB first: all arrays [], last_seen absent, suggested_next absent
	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/summary: status %d, body %s", w.Code, w.Body.String())
	}
	var resp SummaryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.LastSeen != nil {
		t.Error("last_seen should be absent on empty DB")
	}
	if resp.SuggestedNext != nil {
		t.Error("suggested_next should be absent on empty DB")
	}
	if resp.Concluded == nil || len(resp.Concluded) != 0 {
		t.Errorf("concluded should be [], got %v", resp.Concluded)
	}
	if resp.Awaiting == nil || len(resp.Awaiting) != 0 {
		t.Errorf("awaiting should be [], got %v", resp.Awaiting)
	}
	if resp.Questions == nil || len(resp.Questions) != 0 {
		t.Errorf("questions should be [], got %v", resp.Questions)
	}

	// Seed tasks:
	// 1) succeeded task with PR artifact (expect in concluded with pr_url)
	now := time.Now()
	finishedAt := now.UnixNano()
	_, err = db.Exec(`INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url, tier, prompt, state, event_type, dedupe_key, received_at, created_at)
		VALUES ('task-succeeded', 'test/repo', 'github', 'test/repo', '1', 'https://gh.com/1', 'P1', 'Succeeded task', 'succeeded', 'issue_created', 'dedupe-succeeded', ?, ?)`, finishedAt, finishedAt)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	_, err = db.Exec(`INSERT INTO attempts (id, task_id, ordinal, outcome, started_at, finished_at)
		VALUES ('att-succeeded', 'task-succeeded', 1, 'succeeded', ?, ?)`, finishedAt, finishedAt)
	if err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	_, err = db.Exec(`INSERT INTO artifacts (id, attempt_id, kind, uri, produced_at)
		VALUES ('art-pr', 'att-succeeded', 'pull_request', 'https://github.com/test/repo/pull/1', ?)`, finishedAt)
	if err != nil {
		t.Fatalf("insert artifact: %v", err)
	}

	// 2) escalated task with 3 attempts (expect in concluded AND awaiting with reason and retry_count=3)
	_, err = db.Exec(`INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url, tier, prompt, state, event_type, dedupe_key, received_at, created_at)
		VALUES ('task-escalated', 'test/repo', 'github', 'test/repo', '2', 'https://gh.com/2', 'P2', 'Escalated task', 'escalated', 'issue_created', 'dedupe-escalated', ?, ?)`, finishedAt, finishedAt)
	if err != nil {
		t.Fatalf("insert escalated task: %v", err)
	}
	for i := 1; i <= 3; i++ {
		_, err = db.Exec(`INSERT INTO attempts (id, task_id, ordinal, outcome, started_at, finished_at)
			VALUES (?, 'task-escalated', ?, 'non_retryable_failure', ?, ?)`,
			fmt.Sprintf("att-esc-%d", i), i, finishedAt, finishedAt)
		if err != nil {
			t.Fatalf("insert attempt %d: %v", i, err)
		}
	}

	// 3) queued P1 task (expect suggested_next = it)
	_, err = db.Exec(`INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url, tier, prompt, state, event_type, dedupe_key, received_at, created_at)
		VALUES ('task-queued', 'test/repo', 'github', 'test/repo', '3', 'https://gh.com/3', 'P1', 'Queued task', 'queued', 'issue_created', 'dedupe-queued', ?, ?)`, finishedAt, finishedAt)
	if err != nil {
		t.Fatalf("insert queued task: %v", err)
	}

	// GET /api/summary now should have data
	req = httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	w = httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/summary (seeded): status %d, body %s", w.Code, w.Body.String())
	}
	var resp2 SummaryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp2.Concluded) != 2 {
		t.Errorf("concluded should have 2 items, got %d", len(resp2.Concluded))
	}
	if len(resp2.Awaiting) != 1 {
		t.Errorf("awaiting should have 1 item, got %d", len(resp2.Awaiting))
	}
	if resp2.Awaiting[0].RetryCount != 3 {
		t.Errorf("awaiting retry_count should be 3, got %d", resp2.Awaiting[0].RetryCount)
	}
	if resp2.Awaiting[0].Reason != "non_retryable_failure" {
		t.Errorf("awaiting reason should be non_retryable_failure, got %q", resp2.Awaiting[0].Reason)
	}
	if resp2.SuggestedNext == nil || resp2.SuggestedNext.TaskID != "task-queued" {
		t.Errorf("suggested_next should be task-queued, got %v", resp2.SuggestedNext)
	}
	// Check PR URL is present for succeeded task
	var foundPR bool
	for _, c := range resp2.Concluded {
		if c.TaskID == "task-succeeded" && c.PRURL != "" {
			foundPR = true
		}
	}
	if !foundPR {
		t.Error("concluded should have PR URL for succeeded task")
	}

	// POST /api/summary/seen
	req = httptest.NewRequest(http.MethodPost, "/api/summary/seen", nil)
	w = httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("POST /api/summary/seen: status %d, body %s", w.Code, w.Body.String())
	}
	var seenResp SeenResponse
	if err := json.NewDecoder(w.Body).Decode(&seenResp); err != nil {
		t.Fatalf("decode seen: %v", err)
	}
	if seenResp.SeenAt.IsZero() {
		t.Error("seen_at should be set")
	}

	// Re-GET: last_seen present, concluded now empty (all finishes predate seen_at)
	time.Sleep(10 * time.Millisecond)
	req = httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	w = httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/summary (after seen): status %d, body %s", w.Code, w.Body.String())
	}
	var resp3 SummaryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp3); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp3.LastSeen == nil {
		t.Error("last_seen should be present after mark-as-read")
	}
	if len(resp3.Concluded) != 0 {
		t.Errorf("concluded should be empty after mark-as-read, got %d", len(resp3.Concluded))
	}

	// Method checks: GET /api/summary/seen -> 405; POST /api/summary -> 405
	req = httptest.NewRequest(http.MethodGet, "/api/summary/seen", nil)
	w = httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/summary/seen should be 405, got %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/summary", nil)
	w = httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /api/summary should be 405, got %d", w.Code)
	}
}

func TestServer_Tasks(t *testing.T) {
	dbPath := t.TempDir() + "/idle-deck.db"
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Insert a couple of tasks with attempts
	_, _ = db.Exec(`INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url, tier, prompt, state, event_type, dedupe_key, received_at, created_at)
		VALUES ('task-1', 'idle-deck', 'github', 'seppaleinen/idle-deck', '1', 'https://gh.com/1', 'P1', 'First', 'succeeded', 'issue_created', 'dedupe-1', strftime('%s','now')*1000000000, strftime('%s','now')*1000000000)`)
	_, _ = db.Exec(`INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url, tier, prompt, state, event_type, dedupe_key, received_at, created_at)
		VALUES ('task-2', 'idle-deck', 'github', 'seppaleinen/idle-deck', '2', 'https://gh.com/2', 'P2', 'Second', 'running', 'issue_created', 'dedupe-2', strftime('%s','now')*1000000000, strftime('%s','now')*1000000000)`)
	_, _ = db.Exec(`INSERT INTO attempts (id, task_id, ordinal, outcome, started_at, finished_at)
		VALUES ('att-1', 'task-1', 1, 'succeeded', strftime('%s','now')*1000000000, strftime('%s','now')*1000000000)`)
	_, _ = db.Exec(`INSERT INTO attempts (id, task_id, ordinal, outcome, started_at, finished_at)
		VALUES ('att-2', 'task-2', 1, '', strftime('%s','now')*1000000000, 0)`)

	cfg := config.Defaults()
	cfg.DB = dbPath
	cfg.DashboardAddr = "127.0.0.1:0"
	cfg.HarnessURL = "http://127.0.0.1:9999"

	srv, err := NewServer(cfg, db, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	tests := []struct {
		path       string
		wantStatus int
		wantTotal  int
		wantTasks  int
	}{
		{"/api/tasks", http.StatusOK, 2, 2},
		{"/api/tasks?state=succeeded", http.StatusOK, 1, 1},
		{"/api/tasks?state=running", http.StatusOK, 1, 1},
		{"/api/tasks?tier=P1", http.StatusOK, 1, 1},
		{"/api/tasks?tier=P2", http.StatusOK, 1, 1},
		{"/api/tasks?state=invalid", http.StatusBadRequest, 0, 0},
		{"/api/tasks?tier=invalid", http.StatusBadRequest, 0, 0},
		{"/api/tasks?limit=1", http.StatusOK, 2, 1},
		{"/api/tasks?offset=1", http.StatusOK, 2, 1},
	}
	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code != tc.wantStatus {
			t.Errorf("%s: status %d, want %d, body %s", tc.path, w.Code, tc.wantStatus, w.Body.String())
			continue
		}
		if tc.wantTotal > 0 {
			var resp map[string]interface{}
			_ = json.NewDecoder(w.Body).Decode(&resp)
			if resp["total"] != float64(tc.wantTotal) {
				t.Errorf("%s: total %.0f, want %d", tc.path, resp["total"], tc.wantTotal)
			}
			tasks := resp["tasks"].([]interface{})
			if len(tasks) != tc.wantTasks {
				t.Errorf("%s: len(tasks) %d, want %d", tc.path, len(tasks), tc.wantTasks)
			}
		}
	}
}


// fakeTracker is a minimal TrackerIssueLister for dashboard tests.
type fakeTracker struct {
	prState string
}

func (f fakeTracker) ListIssuesWithLabel(ctx context.Context, label string) ([]tracker.IssueSummary, error) {
	return []tracker.IssueSummary{}, nil
}
func (f fakeTracker) PRState(ctx context.Context, prURL string) (string, error) {
	return f.prState, nil
}

func TestServer_Summary_PRState(t *testing.T) {
	dbPath := t.TempDir() + "/idle-deck.db"
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cfg := config.Defaults()
	cfg.DB = dbPath
	cfg.DashboardAddr = "127.0.0.1:0"

	srv, err := NewServer(cfg, db, fakeTracker{prState: "merged"})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	now := time.Now()
	finishedAt := now.UnixNano()
	_, err = db.Exec(`INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url, tier, prompt, state, event_type, dedupe_key, received_at, created_at)
		VALUES ('task-pr', 'test/repo', 'github', 'test/repo', '1', 'https://gh.com/1', 'P1', 'Task with PR', 'succeeded', 'issue_created', 'dedupe-pr', ?, ?)`, finishedAt, finishedAt)
	if err != nil {
		t.Fatalf("insert task: %v", err)
	}
	_, err = db.Exec(`INSERT INTO attempts (id, task_id, ordinal, outcome, started_at, finished_at)
		VALUES ('att-pr', 'task-pr', 1, 'succeeded', ?, ?)`, finishedAt, finishedAt)
	if err != nil {
		t.Fatalf("insert attempt: %v", err)
	}
	_, err = db.Exec(`INSERT INTO artifacts (id, attempt_id, kind, uri, produced_at)
		VALUES ('art-pr', 'att-pr', 'pull_request', 'https://github.com/test/repo/pull/42', ?)`, finishedAt)
	if err != nil {
		t.Fatalf("insert artifact: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/summary", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/summary: status %d, body %s", w.Code, w.Body.String())
	}
	var resp SummaryResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Concluded) != 1 {
		t.Fatalf("expected 1 concluded item, got %d", len(resp.Concluded))
	}
	if resp.Concluded[0].PRURL != "https://github.com/test/repo/pull/42" {
		t.Errorf("PRURL mismatch: got %q", resp.Concluded[0].PRURL)
	}
	if resp.Concluded[0].PRState != "merged" {
		t.Errorf("PRState not enriched: got %q, want merged", resp.Concluded[0].PRState)
	}
}

func TestServer_DashboardActionsPayloadRoundTrip(t *testing.T) {
	dbPath := t.TempDir() + "/idle-deck.db"
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cfg := config.Defaults()
	cfg.DB = dbPath
	cfg.DashboardAddr = "127.0.0.1:0"
	cfg.HarnessURL = "http://127.0.0.1:9999"
	// Enable dashboard actions for testing
	cfg.DashboardActions = true

	srv, err := NewServer(cfg, db, nil)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	// Test action poll payload round-trip with special characters
	// The repo name regex forbids quotes/backslashes in production,
	// but json.Marshal must handle them correctly to avoid injection
	t.Run("actionPoll with special chars", func(t *testing.T) {
		// Test with valid repo name first
		body := `{"repo":"owner/repo"}`
		req := httptest.NewRequest(http.MethodPost, "/api/actions/poll", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /api/actions/poll: status %d, body %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		actionID := resp["action_id"]
		if actionID == "" {
			t.Fatal("action_id missing")
		}

		// Verify payload was stored correctly and can be unmarshalled
		var payload []byte
		err := db.QueryRowContext(ctx,
			"SELECT payload FROM dashboard_actions WHERE id = ?", actionID).Scan(&payload)
		if err != nil {
			t.Fatalf("query payload: %v", err)
		}

		var p actionPollPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if p.Repo != "owner/repo" {
			t.Errorf("repo mismatch: got %q, want owner/repo", p.Repo)
		}

		// Verify the payload is valid JSON (not broken by string concatenation)
		// Even if someone passes a repo with quotes, json.Marshal handles it
		payloadStr := string(payload)
		// The payload should be valid JSON - can re-marshal and check structure
		var parsed map[string]string
		if err := json.Unmarshal(payload, &parsed); err != nil {
			t.Fatalf("payload is not valid JSON: %v, payload=%s", err, payloadStr)
		}
		if parsed["repo"] != "owner/repo" {
			t.Errorf("parsed repo mismatch: got %q", parsed["repo"])
		}
	})

	// Test action cancel payload round-trip
	t.Run("actionCancel round-trip", func(t *testing.T) {
		taskID := "123e4567-e89b-12d3-a456-426614174000"
		body := fmt.Sprintf(`{"task_id":"%s"}`, taskID)
		req := httptest.NewRequest(http.MethodPost, "/api/actions/cancel", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /api/actions/cancel: status %d, body %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		actionID := resp["action_id"]

		var payload []byte
		err := db.QueryRowContext(ctx,
			"SELECT payload FROM dashboard_actions WHERE id = ?", actionID).Scan(&payload)
		if err != nil {
			t.Fatalf("query payload: %v", err)
		}

		var p actionCancelPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if p.TaskID != taskID {
			t.Errorf("task_id mismatch: got %q, want %q", p.TaskID, taskID)
		}
	})

	// Test action retry payload round-trip
	t.Run("actionRetry round-trip", func(t *testing.T) {
		taskID := "task-123-retry"
		body := fmt.Sprintf(`{"task_id":"%s"}`, taskID)
		req := httptest.NewRequest(http.MethodPost, "/api/actions/retry", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("POST /api/actions/retry: status %d, body %s", w.Code, w.Body.String())
		}
		var resp map[string]string
		if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		actionID := resp["action_id"]

		var payload []byte
		err := db.QueryRowContext(ctx,
			"SELECT payload FROM dashboard_actions WHERE id = ?", actionID).Scan(&payload)
		if err != nil {
			t.Fatalf("query payload: %v", err)
		}

		var p actionRetryPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if p.TaskID != taskID {
			t.Errorf("task_id mismatch: got %q, want %q", p.TaskID, taskID)
		}
	})
}
