package main

// Real model-reachability probe for GET /health (D31, ADR 0016).
//
// The remote contract (docs/architecture/remote-contract.md §2) requires
// /health to report *model* reachability, not process liveness: a harness
// process that is alive while the model endpoint is down, rate limited, or
// out of credentials must report reachable=false, or idle-deck's IdlePolicy
// would certify the machine as free and start a run that fails immediately.
//
// The probe drives a real ACP round-trip through the configured agent CLI:
//
//	1. initialize   — the agent process speaks ACP and stays up.
//	2. session/new  — the agent creates a session for the probe.
//	3. session/prompt — forces an actual provider call with a trivial
//	                    no-tool prompt.
//
// Step 3 is load-bearing and cannot be skipped: session/new resolves the
// model name from *config* and succeeds even when the provider is rate
// limited or unreachable — the failure only surfaces when a prompt is
// generated (verified against opencode 1.18.x: initialize and session/new
// both succeed with a dead provider, and the provider error arrives as the
// JSON-RPC response to session/prompt). The contract (remote-contract.md
// §2, ADR 0016) is explicit about what that means: "A harness process that
// is alive while the model endpoint is down, rate limited, or out of
// credentials is **not idle**." So the probe reports reachable=true ONLY
// when session/prompt completes with a JSON-RPC *result* (the model actually
// generated). A JSON-RPC error (rate limit, quota, bad credentials,
// unknown model), a process death, or a timeout all report false.
//
// Latency budget: the round-trip takes a few seconds when the model is up
// and fails fast when the provider rejects the call. The whole probe is
// bounded by healthProbeBudget and idle-deck's waitIdle re-probes on a 1s
// cadence, so the result is cached for healthCacheTTL: reachability does
// not change on a sub-second scale, and the cache keeps a probe storm from
// spawning an agent process on every tick.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// healthProbeBudget bounds the whole round-trip (startup + prompt).
	// A reachable model that is generating a one-word answer answers well
	// under this; a provider that rejects the call answers in ~1-5s; a
	// genuinely unreachable model burns the budget and reports false.
	healthProbeBudget = 30 * time.Second

	// healthProbeStartup bounds the initialize + session/new handshake.
	// Covers a cold agent start (a fresh `opencode acp` process).
	healthProbeStartup = 10 * time.Second

	// healthCacheTTL: how long a probe result is trusted before a fresh
	// probe. The probe is expensive (it spawns the agent CLI and touches
	// the model provider), and idle-deck re-probes every second while
	// waiting for work — without the cache a model-outage would mean a
	// fresh agent process every second.
	healthCacheTTL = 15 * time.Second

	// probePrompt is the cheapest possible generation. It forbids tools so
	// the probe never executes anything in a workspace: reachability is
	// about the model answering, not about the agent doing work.
	probePrompt = "Reply with the single word OK. Do not use any tools."
)

// healthProbe is a model-reachability probe bound to one ACP launch recipe
// and the model name it reports (the configured role model, so the /health
// response stays informative — the response shape is
// {"reachable": bool, "model": string}).
type healthProbe struct {
	bin   string
	args  []string
	model string

	// Durable knobs, defaulted from the package constants. They are fields
	// (not constants at the call site) so tests can shrink the budget;
	// production always uses the defaults set in newHealthProbe.
	budget   time.Duration
	startup  time.Duration
	cacheTTL time.Duration

	// env is extra environment appended to the agent process's inherited
	// environment. The agent needs the operator's credentials (API keys,
	// provider config) to reach the model, so the parent env is always
	// inherited and env is layered on top.
	env []string

	mu        sync.Mutex
	cachedAt  time.Time
	reachable bool
	report    string // model name reported in the /health response
}

// newHealthProbe builds the probe. bin/args are the same launch recipe the
// runner uses (the {{DIR}} placeholder is replaced with a scratch dir — the
// probe needs no repository, only a directory the agent can treat as cwd).
// env is extra environment layered on top of the inherited environment; the
// operator's credentials (API keys, provider config) are inherited so the
// probe reaches the same model the runner would.
func newHealthProbe(bin string, args []string, model string, env []string) *healthProbe {
	probeArgs := make([]string, len(args))
	copy(probeArgs, args)
	for i, a := range probeArgs {
		if a == "{{DIR}}" {
			probeArgs[i] = os.TempDir()
		}
	}
	return &healthProbe{
		bin:      bin,
		args:     probeArgs,
		report:   model,
		budget:   healthProbeBudget,
		startup:  healthProbeStartup,
		cacheTTL: healthCacheTTL,
		env:      env,
	}
}

// Check implements the Server.Health hook: (reachable, model). It never
// panics and always returns within roughly the probe budget.
func (p *healthProbe) Check() (bool, string) {
	now := time.Now()
	p.mu.Lock()
	if now.Sub(p.cachedAt) < p.cacheTTL {
		r, m := p.reachable, p.report
		p.mu.Unlock()
		return r, m
	}
	p.mu.Unlock()

	reachable := p.probeModel()

	p.mu.Lock()
	p.reachable = reachable
	p.cachedAt = time.Now()
	r, m := p.reachable, p.report
	p.mu.Unlock()
	return r, m
}

// probeModel runs one ACP round-trip and reports whether the model was
// actually contacted. See the file header for why every step is required.
func (p *healthProbe) probeModel() bool {
	ctx, cancel := context.WithTimeout(context.Background(), p.budget)
	defer cancel()

	cmd := exec.CommandContext(ctx, p.bin, p.args...)
	cmd.Dir = os.TempDir()
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	// Stderr is discarded on purpose: the probe's verdict comes from the
	// ACP responses only, and a dead provider's stderr noise is neither.
	cmd.Stderr = io.Discard
	// The agent inherits the operator's environment (API keys, provider
	// config) so it reaches the same model the runner would; p.env layers
	// extra variables on top. cmd.Env must be set BEFORE Start: setting it
	// afterwards has no effect (the process is already forked).
	if len(p.env) > 0 {
		cmd.Env = append(os.Environ(), p.env...)
	}

	if err := cmd.Start(); err != nil {
		return false // agent CLI missing — nothing is reachable
	}
	// The probe must never leak an agent process: on any path (success,
	// timeout, process death) we kill and reap.
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	lines := make(chan string, 64)
	go func() {
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" {
				continue
			}
			lines <- line
		}
		close(lines)
	}()

	// 1. initialize — the agent speaks ACP.
	if _, err := p.rpc(ctx, stdin, lines, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo":      map[string]any{"name": "idle-deck-health", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	}, p.startup); err != nil {
		return false
	}

	// 2. session/new — the agent can create a session (cwd = scratch dir).
	res, err := p.rpc(ctx, stdin, lines, "session/new", map[string]any{
		"cwd":        os.TempDir(),
		"mcpServers": []any{},
	}, p.startup)
	if err != nil {
		return false
	}
	var sNew struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &sNew); err != nil || sNew.SessionID == "" {
		return false
	}

	// 3. session/prompt — the only step that touches the provider, and the
	// one that decides the answer. The contract (remote-contract.md §2,
	// ADR 0016) is explicit: "A harness process that is alive while the
	// model endpoint is down, rate limited, or out of credentials is **not
	// idle**." So only a JSON-RPC *result* (the model actually generated)
	// means reachable. A JSON-RPC error (rate limit, quota, missing
	// credentials, unknown model), a process death, or a timeout all mean
	// the model cannot serve generation right now → not idle.
	_, err = p.rpc(ctx, stdin, lines, "session/prompt", map[string]any{
		"sessionId": sNew.SessionID,
		"prompt": []any{
			map[string]any{"type": "text", "text": probePrompt},
		},
	}, p.budget-p.startup)
	return err == nil
}

var (
	rpcIDMu  sync.Mutex
	nextRPC  int
)

func nextRPCID() int {
	rpcIDMu.Lock()
	nextRPC++
	id := nextRPC
	rpcIDMu.Unlock()
	return id
}

// rpc sends one JSON-RPC 2.0 request and waits for the response carrying
// its id, skipping ACP notifications (which have no id). It returns the
// response's `result` field, or an error that is either a JSON-RPC `error`
// (formatted "rpc error <code>: <message>") or a transport failure
// (timeout / process death / EOF).
func (p *healthProbe) rpc(ctx context.Context, stdin io.WriteCloser, lines <-chan string, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	id := nextRPCID()
	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", method, err)
	}
	if _, err := stdin.Write(append(body, '\n')); err != nil {
		return nil, fmt.Errorf("write %s: %w", method, err)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				return nil, fmt.Errorf("%s: agent closed its output", method)
			}
			var env struct {
				ID    *int            `json:"id"`
				Error *jsonRPCError   `json:"error"`
				Res   json.RawMessage `json:"result"`
			}
			if err := json.Unmarshal([]byte(line), &env); err != nil {
				continue
			}
			if env.ID == nil || *env.ID != id {
				continue // notification or a response to another request
			}
			if env.Error != nil {
				return nil, fmt.Errorf("rpc error %d: %s", env.Error.Code, env.Error.Message)
			}
			return env.Res, nil
		case <-timer.C:
			return nil, fmt.Errorf("%s: no response within %s", method, timeout)
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// jsonRPCError is the JSON-RPC 2.0 error object.
type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
