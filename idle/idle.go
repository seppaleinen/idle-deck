package idle

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type IdlePolicy interface {
	Idle(ctx context.Context) (bool, error)
}

type HarnessIdlePolicy struct {
	baseURL string
	token   string
}

func NewHarnessIdlePolicy(baseURL, token string) *HarnessIdlePolicy {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return &HarnessIdlePolicy{
		baseURL: baseURL,
		token:   token,
	}
}

func (p *HarnessIdlePolicy) Idle(ctx context.Context) (bool, error) {
	url := p.baseURL + "/v1/health"
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

	var payload struct {
		Reachable *bool `json:"reachable"`
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
	return true, nil
}
