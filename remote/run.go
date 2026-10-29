package remote

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// RunStatus is the run state machine.
type RunStatus string

const (
	StatusRunning  RunStatus = "running"
	StatusTerminal RunStatus = "terminal"
)

// Run is a server-side run record. It owns the runner session and the
// workspace lifecycle. One run = one agent execution.
type Run struct {
	mu               sync.Mutex
	ID               string
	AttemptID        string
	Status           RunStatus
	StartedAt        time.Time
	Deadline         time.Time // server-enforced timeout (D17)
	LastActivity     time.Time
	Runner           Runner
	SessionID        SessionID
	Workspace        Workspace
	Outcome          *RunResult
	Cancelled        bool
	done             chan struct{} // closed when the run reaches a terminal state
	ctx              context.Context
	cancel           context.CancelFunc
	WorkspaceManager *WorkspaceManager
}

// NewRun creates a run record and starts it via the runner.
// timeoutSeconds is server-enforced (D17): a dead idle-deck cannot leave a
// billed run orbiting. The watchdog goroutine kills the session at the
// deadline regardless of whether idle-deck is still polling.
func NewRun(attemptID string, runner Runner, ws Workspace, req StartRequest, timeoutSeconds int, wm *WorkspaceManager) (*Run, error) {
	ctx, cancel := context.WithCancel(context.Background())
	run := &Run{
		ID:               "run-" + uuid.New().String()[:12],
		AttemptID:        attemptID,
		Status:           StatusRunning,
		StartedAt:        time.Now().UTC(),
		Deadline:         time.Now().UTC().Add(time.Duration(timeoutSeconds) * time.Second),
		LastActivity:     time.Now().UTC(),
		Runner:           runner,
		Workspace:        ws,
		WorkspaceManager: wm,
		done:             make(chan struct{}),
		ctx:              ctx,
		cancel:           cancel,
	}

	// Start the agent in the workspace. This is synchronous: the runner
	// returns a session id once the agent is up.
	sessionID, err := runner.Start(run.context(), ws, req)
	if err != nil {
		cancel()
		return nil, err
	}
	run.SessionID = sessionID

	// The result is collected in the background so Start returns promptly.
	go run.collect()

	// The watchdog enforces the deadline (D17).
	go run.watchdog()

	return run, nil
}

// ctx returns the run's context, cancelled when the run is aborted or the
// server shuts down. The runner uses it for Start/Result/Abort.
func (r *Run) context() context.Context {
	return r.ctx
}

// collect blocks until the runner's Result is terminal and records it.
// If the run was cancelled (client DELETE or server timeout), Abort owns
// the outcome — collect must not clobber it with a retryable failure.
func (r *Run) collect() {
	result, err := r.Runner.Result(r.context(), r.SessionID)
	if r.wasCancelled() {
		return // Abort owns the outcome
	}
	// If the runner returned an error, treat as retryable failure and still
	// attempt workspace failure handling to preserve debug state.
	if err != nil {
		result = RunResult{Outcome: OutcomeRetryableFailure}
		if r.WorkspaceManager != nil && r.Workspace.Dir != "" {
			if art, werr := r.WorkspaceManager.OnFailure(r.Workspace, r.Workspace, r.AttemptID); werr == nil {
				result.Artifacts = append(result.Artifacts, art)
			}
			// If workspace handling fails, we keep the runner outcome and
			// surface the error via logs. Do not clobber a succeeded outcome.
		}
		r.Complete(result)
		return
	}

	// Runner succeeded with a terminal outcome. Wire workspace lifecycle:
	// - Success → OnSuccess (feature branch + Draft PR)
	// - Any failure (retryable/non-retryable/timeout) → OnFailure (debug branch)
	// D21: failed and timed-out runs preserve state to debug branch.
	if r.WorkspaceManager != nil && r.Workspace.Dir != "" {
		var art Artifact
		var werr error
		switch result.Outcome {
		case OutcomeSucceeded:
			art, werr = r.WorkspaceManager.OnSuccess(r.Workspace, r.Workspace, r.AttemptID)
		default:
			// Any non-success outcome is a failure path per D14/D21.
			art, werr = r.WorkspaceManager.OnFailure(r.Workspace, r.Workspace, r.AttemptID)
		}
		if werr == nil && art.Kind != "" {
			result.Artifacts = mergeArtifacts(result.Artifacts, art)
		}
		// If workspace handling errors, keep runner outcome and log.
		// Do not clobber a succeeded outcome with a workspace failure.
	}
	r.Complete(result)
}

// mergeArtifacts appends new artifact if not already present (dedupe by Kind+URI,
// matching acp.go addArtifact semantics).
func mergeArtifacts(existing []Artifact, a Artifact) []Artifact {
	if a.Kind == "" {
		return existing
	}
	for _, x := range existing {
		if x.Kind == a.Kind && x.URI == a.URI {
			return existing
		}
	}
	return append(existing, a)
}

// wasCancelled reports whether the run was aborted by a client or the
// watchdog before collect recorded anything.
func (r *Run) wasCancelled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Cancelled
}

// watchdog kills the session at the deadline (D17). This is the server's own
// enforcement — independent of idle-deck, so a dead client cannot strand a
// run. It also fires if the runner's Result never returns.
func (r *Run) watchdog() {
	deadline := time.Until(r.Deadline)
	if deadline <= 0 {
		r.killAndTimeout()
		return
	}
	select {
	case <-r.done:
		// Run reached a terminal state; nothing to enforce.
	case <-time.After(deadline):
		// Deadline exceeded — kill the agent and record timeout (D17).
		// We kill the runner directly (not via Abort) so the wire state is
		// timed_out, not cancelled. I7: preempted only crosses the wire when
		// the client asked for it; timeout is the server's own call.
		r.killAndTimeout()
	case <-r.ctx.Done():
		// Context cancelled (e.g. server shutdown).
	}
}

// killAndTimeout kills the runner session and records a timed_out outcome.
func (r *Run) killAndTimeout() {
	// Kill the agent process (no outcome assignment — that's Complete's job).
	if r.Runner != nil && r.SessionID != "" {
		_ = r.Runner.Abort(r.context(), r.SessionID)
	}
	r.Complete(RunResult{Outcome: OutcomeTimeout})
}

// Complete marks the run terminal with the given result.
func (r *Run) Complete(result RunResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.Status == StatusTerminal {
		return // idempotent
	}
	r.Status = StatusTerminal
	r.Outcome = &result
	r.LastActivity = time.Now().UTC()
	close(r.done)
}

// Abort signals the runner to cancel and waits for confirmation.
func (r *Run) Abort() error {
	r.mu.Lock()
	if r.Cancelled || r.Status == StatusTerminal {
		r.mu.Unlock()
		return nil // idempotent
	}
	r.Cancelled = true
	runner := r.Runner
	sessionID := r.SessionID
	r.mu.Unlock()

	// Cancel the runner's context so the agent sees the kill signal.
	r.cancel()

	if runner != nil && sessionID != "" {
		if err := runner.Abort(r.context(), sessionID); err != nil {
			return err
		}
	}

	r.mu.Lock()
	r.Status = StatusTerminal
	r.Outcome = &RunResult{
		Outcome: OutcomePreempted, // server says cancelled; worker maps to preempted (I7)
		Artifacts: []Artifact{{
			Kind:          "branch",
			BranchPurpose: stringPtr("debug"),
		}},
	}
	r.LastActivity = time.Now().UTC()
	r.mu.Unlock()
	return nil
}

// stringPtr returns a pointer to s.
func stringPtr(s string) *string { return &s }

// Response payloads matching the wire contract (remote-contract.md §2, §3, §4, §6).

type runResponse struct {
	Status    string           `json:"status"`
	RunID     string           `json:"run_id,omitempty"`
	Outcome   *outcomeResponse `json:"outcome,omitempty"`
	Artifacts []Artifact       `json:"artifacts,omitempty"`
	Usage     *Usage           `json:"usage,omitempty"` // token usage, optional
}

type outcomeResponse struct {
	State   string  `json:"state"` // completed | failed | timed_out | cancelled
	Code    *string `json:"code"`  // set when state = failed
	Message string  `json:"message"`
}

type healthResponse struct {
	Reachable bool   `json:"reachable"`
	Busy      bool   `json:"busy"` // D49: the remote is occupied by a run it is executing
	Model     string `json:"model"`
}

type errorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// toWireOutcome maps our internal AttemptOutcome to the wire state string.
func toWireOutcome(o AttemptOutcome) (string, *string) {
	switch o {
	case OutcomeSucceeded:
		return "completed", nil
	case OutcomeRetryableFailure:
		return "failed", stringPtr("retryable")
	case OutcomeNonRetryableFailure:
		return "failed", stringPtr("non_retryable")
	case OutcomeTimeout:
		return "timed_out", nil
	case OutcomePreempted:
		return "cancelled", nil
	}
	return "failed", stringPtr("unknown")
}

// toWireResponse builds the JSON response for GET /v1/runs/{id}.
func (r *Run) toWireResponse() runResponse {
	r.mu.Lock()
	defer r.mu.Unlock()

	resp := runResponse{
		Status: string(r.Status),
		RunID:  r.ID,
	}

	if r.Outcome != nil {
		state, code := toWireOutcome(r.Outcome.Outcome)
		resp.Outcome = &outcomeResponse{
			State:   state,
			Code:    code,
			Message: "done",
		}
		resp.Artifacts = r.Outcome.Artifacts
		resp.Usage = r.Outcome.Usage
	}
	return resp
}

// marshalJSON is a helper for handlers.
func marshalJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// errorJSON writes an error response.
func errorJSON(w http.ResponseWriter, status int, code, msg string) {
	marshalJSON(w, status, errorResponse{Code: code, Message: msg})
}

var _ = time.Time{} // ensure time is imported (context is used in Runner interface)
