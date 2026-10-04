package stubs

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

// HarnessRun represents a run in the harness stub.
type HarnessRun struct {
	ID              string
	AttemptID       string
	Role            string
	Prompt          string
	Budget          int
	TimeoutSeconds  int
	Repository      RepositoryInfo
	Ticket          *TicketInfo
	Status          string        // "running" or "terminal"
	StartedAt       time.Time
	LastActivityAt  time.Time
	Outcome         *HarnessOutcome
	Artifacts       []HarnessArtifact
	Logs            []string
	LogOffset       int
	Cancelled       bool
}

// RepositoryInfo represents repository information in a run request.
type RepositoryInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Tracker string `json:"tracker"`
}

// TicketInfo represents ticket information in a run request.
type TicketInfo struct {
	Tracker      string `json:"tracker"`
	ExternalID   string `json:"external_id"`
	URL          string `json:"url"`
}

// HarnessOutcome represents the outcome of a terminal run.
type HarnessOutcome struct {
	State   string  `json:"state"`   // completed, failed, timed_out, cancelled
	Code    *string `json:"code"`    // failure code when state = failed
	Message string  `json:"message"`
}

// HarnessArtifact represents an artifact produced by a run.
type HarnessArtifact struct {
	Kind           string  `json:"kind"`           // comment, pull_request, branch, report, log
	BranchPurpose  *string `json:"branch_purpose"` // feature, debug (only for kind=branch)
	URI            string  `json:"uri"`
	Body           string  `json:"body,omitempty"` // only for kind=comment
}

// HealthResponse represents the /health endpoint response.
type HealthResponse struct {
	Reachable bool   `json:"reachable"`
	Model     string `json:"model"`
}

// HarnessState holds the state for the harness stub server.
type HarnessState struct {
	mu         sync.Mutex
	Runs       map[string]*HarnessRun
	Health     HealthResponse
	AttemptMap map[string]string // attempt_id -> run_id (for idempotency)
}

// NewHarnessState creates a new harness state.
func NewHarnessState(model string) *HarnessState {
	return &HarnessState{
		Runs:       make(map[string]*HarnessRun),
		AttemptMap: make(map[string]string),
		Health: HealthResponse{
			Reachable: true,
			Model:     model,
		},
	}
}

// SetHealth sets the health response.
func (s *HarnessState) SetHealth(reachable bool, model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Health.Reachable = reachable
	s.Health.Model = model
}

// CreateRun creates a new run or returns existing one for idempotency.
func (s *HarnessState) CreateRun(attemptID, role, prompt string, budget, timeoutSeconds int, repo RepositoryInfo, ticket *TicketInfo) (*HarnessRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check idempotency
	if existingRunID, ok := s.AttemptMap[attemptID]; ok {
		if run, ok := s.Runs[existingRunID]; ok {
			return run, false // duplicate
		}
	}

	runID := "run-" + uuid.New().String()[:8]
	now := time.Now().UTC()

	run := &HarnessRun{
		ID:             runID,
		AttemptID:      attemptID,
		Role:           role,
		Prompt:         prompt,
		Budget:         budget,
		TimeoutSeconds: timeoutSeconds,
		Repository:     repo,
		Ticket:         ticket,
		Status:         "running",
		StartedAt:      now,
		LastActivityAt: now,
		Logs:           []string{},
		LogOffset:      0,
	}
	s.Runs[runID] = run
	s.AttemptMap[attemptID] = runID
	return run, true // new
}

// GetRun returns a run by ID.
func (s *HarnessState) GetRun(runID string) (*HarnessRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.Runs[runID]
	return run, ok
}

// CompleteRun marks a run as terminal with the given outcome and artifacts.
func (s *HarnessState) CompleteRun(runID string, outcome *HarnessOutcome, artifacts []HarnessArtifact) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.Runs[runID]
	if !ok {
		return false
	}
	run.Status = "terminal"
	run.Outcome = outcome
	run.Artifacts = artifacts
	run.LastActivityAt = time.Now().UTC()
	return true
}

// AbortRun aborts a running run.
func (s *HarnessState) AbortRun(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.Runs[runID]
	if !ok {
		return false
	}
	if run.Status == "terminal" {
		return true // already terminal, idempotent
	}
	run.Cancelled = true
	run.Status = "terminal"
	run.Outcome = &HarnessOutcome{
		State:   "cancelled",
		Code:    nil,
		Message: "aborted by client",
	}
	run.LastActivityAt = time.Now().UTC()
	return true
}

// AppendLog appends a log entry to a run.
func (s *HarnessState) AppendLog(runID string, entry string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.Runs[runID]
	if !ok {
		return false
	}
	run.Logs = append(run.Logs, entry)
	run.LastActivityAt = time.Now().UTC()
	return true
}

// HarnessServer wraps an httptest.Server with its state.
type HarnessServer struct {
	*httptest.Server
	State *HarnessState
}

// NewHarnessServer creates a new harness stub server.
func NewHarnessServer(state *HarnessState) *HarnessServer {
	mux := http.NewServeMux()
	server := &HarnessServer{
		Server: httptest.NewServer(mux),
		State:  state,
	}

	mux.HandleFunc("/v1/runs", server.handleRuns)
	mux.HandleFunc("/v1/runs/", server.handleRunByID)
	mux.HandleFunc("/v1/health", server.handleHealth)

	return server
}

// URL returns the base URL of the server.
func (s *HarnessServer) URL() string {
	return s.Server.URL
}

func (s *HarnessServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.State.Health)
}

func (s *HarnessServer) handleRuns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleCreateRun(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HarnessServer) handleCreateRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		AttemptID      string        `json:"attempt_id"`
		Role           string        `json:"role"`
		Prompt         string        `json:"prompt"`
		Budget         int           `json:"budget"`
		TimeoutSeconds int           `json:"timeout_seconds"`
		Repository     RepositoryInfo `json:"repository"`
		Ticket         *TicketInfo   `json:"ticket"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.AttemptID == "" {
		http.Error(w, "attempt_id required", http.StatusBadRequest)
		return
	}

	run, isNew := s.State.CreateRun(
		req.AttemptID,
		req.Role,
		req.Prompt,
		req.Budget,
		req.TimeoutSeconds,
		req.Repository,
		req.Ticket,
	)

	w.Header().Set("Content-Type", "application/json")
	if isNew {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{
			"run_id": run.ID,
			"status": run.Status,
		})
	} else {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"run_id": run.ID,
			"status": run.Status,
		})
	}
}

func (s *HarnessServer) handleRunByID(w http.ResponseWriter, r *http.Request) {
	// Path: /v1/runs/{id}
	path := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	if path == "" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	runID := path
	run, ok := s.State.GetRun(runID)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleGetRun(w, r, run)
	case http.MethodDelete:
		s.handleDeleteRun(w, runID)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *HarnessServer) handleGetRun(w http.ResponseWriter, r *http.Request, run *HarnessRun) {
	w.Header().Set("Content-Type", "application/json")

	if run.Status == "running" {
		// Return running status with logs
		logOffset := 0
		if v := w.Header().Get("X-Log-Offset"); v != "" {
			fmt.Sscanf(v, "%d", &logOffset)
		}
		// Also check query param
		if v := r.URL.Query().Get("log_offset"); v != "" {
			fmt.Sscanf(v, "%d", &logOffset)
		}

		nextOffset := logOffset
		var logs []string
		if logOffset < len(run.Logs) {
			logs = run.Logs[logOffset:]
			nextOffset = len(run.Logs)
		}

		json.NewEncoder(w).Encode(map[string]any{
			"status":            "running",
			"last_activity_at":  run.LastActivityAt.Format(time.RFC3339Nano),
			"next_log_offset":   nextOffset,
			"logs":              logs,
		})
	} else {
		// Terminal
		resp := map[string]any{
			"status": "terminal",
			"outcome": map[string]any{
				"state":   run.Outcome.State,
				"code":    run.Outcome.Code,
				"message": run.Outcome.Message,
			},
			"artifacts": run.Artifacts,
		}
		json.NewEncoder(w).Encode(resp)
	}
}

func (s *HarnessServer) handleDeleteRun(w http.ResponseWriter, runID string) {
	ok := s.State.AbortRun(runID)
	if !ok {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}