package remote

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/google/uuid"
)

// Server is the remote execution service. It exposes the locked wire contract
// (remote-contract.md §2) over HTTP:
//
//	POST   /v1/runs       Start        (idempotent by attempt_id)
//	GET    /v1/runs/{id}  Result       (status while running, terminal when done)
//	DELETE /v1/runs/{id}  Abort        (204 only when confirmed dead)
//	GET    /health        IdlePolicy   (model reachability, not liveness — D31)
//
// The server does not run the agent itself. It owns the run record, the
// idempotency map, and the timeout, and delegates execution to the Runner.
type Server struct {
	mu         sync.Mutex
	Runs       map[string]*Run
	AttemptMap map[string]string // attempt_id -> run_id (C1 idempotency)
	Runner     Runner
	// Health is the model-reachability probe (D31). The server never
	// implements a liveness ping; this hook is called on every GET /health
	// so the answer is always current.
	Health func() (reachable bool, model string)
	// Config holds the wrapper's configuration.
	Config *Config
	// HTTPToken is the bearer credential required on every HTTP request
	// (contract §1). Empty = no auth enforced (local dev).
	HTTPToken string
}

// NewServer builds a Server from a Runner, health probe, HTTP bearer token,
// and wrapper configuration.
func NewServer(runner Runner, health func() (bool, string), httpToken string, bareDir, baseDir, gitHubToken string) *Server {
	cfg := DefaultConfig(bareDir, baseDir, gitHubToken)
	return &Server{
		Runs:       make(map[string]*Run),
		AttemptMap: make(map[string]string),
		Runner:     runner,
		Health:     health,
		Config:     cfg,
		HTTPToken:  httpToken,
	}
}

// Routes returns an http.Handler wired to the four contract endpoints.
func (s *Server) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/runs", s.handleRuns)
	mux.HandleFunc("/v1/runs/", s.handleRunByID)
	mux.HandleFunc("/health", s.handleHealth)
	return s.auth(mux)
}

// auth wraps a handler with the bearer check. /health is exempt from auth
// (contract §1: the idle probe must work without the operator's full token
// in the same request path — in practice the same token is used, but the
// probe is called by the worker's IdlePolicy, not the adapter, so it can
// carry no credentials).
func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.HTTPToken == "" {
			next.ServeHTTP(w, r)
			return
		}
		if r.URL.Path != "/health" && !s.authorized(r) {
			errorJSON(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	return len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) && h[len(prefix):] == s.HTTPToken
}

// handleRuns implements POST /v1/runs — Start (idempotent by attempt_id).
func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		errorJSON(w, http.StatusMethodNotAllowed, "method_not_allowed", "POST only")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		errorJSON(w, http.StatusBadRequest, "bad_request", "unreadable body")
		return
	}
	var req startRequest
	if err := json.Unmarshal(body, &req); err != nil {
		errorJSON(w, http.StatusBadRequest, "bad_request", "invalid JSON")
		return
	}
	if req.AttemptID == "" {
		errorJSON(w, http.StatusBadRequest, "bad_request", "attempt_id required")
	}

	// Idempotency: if this attempt_id already has a run, return it with 200
	// instead of creating a duplicate (C1). The contract says Start is
	// idempotent by attempt_id, so a duplicate is a success, not an error.
	s.mu.Lock()
	if existing, ok := s.AttemptMap[req.AttemptID]; ok {
		run := s.Runs[existing]
		s.mu.Unlock()
		if run == nil {
			errorJSON(w, http.StatusInternalServerError, "internal_error", "attempt map references missing run")
			return
		}
		marshalJSON(w, http.StatusOK, runResponse{Status: string(run.Status), RunID: run.ID})
		return
	}
	s.mu.Unlock()

	// Prepare the workspace before starting the run.
	workspace, err := s.Config.WorkspaceManager.PrepareWorkspace(toWorkspace(req.Repository, req.Ticket), req.AttemptID)
	if err != nil {
		// If we can't prepare the workspace, we can't start the run.
		errorJSON(w, http.StatusBadGateway, "workspace_failed", err.Error())
		return
	}

	run, err := NewRun(req.AttemptID, s.Runner, workspace, req.toStartRequest(), req.TimeoutSeconds, s.Config.WorkspaceManager)
	if err != nil {
		// A run that could not start is not a wire error per se — the
		// contract does not define a 500 on Start beyond auth/validation.
		// The adapter maps any non-2xx to retryable_failure (mapping.go).
		errorJSON(w, http.StatusBadGateway, "start_failed", err.Error())
		return
	}

	s.mu.Lock()
	s.Runs[run.ID] = run
	s.AttemptMap[run.AttemptID] = run.ID
	s.mu.Unlock()

	marshalJSON(w, http.StatusCreated, runResponse{Status: string(StatusRunning), RunID: run.ID})
}

// handleRunByID implements GET /v1/runs/{id} (Result) and DELETE /v1/runs/{id} (Abort).
func (s *Server) handleRunByID(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	if id == "" {
		errorJSON(w, http.StatusNotFound, "not_found", "missing run id")
		return
	}

	s.mu.Lock()
	run, ok := s.Runs[id]
	s.mu.Unlock()
	if !ok {
		errorJSON(w, http.StatusNotFound, "not_found", "unknown run")
		return
	}

	switch r.Method {
	case http.MethodGet:
		resp := run.toWireResponse()
		marshalJSON(w, http.StatusOK, resp)
	case http.MethodDelete:
		// Abort: 204 only when confirmed dead (I8). NewRun's session was
		// already started; Abort waits for runner confirmation.
		if err := run.Abort(); err != nil {
			// Confirmed-dead is the only 204. An abort error means the
			// remote did not confirm — that is a retryable failure, not
			// a confirmed kill.
			errorJSON(w, http.StatusBadGateway, "abort_unconfirmed", err.Error())
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		errorJSON(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET or DELETE only")
	}
}

// handleHealth implements GET /health — model reachability AND capacity (D31, D49).
//
// `reachable` comes from the Health hook (a real ACP round-trip — see
// cmd/remote/health.go): it is false when the model cannot generate right now.
//
// `busy` is this server's own knowledge of its occupancy (D49): true when any
// run in this server is still executing. It is a courtesy signal — the remote
// cannot see model contention it did not schedule — and it is what lets the
// daemon yield instead of starting a run that would compete for the same
// accelerator. The producer emits it unconditionally; the consumer gates on it
// only behind IDLE_DECK_YIELD_WHEN_BUSY (default false).
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		errorJSON(w, http.StatusMethodNotAllowed, "method_not_allowed", "GET only")
		return
	}
	reachable, model := s.Health()
	marshalJSON(w, http.StatusOK, healthResponse{
		Reachable: reachable,
		Busy:      s.hasRunningRun(),
		Model:     model,
	})
}

// hasRunningRun reports whether any run in this server is still executing.
// It is the source of the /health `busy` field (D49). Checked under the
// server's lock so a run that transitions to terminal between the check and
// the response cannot be reported as busy.
func (s *Server) hasRunningRun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.Runs {
		if run.Status == StatusRunning {
			return true
		}
	}
	return false
}

// startRequest is the wire shape of POST /v1/runs (remote-contract.md §3).
type startRequest struct {
	AttemptID      string         `json:"attempt_id"`
	Role           string         `json:"role"`
	Prompt         string         `json:"prompt"`
	Budget         int            `json:"budget"`
	TimeoutSeconds int            `json:"timeout_seconds"`
	Repository     wireRepository `json:"repository"`
	Ticket         *wireTicket    `json:"ticket"`
}

type wireRepository struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	URL     string `json:"url"`
	Tracker string `json:"tracker"`
}

type wireTicket struct {
	Tracker    string `json:"tracker"`
	ExternalID string `json:"external_id"`
	URL        string `json:"url"`
}

func (r startRequest) toStartRequest() StartRequest {
	return StartRequest{
		Role:   r.Role,
		Prompt: r.Prompt,
		Budget: r.Budget,
	}
}

// toWorkspace converts the wire request into a Workspace. The ticket's
// external id is the ticket_id (D15: feature/<ticket_id>-<slug>); a nil
// ticket means the run has no ticket (P3 sweep, D44).
func toWorkspace(repo wireRepository, ticket *wireTicket) Workspace {
	ws := Workspace{
		RepoURL: repo.URL,
	}
	if ticket != nil {
		ws.TicketID = ticket.ExternalID
	}
	return ws
}

var _ = uuid.New // keep uuid import for NewRun in run.go (separate file)
