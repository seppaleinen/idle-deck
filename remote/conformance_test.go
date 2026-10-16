package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// mustJSON marshals v to a JSON string, panicking on error.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// driveHTTP runs the server's handler against the given request.
func driveHTTP(t *testing.T, srv *Server, method, path string, body string) (*http.Response, []byte) {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = bytes.NewBufferString(body)
	}
	req := httptest.NewRequest(method, path, r)
	rec := httptest.NewRecorder()
	srv.Routes().ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec.Result(), b
}

// unmarshalJSON is a shortcut for json.Unmarshal.
func unmarshalJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}

// startRun posts a run and returns the status code + run_id.
func startRun(t *testing.T, srv *Server, attemptID, role, prompt string, budget, timeout int) (int, string) {
	t.Helper()
	body := mustJSON(startRequest{
		AttemptID:      attemptID,
		Role:           role,
		Prompt:         prompt,
		Budget:         budget,
		TimeoutSeconds: timeout,
		Repository:     wireRepository{ID: "repo-1", Name: "conformance/upstream", URL: srv.Config.TestUpstreamURL, Tracker: "github"},
		Ticket:         &wireTicket{Tracker: "github", ExternalID: "42", URL: "https://example.com/issue/42"},
	})
	resp, raw := driveHTTP(t, srv, http.MethodPost, "/v1/runs", body)
	var out struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := unmarshalJSON(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, out.RunID
}

// getRun performs GET /v1/runs/{id} and returns the status code + runResponse.
func getRun(t *testing.T, srv *Server, id string) (int, runResponse) {
	t.Helper()
	resp, raw := driveHTTP(t, srv, http.MethodGet, "/v1/runs/"+id, "")
	var out runResponse
	if err := unmarshalJSON(raw, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.StatusCode, out
}

// deleteRun performs DELETE /v1/runs/{id} and returns the status code.
func deleteRun(t *testing.T, srv *Server, id string) int {
	t.Helper()
	resp, _ := driveHTTP(t, srv, http.MethodDelete, "/v1/runs/"+id, "")
	return resp.StatusCode
}

// TestConformanceC1_IdempotentStart tests that Start with the same attempt_id
// returns the same run_id and no second execution occurs.
func TestConformanceC1_IdempotentStart(t *testing.T) {
	srv, runner := newTestServer(t)

	code1, id1 := startRun(t, srv, "a1", "do", "p", 1000, 60)
	if code1 != http.StatusCreated {
		t.Fatalf("first: code=%d, want 201", code1)
	}
	if id1 == "" {
		t.Fatal("first: empty run_id")
	}

	// duplicate attempt_id -> 200, same run_id
	code2, id2 := startRun(t, srv, "a1", "do", "p", 1000, 60)
	if code2 != http.StatusOK {
		t.Fatalf("dup: code=%d, want 200", code2)
	}
	if id2 != id1 {
		t.Fatalf("dup: run_id %q != %q", id2, id1)
	}

	// no second execution
	runner.mu.Lock()
	started := runner.started
	runner.mu.Unlock()
	if started != 1 {
		t.Fatalf("started=%d, want 1 (idempotency)", started)
	}
}

// TestConformanceC2_AbortRunningRun tests that Abort a running run returns
// only after the remote side confirms cancellation (or it was already terminal).
func TestConformanceC2_AbortRunningRun(t *testing.T) {
	srv, _ := newTestServer(t)

	code, id := startRun(t, srv, "a2", "do", "p", 1000, 60)
	if code != http.StatusCreated {
		t.Fatalf("start: code=%d", code)
	}

	// DELETE running -> 204
	if d := deleteRun(t, srv, id); d != http.StatusNoContent {
		t.Fatalf("abort: code=%d, want 204", d)
	}

	// terminal, cancelled
	_, out := getRun(t, srv, id)
	if out.Outcome == nil {
		t.Fatal("no outcome after abort")
	}
	if out.Outcome.State != "cancelled" {
		t.Fatalf("state=%q, want cancelled", out.Outcome.State)
	}

	// DELETE again -> 204 (idempotent)
	if d := deleteRun(t, srv, id); d != http.StatusNoContent {
		t.Fatalf("re-abort: code=%d, want 204", d)
	}
}

// TestConformanceC3_TimeoutServerKill tests that when a run exceeds timeout_seconds,
// the server kills the run and returns a timeout outcome.
func TestConformanceC3_TimeoutServerKill(t *testing.T) {
	srv, _ := newTestServer(t)

	code, id := startRun(t, srv, "a3", "do", "p", 1000, 1) // 1s timeout
	if code != http.StatusCreated {
		t.Fatalf("start: code=%d", code)
	}

	// wait for the watchdog to fire
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		_, out := getRun(t, srv, id)
		if out.Outcome != nil && out.Outcome.State == "timed_out" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	_, out := getRun(t, srv, id)
	if out.Outcome == nil || out.Outcome.State != "timed_out" {
		t.Fatalf("state=%v, want timed_out", out.Outcome)
	}
}

// TestConformanceC4_TerminalResultArtifacts tests that a terminal result has
// the correct artifacts.
func TestConformanceC4_TerminalResultArtifacts(t *testing.T) {
	srv, runner := newTestServer(t)

	code, id := startRun(t, srv, "a4", "do", "p", 1000, 60)
	if code != http.StatusCreated {
		t.Fatalf("start: code=%d", code)
	}

	// the stub runner auto-completes with a pull_request artifact;
	// force the session terminal to unblock collect.
	runner.mu.Lock()
	for sid, ch := range runner.running {
		runner.results[sid] = RunResult{
			Outcome: OutcomeSucceeded,
			Artifacts: []Artifact{
				{Kind: "pull_request", URI: "https://github.com/acme/widgets/pull/9"},
			},
		}
		close(ch)
	}
	runner.mu.Unlock()

	time.Sleep(100 * time.Millisecond)
	_, out := getRun(t, srv, id)
	if out.Outcome == nil || out.Outcome.State != "completed" {
		t.Fatalf("state=%v, want completed", out.Outcome)
	}
	if len(out.Artifacts) != 1 || out.Artifacts[0].Kind != "pull_request" {
		t.Fatalf("artifacts=%v, want one pull_request", out.Artifacts)
	}
}

// TestConformanceC5_429RetryAfter, C6_401403404, C7_422BudgetExceeded,
// C8_UnreachableService, and C9_ClientAbortThenReadResult live in
// harness/conformance_test.go — they are black-box tests of the adapter
// (RemoteHarness) against a controllable stub server, which is what
// §9a's "client-observable" wording requires. The server-level C1–C4
// below test the real remote.Server with a stub runner, a complementary
// layer.

// TestHealthModelReachable tests that /health reports reachable when the model is reachable.
func TestHealthModelReachable(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, raw := driveHTTP(t, srv, http.MethodGet, "/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health: code=%d", resp.StatusCode)
	}
	var h healthResponse
	if err := unmarshalJSON(raw, &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !h.Reachable || h.Model != "stub-model" {
		t.Fatalf("health=%+v", h)
	}
}

// TestHealthModelDown tests that /health reports unreachable when the model is down
// but the process is alive (D31).
func TestHealthModelDown(t *testing.T) {
	// model down: wrapper alive but reachable=false (D31)
	srv := NewServer(newStubRunner(), func() (bool, string) { return false, "" }, "", t.TempDir(), t.TempDir(), "")
	resp, raw := driveHTTP(t, srv, http.MethodGet, "/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health down: code=%d, want 200 (alive, not idle)", resp.StatusCode)
	}
	var h healthResponse
	if err := unmarshalJSON(raw, &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if h.Reachable {
		t.Fatalf("want reachable=false when model is down")
	}
}

// TestHealthBusyIsClientObservable (D49 / conformance case C10) pins the `busy`
// field on /health — a client-observable property of the wire contract, so a
// harness that drops it, renames it, or emits a wrong value fails the suite
// rather than silently passing. The remote reports busy from its own run
// state: a run still executing means the remote is occupied.
//
// This is the case that makes the D49 field load-bearing. It is written
// against the real remote.Server (not a stub) so it exercises hasRunningRun
// on the server's own Runs map.
func TestHealthBusyIsClientObservable(t *testing.T) {
	// Idle remote: no runs → busy=false, and the field is present.
	srv, _ := newTestServer(t)
	resp, raw := driveHTTP(t, srv, http.MethodGet, "/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("idle health: code=%d", resp.StatusCode)
	}
	var h healthResponse
	if err := unmarshalJSON(raw, &h); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !h.Reachable {
		t.Fatalf("idle health: want reachable=true, got false")
	}
	if h.Model != "stub-model" {
		t.Fatalf("idle health: model=%q, want stub-model", h.Model)
	}
	if h.Busy {
		t.Fatalf("idle health: want busy=false with no running run")
	}

	// Start a run and poll until the runner has it in flight, then re-probe:
	// the remote is now occupied, so busy must flip to true.
	code, id := startRun(t, srv, "c10", "do", "p", 1000, 60)
	if code != http.StatusCreated {
		t.Fatalf("start: code=%d, want 201", code)
	}
	// Wait until the runner has the run in flight (StatusRunning), so the
	// busy probe below is taken against an actually-occupied remote.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, out := getRun(t, srv, id); out.Status == string(StatusRunning) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var busyResp healthResponse
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, r := driveHTTP(t, srv, http.MethodGet, "/health", "")
		if err := unmarshalJSON(r, &busyResp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if busyResp.Busy {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !busyResp.Busy {
		t.Fatalf("busy health: a running run must make busy=true (D49), got %+v", busyResp)
	}
	if !busyResp.Reachable {
		t.Fatalf("busy health: model still reachable while a run executes")
	}
	t.Logf("C10: /health emits busy=true when a run is executing (D49)")
}

// newTestServer creates a test server with a stub runner.
func newTestServer(t *testing.T) (*Server, *stubRunner) {
	t.Helper()
	upstream := newUpstreamRepo(t)
	bareDir := t.TempDir()
	baseDir := t.TempDir()
	cfg := DefaultConfig(bareDir, baseDir, "tok-conformance")
	cfg.TestUpstreamURL = upstream
	srv := &Server{
		Runs:       make(map[string]*Run),
		AttemptMap: make(map[string]string),
		Runner:     newStubRunner(),
		Health:     func() (bool, string) { return true, "stub-model" },
		Config:     cfg,
		HTTPToken:  "",
	}
	t.Cleanup(func() { time.Sleep(50 * time.Millisecond) })
	return srv, srv.Runner.(*stubRunner)
}

// stubRunner implements Runner for testing the server without a real agent.
type stubRunner struct {
	mu      sync.Mutex
	started int
	aborted int
	results map[SessionID]RunResult
	running map[SessionID]chan struct{}
}

func newStubRunner() *stubRunner {
	return &stubRunner{
		results: make(map[SessionID]RunResult),
		running: make(map[SessionID]chan struct{}),
	}
}

func (s *stubRunner) Start(ctx context.Context, ws Workspace, req StartRequest) (SessionID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.started++
	id := SessionID("sess-" + itoa(s.started))
	s.running[id] = make(chan struct{})
	return id, nil
}

func (s *stubRunner) Abort(ctx context.Context, id SessionID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.aborted++
	if ch, ok := s.running[id]; ok {
		close(ch)
	}
	return nil
}

func (s *stubRunner) Result(ctx context.Context, id SessionID) (RunResult, error) {
	if ch, ok := s.running[id]; ok {
		select {
		case <-ctx.Done():
			return RunResult{}, ctx.Err()
		case <-ch:
		}
	}
	if r, ok := s.results[id]; ok {
		return r, nil
	}
	return RunResult{Outcome: OutcomeSucceeded, Artifacts: []Artifact{{Kind: "pull_request", URI: "https://example/pr/1"}}}, nil
}

func (s *stubRunner) setResult(id SessionID, r RunResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.results[id] = r
	if ch, ok := s.running[id]; ok {
		close(ch)
	}
}

func itoa(n int) string {
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if len(b) == 0 {
		return "0"
	}
	return string(b)
}

// context import used by stubRunner signature in server_smoke_test.go; keep linter happy.
var _ = context.Background
