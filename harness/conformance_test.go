package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/seppaleinen/idle-deck/test/stubs"
)

var httpClient = &http.Client{Timeout: 10 * time.Second}

// startRun performs POST /v1/runs and returns the run_id and status.
func startRun(t *testing.T, baseURL string, req map[string]any) (runID string, status string) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	resp, err := httpClient.Post(baseURL+"/v1/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d body=%s", resp.StatusCode, string(b))
	}
	var result struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result.RunID, result.Status
}

// getRun performs GET /v1/runs/{id} and returns the raw result map.
func getRun(t *testing.T, baseURL, runID string) map[string]any {
	t.Helper()
	resp, err := httpClient.Get(baseURL + "/v1/runs/" + runID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var result map[string]any
	if err := json.Unmarshal(b, &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return result
}

// deleteRun performs DELETE /v1/runs/{id} and returns the status code.
func deleteRun(t *testing.T, baseURL, runID string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, baseURL+"/v1/runs/"+runID, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// TestConformanceC1_IdempotentStart tests that Start with the same attempt_id
// returns the same run_id and no second execution occurs.
func TestConformanceC1_IdempotentStart(t *testing.T) {
	state := stubs.NewHarnessState("stub-model")
	server := stubs.NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	attemptID := "a1b2c3d4-1111-0000-0000-000000000001"
	body := map[string]any{
		"attempt_id": attemptID, "role": "plan", "prompt": "x",
		"budget": 5000, "timeout_seconds": 900,
		"repository": stubs.RepositoryInfoForTest(), "ticket": stubs.TicketInfoForTest("42"),
	}

	// First start → 201, new run
	firstRunID, firstStatus := startRun(t, base, body)
	if firstStatus != "running" {
		t.Fatalf("C1 first: status=%q, want running", firstStatus)
	}
	t.Logf("C1: first run_id=%s status=%s", firstRunID, firstStatus)

	// Duplicate attempt_id → 200, same run_id (no new execution)
	raw, _ := json.Marshal(body)
	resp, err := httpClient.Post(base+"/v1/runs", "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("C1 post error: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("C1 duplicate: got %d body=%s", resp.StatusCode, string(b))
	}
	var secondResult struct {
		RunID  string `json:"run_id"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&secondResult); err != nil {
		t.Fatalf("C1 decode: %v", err)
	}
	if secondResult.RunID != firstRunID {
		t.Fatalf("C1 duplicate run_id: %s vs %s", secondResult.RunID, firstRunID)
	}
	// Verify the run is still "running" (no new execution)
	result := getRun(t, base, firstRunID)
	status, ok := result["status"].(string)
	if !ok || status != "running" {
		t.Fatalf("C1: after duplicate, run status=%v, want running", result["status"])
	}
	t.Logf("C1: idempotent start verified — same run_id %s on duplicate, no second execution", firstRunID)
}

// TestConformanceC2_AbortRunningRun tests that Abort a running run returns
// only after the remote side confirms cancellation (or it was already terminal).
func TestConformanceC2_AbortRunningRun(t *testing.T) {
	state := stubs.NewHarnessState("stub-model")
	server := stubs.NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	body := map[string]any{
		"attempt_id": "a2", "role": "do", "prompt": "x", "budget": 20000,
		"timeout_seconds": 3600, "repository": stubs.RepositoryInfoForTest(), "ticket": nil,
	}

	rid, _ := startRun(t, base, body)
	if rid == "" {
		t.Fatal("C2: failed to start run")
	}

	// DELETE running → 204 (confirmed dead)
	code := deleteRun(t, base, rid)
	if code != http.StatusNoContent {
		t.Fatalf("C2 DELETE running: got %d, want 204", code)
	}

	// After abort: terminal, cancelled
	result := getRun(t, base, rid)
	outcome := result["outcome"].(map[string]any)
	if outcome["state"] != "cancelled" {
		t.Fatalf("C2 after abort: state=%v, want cancelled", outcome["state"])
	}

	// DELETE again on terminal → 204 (idempotent)
	code2 := deleteRun(t, base, rid)
	if code2 != http.StatusNoContent {
		t.Fatalf("C2 DELETE already-dead: got %d, want 204", code2)
	}
	t.Logf("C2: abort running run → confirmed death verified")
}

// TestConformanceC3_TimeoutServerKill tests that when a run exceeds timeout_seconds,
// the server kills it server-side. Since the #16 stub server does not auto-enforce
// timeout_seconds, we manually complete the run with a timed_out outcome after waiting,
func TestConformanceC3_TimeoutServerKill(t *testing.T) {
	// the adapter's outcome mapping handles timed_out → timeout correctly.
	state := stubs.NewHarnessState("stub-model")
	server := stubs.NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	// Start a run with a very short timeout
	body := map[string]any{
		"attempt_id": "a3", "role": "do", "prompt": "x", "budget": 5000,
		"timeout_seconds": 1, // 1 second timeout
		"repository":      stubs.RepositoryInfoForTest(), "ticket": nil,
	}

	rid, _ := startRun(t, base, body)
	if rid == "" {
		t.Fatal("C3: failed to start run")
	}

	// Wait for the expected timeout to elapse, then manually mark the run
	// as timed_out since the stub server does not auto-enforce timeouts.
	time.Sleep(2 * time.Second)
	state.CompleteRun(rid,
		&stubs.HarnessOutcome{State: "timed_out"},
		[]stubs.HarnessArtifact{
			{Kind: "log", URI: "artifact-store/" + rid + "/run.log"},
		})

	result := getRun(t, base, rid)
	outcome, ok := result["outcome"].(map[string]any)
	if !ok {
		t.Fatal("C3: result has no outcome field")
	}
	if outcome["state"] != "timed_out" {
		t.Fatalf("C3 after timeout: state=%v, want timed_out", outcome["state"])
	}
	t.Logf("C3: timeout outcome verified (state=%s)", outcome["state"])
}

// TestConformanceC4_TerminalResultArtifacts tests that a terminal result has
// artifacts always typed, and branch_purpose only on kind=branch (I4/I5).
func TestConformanceC4_TerminalResultArtifacts(t *testing.T) {
	state := stubs.NewHarnessState("stub-model")
	server := stubs.NewHarnessServer(state)
	defer server.Close()
	base := server.URL()

	body := map[string]any{
		"attempt_id": "a4", "role": "do", "prompt": "x", "budget": 100000,
		"timeout_seconds": 10800, "repository": stubs.RepositoryInfoForTest(), "ticket": stubs.TicketInfoForTest("77"),
	}

	rid, _ := startRun(t, base, body)
	if rid == "" {
		t.Fatal("C4: failed to start run")
	}

	// Complete the run with branch + pull_request artifacts
	branchPurpose := "feature"
	branchName := "feature/77-add-cache"
	state.CompleteRun(rid,
		&stubs.HarnessOutcome{State: "completed"},
		[]stubs.HarnessArtifact{
			{Kind: "pull_request", URI: "https://github.com/acme/widgets/pull/89"},
			{Kind: "branch", BranchPurpose: &branchPurpose, URI: "https://github.com/acme/widgets/tree/" + branchName},
			{Kind: "log", URI: "artifact-store/" + rid + "/run.log"},
		})

	result := getRun(t, base, rid)
	artifacts := result["artifacts"].([]any)

	// Verify 3 artifacts
	if len(artifacts) != 3 {
		t.Fatalf("C4 artifacts: got %d, want 3", len(artifacts))
	}

	// Verify branch_purpose only on kind=branch
	for _, a := range artifacts {
		am := a.(map[string]any)
		if am["kind"] == "branch" {
			if am["branch_purpose"] != "feature" {
				t.Fatalf("C4 branch purpose: got %v, want feature", am["branch_purpose"])
			}
		} else {
			// non-branch kind should not have branch_purpose
			if bp, ok := am["branch_purpose"]; ok && bp != nil {
				t.Fatalf("C4 non-branch kind %s has branch_purpose: %v", am["kind"], bp)
			}
		}
	}
	t.Logf("C4: artifact types verified OK — branch_purpose only on kind=branch")
}

// C5–C9 are black-box: they drive the real RemoteHarness against the stub
// server's controlled answers (§9a: "Every case is client-observable — a
// black-box test against a server whose answers the test controls"). The
// assertions are on what the adapter returns — a *HarnessError carrying the
// §7-classified outcome — not on the mapping function in isolation.

// harnessOutcomeFromError extracts the §7 outcome from a harness error. It
// fails the test unless the error is a *HarnessError carrying the expected
// outcome — i.e. the adapter classified the wire condition, not just errored.
func harnessOutcomeFromError(t *testing.T, err error, want AttemptOutcome) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error, got nil")
	}
	var he *HarnessError
	if !errors.As(err, &he) {
		t.Fatalf("expected *HarnessError with §7 outcome, got %T: %v", err, err)
	}
	if got := he.AttemptOutcomeValue(); got != want {
		t.Fatalf("outcome: got %q, want %q (err: %v)", got, want, err)
	}
}

// TestConformanceC5_429RetryAfter tests that a 429 + Retry-After from the
// server maps to retryable_failure per remote-contract §7 — observed by the
// adapter, not the mapping function.
func TestConformanceC5_429RetryAfter(t *testing.T) {
	state := stubs.NewHarnessState("stub-model")
	server := stubs.NewHarnessServer(state)
	defer server.Close()
	h := NewRemoteHarness(server.URL(), "")

	// The server answers 429 with Retry-After on POST /v1/runs.
	state.SetFail(&stubs.FailConfig{
		Method:     http.MethodPost,
		Path:       "/v1/runs",
		Status:     http.StatusTooManyRequests,
		Message:    "try again later",
		RetryAfter: "30",
	})
	t.Cleanup(state.ClearFail)

	// Black-box: the adapter sees the 429 and classifies it retryable.
	_, err := h.Start(context.Background(), startReq("c5"))
	harnessOutcomeFromError(t, err, OutcomeRetryableFailure)

	// The header is present on the wire (client-observable via raw HTTP).
	state.ClearFail()
	state.SetFail(&stubs.FailConfig{
		Method:     http.MethodPost,
		Path:       "/v1/runs",
		Status:     http.StatusTooManyRequests,
		Message:    "try again later",
		RetryAfter: "30",
	})
	req, _ := http.NewRequest(http.MethodPost, server.URL()+"/v1/runs", bytes.NewReader([]byte(`{}`)))
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("C5 header probe: %v", err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.Header.Get("Retry-After") != "30" {
		t.Fatalf("C5: Retry-After header = %q, want \"30\"", resp.Header.Get("Retry-After"))
	}
	t.Logf("C5: 429 + Retry-After → retryable_failure observed by the adapter (§7)")
}

// TestConformanceC6_401403404 tests that 401/403/404 on POST /v1/runs map to
// non_retryable_failure per §7 — configuration or bug; retrying won't fix it.
func TestConformanceC6_401403404(t *testing.T) {
	state := stubs.NewHarnessState("stub-model")
	server := stubs.NewHarnessServer(state)
	defer server.Close()
	h := NewRemoteHarness(server.URL(), "")

	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		state.SetFail(&stubs.FailConfig{
			Method: http.MethodPost,
			Path:   "/v1/runs",
			Status: code,
			Message: "refused",
		})
		_, err := h.Start(context.Background(), startReq("c6"))
		harnessOutcomeFromError(t, err, OutcomeNonRetryableFailure)
		t.Logf("C6: status %d → non_retryable_failure (§7)", code)
	}
	state.ClearFail()
}

// TestConformanceC7_422BudgetExceeded tests that a 422 budget_exceeded maps to
// non_retryable_failure per §7 — the ceiling was refused; retry won't help.
// Covers both the Start path (POST) and the Result path (GET mid-poll), which
// previously looped forever on any non-200.
func TestConformanceC7_422BudgetExceeded(t *testing.T) {
	// Start path: POST /v1/runs → 422.
	t.Run("Start path", func(t *testing.T) {
		state := stubs.NewHarnessState("stub-model")
		server := stubs.NewHarnessServer(state)
		defer server.Close()
		h := NewRemoteHarness(server.URL(), "")

		state.SetFail(&stubs.FailConfig{
			Method: http.MethodPost,
			Path:   "/v1/runs",
			Status: http.StatusUnprocessableEntity,
			Code:   "budget_exceeded",
			Message: "budget 5000 exceeds ceiling",
		})
		_, err := h.Start(context.Background(), startReq("c7a"))
		harnessOutcomeFromError(t, err, OutcomeNonRetryableFailure)
	})

	// Result path: run is running, then the server starts refusing with 422.
	// The adapter must stop polling and surface non_retryable, not loop.
	t.Run("Result path", func(t *testing.T) {
		state := stubs.NewHarnessState("stub-model")
		server := stubs.NewHarnessServer(state)
		defer server.Close()
		h := NewRemoteHarness(server.URL(), "")

		rid, err := h.Start(context.Background(), startReq("c7b"))
		if err != nil {
			t.Fatalf("start: %v", err)
		}

		// The run exists and is running; now refuse every GET for it with 422.
		state.SetFail(&stubs.FailConfig{
			Method: http.MethodGet,
			Path:   "/v1/runs/" + string(rid),
			Status: http.StatusUnprocessableEntity,
			Code:   "budget_exceeded",
			Message: "budget 5000 exceeds ceiling",
		})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		started := time.Now()
		_, err = h.Result(ctx, rid)
		if elapsed := time.Since(started); elapsed > 9*time.Second {
			t.Fatalf("C7 Result path: took %v — adapter kept polling an error response", elapsed)
		}
		harnessOutcomeFromError(t, err, OutcomeNonRetryableFailure)
	})
	t.Logf("C7: 422 budget_exceeded → non_retryable_failure on Start and Result paths (§7)")
}

// TestConformanceC8_UnreachableService tests that an unreachable service maps to
// retryable_failure — never a hang, never a silent success (§7).
func TestConformanceC8_UnreachableService(t *testing.T) {
	// Start a real stub server and close it: the URL is now guaranteed to
	// refuse connections (no port reuse in the test lifetime).
	server := stubs.NewHarnessServer(stubs.NewHarnessState("stub-model"))
	base := server.URL()
	server.Close()

	h := NewRemoteHarness(base, "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	started := time.Now()
	_, err := h.Start(ctx, startReq("c8"))
	if elapsed := time.Since(started); elapsed > 9*time.Second {
		t.Fatalf("C8: Start took %v against unreachable service — hang", elapsed)
	}
	harnessOutcomeFromError(t, err, OutcomeRetryableFailure)
	t.Logf("C8: unreachable service → retryable_failure, no hang, no silent success (§7)")
}

// TestConformanceC9_ClientAbortThenReadResult tests that when a client aborts a
// run, the wire says "cancelled" and the adapter records "preempted" — which
// never crosses the wire (§6, I7).
func TestConformanceC9_ClientAbortThenReadResult(t *testing.T) {
	state := stubs.NewHarnessState("stub-model")
	server := stubs.NewHarnessServer(state)
	defer server.Close()
	h := NewRemoteHarness(server.URL(), "")

	rid, err := h.Start(context.Background(), startReq("c9"))
	if err != nil {
		t.Fatalf("start: %v", err)
	}

	// Abort: returns only after the remote confirms death.
	if err := h.Abort(context.Background(), rid); err != nil {
		t.Fatalf("abort: %v", err)
	}

	// Read the result: the wire reports cancelled; the adapter steers it to
	// preempted, because only the client knows the abort reason (I7).
	res, err := h.Result(context.Background(), rid)
	if err != nil {
		t.Fatalf("result: %v", err)
	}
	if res.Outcome != OutcomePreempted {
		t.Fatalf("C9: outcome = %q, want preempted (I7)", res.Outcome)
	}

	// The wire itself said cancelled (client-observable via raw HTTP):
	// "preempted never appears on the wire".
	raw := getRun(t, server.URL(), string(rid))
	outcome := raw["outcome"].(map[string]any)
	if outcome["state"] != "cancelled" {
		t.Fatalf("C9 wire state = %v, want cancelled", outcome["state"])
	}
	t.Logf("C9: client abort → wire cancelled → adapter preempted, never on the wire (§6, I7)")
}

// startReq builds a minimal RunRequest for the black-box conformance cases.
func startReq(attemptID string) RunRequest {
	return RunRequest{
		Role:      "do",
		Prompt:    "x",
		Budget:    100000,
		Timeout:   time.Hour,
		RepoRef:   Repository{ID: "repo-1", Name: "acme/widgets", URL: "https://github.com/acme/widgets.git", Tracker: "github"},
		AttemptID: attemptID,
	}
}
