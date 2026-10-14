package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// RemoteHarness implements Harness by talking to a remote JSON/HTTP execution
// service. The base URL is configured via the IDLE_DECK_HARNESS_URL
// environment variable.
//
// §7 error taxonomy is wired into the transport: every non-success response
// from Start or Result is classified to an AttemptOutcome via
// HTTPErrorOutcome and returned as a *HarnessError, so the worker's retry
// policy (D14) sees the real distinction between retryable and non-retryable.
// Before this, Start returned a plain error string (the worker hardcoded
// OutcomeRetryableFailure on Start failure) and Result looped forever on any
// non-200 — a 429, 401, 403, 404, and 422 all collapsed to "retryable".
type RemoteHarness struct {
	baseURL string
	token   string
}

// NewRemoteHarness creates a new RemoteHarness.
func NewRemoteHarness(baseURL, token string) *RemoteHarness {
	return &RemoteHarness{
		baseURL: baseURL,
		token:   token,
	}
}

// withAuth adds the Bearer auth header to a request.
func (h *RemoteHarness) withAuth(req *http.Request) {
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
}

// wireStartRequest is the §3 wire shape of POST /v1/runs. RunRequest is the
// adapter's domain type (role, never a model — D6); this is the translation to
// the contract's field names, including timeout_seconds as whole seconds rather
// than Go's nanosecond Duration encoding.
type wireStartRequest struct {
	AttemptID      string      `json:"attempt_id"`
	Role           string      `json:"role"`
	Prompt         string      `json:"prompt"`
	Budget         int         `json:"budget"`
	TimeoutSeconds int         `json:"timeout_seconds"`
	Repository     wireRepo    `json:"repository"`
	Ticket         *wireTicket `json:"ticket"`
}

type wireRepo struct {
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

// toWire converts a RunRequest into the §3 wire shape. Timeout is expressed in
// whole seconds (the server enforces it, D17); a zero duration stays zero and
// the server applies the tier default.
func (req RunRequest) toWire() wireStartRequest {
	w := wireStartRequest{
		AttemptID:  req.AttemptID,
		Role:       req.Role,
		Prompt:     req.Prompt,
		Budget:     req.Budget,
		Repository: wireRepo{ID: req.RepoRef.ID, Name: req.RepoRef.Name, URL: req.RepoRef.URL, Tracker: req.RepoRef.Tracker},
	}
	if req.Timeout > 0 {
		w.TimeoutSeconds = int(req.Timeout / time.Second)
	}
	if req.TicketRef.Tracker != "" || req.TicketRef.ExternalID != "" || req.TicketRef.URL != "" {
		w.Ticket = &wireTicket{
			Tracker:    req.TicketRef.Tracker,
			ExternalID: req.TicketRef.ExternalID,
			URL:        req.TicketRef.URL,
		}
	}
	return w
}

// Start begins execution of a run and returns a session id usable for abort (I8).
// POST /v1/runs with JSON body. Idempotent by attempt_id: 201 first time, 200 same run_id on duplicate.
//
// Non-2xx responses (429, 401/403/404, 422, 5xx) and network errors are
// classified per remote-contract §7 and returned as a *HarnessError carrying
// the outcome, so the caller can errors.As and apply the real retry policy.
func (h *RemoteHarness) Start(ctx context.Context, req RunRequest) (RunID, error) {
	url := h.baseURL + "/v1/runs"
	body, err := json.Marshal(req.toWire())
	if err != nil {
		return "", fmt.Errorf("marshal start request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return "", fmt.Errorf("create start request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	h.withAuth(httpReq)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		// Network error / server unreachable → retryable_failure (§7).
		if ctx.Err() != nil {
			return "", ctx.Err() // caller cancelled — not a server condition
		}
		return "", newHarnessError(0, []byte(err.Error()))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read start response: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK:
		// 201 = created; 200 = idempotent duplicate (same run_id, no new run — §3).
		var runResp runResponse
		if err := json.Unmarshal(raw, &runResp); err != nil {
			return "", fmt.Errorf("decode start response: %w", err)
		}
		return RunID(runResp.RunID), nil
	default:
		return "", newHarnessError(resp.StatusCode, raw)
	}
}

// Abort cancels a running session. It returns only when the remote side
// confirms cancellation (or has already completed).
// DELETE /v1/runs/{id}, returns 204 only when confirmed dead (or already terminal).
//
// Per §7, a 404 here means the remote has lost the run record (unknown run
// id) — non_retryable. Transient 5xx/429 are retryable. The worker's
// preemption path ignores the error (the run is going to be re-queued either
// way, D16/I7), so classification only matters for diagnostics and the
// conformance suite.
func (h *RemoteHarness) Abort(ctx context.Context, id RunID) error {
	url := h.baseURL + "/v1/runs/" + string(id)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create abort request: %w", err)
	}
	h.withAuth(httpReq)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return newHarnessError(0, []byte(err.Error()))
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil // confirmed dead (or already terminal)
	}

	raw, _ := io.ReadAll(resp.Body)
	return newHarnessError(resp.StatusCode, raw)
}

// Result blocks until the run is terminal and returns its outcome and artifacts.
// Polls GET /v1/runs/{id} until terminal (status=terminal), no SSE in MVP.
//
// Polling contract: the adapter polls internally with a hard 24h safety cap
// (the server enforces timeout_seconds per §3). On retryable HTTP errors
// (429, 5xx) the adapter returns immediately with a typed error so the
// worker's own backoff governs retries — the adapter does not loop on
// error responses. Only transient network errors are retried inside Result.
// §7 is applied at the transport: a 429 or 5xx mid-poll maps to
// retryable_failure (returned immediately), and a 404 maps to
// non_retryable_failure (unknown run id implies lost state; escalating
// beats looping). Only transient network errors continue polling with backoff.
func (h *RemoteHarness) Result(ctx context.Context, id RunID) (RunResult, error) {
	url := h.baseURL + "/v1/runs/" + string(id)

	var result RunResult

	pollInterval := time.Second * 5
	stopPoll := time.After(24 * time.Hour)

	for {
		select {
		case <-stopPoll:
			return result, fmt.Errorf("result: polling stopped after 24h")
		case <-ctx.Done():
			// The worker aborted us (preemption) or is shutting down. The
			// worker already knows why and records the outcome itself.
			return result, ctx.Err()
		case <-time.After(pollInterval):
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return result, fmt.Errorf("create result request: %w", err)
		}
		h.withAuth(httpReq)

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			if ctx.Err() != nil {
				return result, ctx.Err()
			}
			// Transient network error — keep polling; the server may come back.
			continue
		}

		raw, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			continue // body undeliverable — retry
		}

		switch resp.StatusCode {
		case http.StatusOK:
			var runResp runResponse
			if err := json.Unmarshal(raw, &runResp); err != nil {
				continue // decode error — retry
			}

			if runResp.Status == "terminal" {
				outcome := AttemptOutcome(mapWireOutcome(runResp.Outcome.State, runResp.Outcome.Code))
				artifacts := runResp.Artifacts
				if len(artifacts) == 0 {
					artifacts = nil
				}
				return RunResult{
					Outcome:   outcome,
					Artifacts: artifacts,
				}, nil
			}

			// Still running — continue polling
			continue

		case http.StatusNotFound:
			// Unknown run id — non_retryable_failure per §7. Escalating
			// beats looping: lost state means the run is gone.
			return result, newHarnessError(http.StatusNotFound, raw)

		default:
			// 429, 5xx, 401/403/404, 422 — map per §7 and stop polling.
			// The worker Nacks with the outcome and the retry policy decides
			// (retry the task at backoff, or escalate immediately).
			return result, newHarnessError(resp.StatusCode, raw)
		}
	}
}

// Health was the harness package's own /health implementation. It is removed:
// D31 defines exactly one idle probe, the remote server's GET /health
// (remote/server.go handleHealth, produced by cmd/remote/health.go), and the
// sole consumer is idle.HarnessIdlePolicy (idle/idle.go). This method was dead
// code — never wired into the worker, never called by any test — and it
// duplicated a third response shape, so keeping it invited drift on the
// contract D31 pins. (Architecture audit H6, M11.)
