package idle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// HarnessIdlePolicy implements IdlePolicy by probing the remote execution
// service's GET /health (D31, D49). It is the sole consumer of the /health
// response, so it owns the decode of both fields the producer emits.
//
// idle = reachable AND not busy (D49). Reachability is checked first: an
// unreachable model is not idle regardless of busy. The busy field is a
// courtesy signal (the remote is occupied by a run it is executing) and is
// gated on YieldWhenBusy: when false ( busy answer is ignored, preserving the
// reachable-only behaviour of D31 for every existing deployment; when true a
// busy answer makes the probe report not-idle, so the worker re-probes instead
// of starting a run that would compete for the same accelerator.
type HarnessIdlePolicy struct {
	baseURL       string
	token         string
	yieldWhenBusy bool
}

// NewHarnessIdlePolicy creates a new HarnessIdlePolicy. baseURL and token are
// the remote execution service's base URL and bearer credential (D27); the
// idle probe touches nothing else. yieldWhenBusy is the D49 capacity gate —
// pass config.YieldWhenBusy (IDLE_DECK_YIELD_WHEN_BUSY, default false).
func NewHarnessIdlePolicy(baseURL, token string, yieldWhenBusy bool) *HarnessIdlePolicy {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &HarnessIdlePolicy{
		baseURL:       baseURL,
		token:         token,
		yieldWhenBusy: yieldWhenBusy,
	}
}

func (p *HarnessIdlePolicy) Idle(ctx context.Context) (bool, error) {
	url := p.baseURL + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false, fmt.Errorf("idle probe: create request: %w", err)
	}
	if p.token != "" {
		req.Header.Set("Authorization", "Bearer "+p.token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false, fmt.Errorf("idle probe: request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("idle probe: unexpected status %d", resp.StatusCode)
	}

	// The producer emits reachable, busy, and model (remote-contract §2,
	// D49). `busy` is optional for backward compatibility: a producer that
	// predates D49 omits it, and missing must mean not-busy, never an error.
	var payload struct {
		Reachable *bool `json:"reachable"`
		Busy      *bool `json:"busy"`
	}
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&payload); err != nil {
		return false, fmt.Errorf("idle probe: decode body: %w", err)
	}

	if payload.Reachable == nil {
		return false, fmt.Errorf("idle probe: missing reachable field")
	}
	if !*payload.Reachable {
		return false, nil
	}

	// Reachable. Now the capacity gate: busy only matters when the operator
	// opted in, and only when the producer actually reported it.
	if p.yieldWhenBusy && payload.Busy != nil && *payload.Busy {
		return false, nil
	}
	return true, nil
}