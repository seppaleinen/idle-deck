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

// RemoteHarness implements Harness by talking to a remote JSON/HTTP execution service.
// The base URL is configured via the IDLE_DECK_HARNESS_URL environment variable.
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

// Start begins execution of a run and returns a session id usable for abort (I8).
// POST /v1/runs with JSON body. Idempotent by attempt_id: 201 first time, 200 same run_id on duplicate.
func (h *RemoteHarness) Start(ctx context.Context, req RunRequest) (RunID, error) {
	url := h.baseURL + "/v1/runs"
	body, err := json.Marshal(req)
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
		return "", fmt.Errorf("do start request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusCreated {
		var runResp runResponse
		if err := json.NewDecoder(resp.Body).Decode(&runResp); err != nil {
			return "", fmt.Errorf("decode start response: %w", err)
		}
		return RunID(runResp.RunID), nil
	}

	if resp.StatusCode == http.StatusOK {
		// Idempotent duplicate — server returns the existing run_id
		var runResp runResponse
		if err := json.NewDecoder(resp.Body).Decode(&runResp); err != nil {
			return "", fmt.Errorf("decode idempotent start response: %w", err)
		}
		return RunID(runResp.RunID), nil
	}

	var errBody errorBody
	if err := json.NewDecoder(resp.Body).Decode(&errBody); err == nil {
		return "", fmt.Errorf("start: %s (%d)", errBody.Message, resp.StatusCode)
	}

	return "", fmt.Errorf("start: unexpected status %d", resp.StatusCode)
}

// Abort cancels a running session. It returns only when the remote side confirms
// cancellation (or has already completed).
// DELETE /v1/runs/{id}, returns 204 only when confirmed dead (or already terminal).
func (h *RemoteHarness) Abort(ctx context.Context, id RunID) error {
	url := h.baseURL + "/v1/runs/" + string(id)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("create abort request: %w", err)
	}
	h.withAuth(httpReq)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("do abort request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNoContent {
		return nil
	}

	if resp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("abort: run %s not found", id)
	}

	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("abort: unexpected status %d body %s", resp.StatusCode, string(body))
}

// Result blocks until the run is terminal and returns its outcome and artifacts.
// Polls GET /v1/runs/{id} until terminal (status=terminal), no SSE in MVP.
func (h *RemoteHarness) Result(ctx context.Context, id RunID) (RunResult, error) {
	url := h.baseURL + "/v1/runs/" + string(id)

	var result RunResult

	// Polling loop — check every 5 seconds
	pollInterval := time.Second * 5
	stopPoll := time.After(24 * time.Hour)

	for {
		select {
		case <-stopPoll:
			return result, fmt.Errorf("result: polling stopped after 24h")
		case <-time.After(pollInterval):
		}

		httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return result, fmt.Errorf("create result request: %w", err)
		}
		h.withAuth(httpReq)

		resp, err := http.DefaultClient.Do(httpReq)
		if err != nil {
			// Network error — retryable, continue polling
			continue
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == http.StatusOK {
			var runResp runResponse
			if err := json.NewDecoder(resp.Body).Decode(&runResp); err != nil {
				continue // decode error — retry
			}

			if runResp.Status == "terminal" {
				outcome := AttemptOutcome(mapWireOutcome(runResp.Outcome.State, runResp.Outcome.Code))
				artifacts := mapWireArtifacts(runResp)
				return RunResult{
					Outcome:   outcome,
					Artifacts: artifacts,
				}, nil
			}

			// Still running — continue polling
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			// Unknown run id — non-retryable per remote-contract §7
			continue // keep polling, or could return error
		}
	}
}

// mapWireArtifacts converts the wire run response into a slice of Artifact.
func mapWireArtifacts(runResp runResponse) []Artifact {
	// The artifacts field is populated from the terminal GET response.
	if len(runResp.Artifacts) == 0 {
		return nil
	}
	return runResp.Artifacts
}

// Health checks the remote service model reachability per D31.
// GET /v1/health returns reachability, not process liveness.
func (h *RemoteHarness) Health(ctx context.Context) (bool, string, error) {
	url := h.baseURL + "/v1/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, "", fmt.Errorf("create health request: %w", err)
	}
	h.withAuth(req)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Network error — service unreachable
		return false, "", fmt.Errorf("health: network error: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, "", fmt.Errorf("health: read body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("health: unexpected status %d", resp.StatusCode)
	}

	var healthResp HealthResponse
	if err := json.Unmarshal(body, &healthResp); err != nil {
		return false, "", fmt.Errorf("health: decode response: %w", err)
	}

	// Per D31: "if it answers, the engine is considered idle"
	// "Must reflect model reachability, not merely process liveness"
	// If the endpoint answers with reachable=true, the service is reachable.
	// The Model field identifies which model is available.
	return healthResp.Reachable, healthResp.Model, nil
}
