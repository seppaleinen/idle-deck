package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seppaleinen/idle-deck/config"
	"github.com/seppaleinen/idle-deck/store"
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

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	for _, path := range []string{
		"/api/status", "/api/health", "/api/repos",
		"/api/queue", "/api/attempts", "/api/artifacts",
		"/api/heartbeat", "/api/config", "/",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code >= 400 {
			t.Errorf("%s: status %d body %s", path, w.Code, w.Body.String())
		}
	}
}

var _ = sql.ErrNoRows

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

	srv, err := NewServer(cfg)
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
