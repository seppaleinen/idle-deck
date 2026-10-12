package main

import (
	"os"
	"testing"
)

// TestAgentConfig defaults are opencode acp with no extra env when the
// operator does not set any IDLE_DECK_REMOTE_AGENT_* vars (D46).
func TestAgentConfigDefaults(t *testing.T) {
	for _, k := range []string{
		"IDLE_DECK_REMOTE_AGENT_BIN",
		"IDLE_DECK_REMOTE_AGENT_ARGS",
		"IDLE_DECK_REMOTE_AGENT_ENV",
	} {
		os.Unsetenv(k)
	}
	cfg := agentConfig()
	if cfg.Bin != "opencode" {
		t.Fatalf("bin=%q, want opencode", cfg.Bin)
	}
	if len(cfg.Args) != 1 || cfg.Args[0] != "acp" {
		t.Fatalf("args=%v, want [acp]", cfg.Args)
	}
	if cfg.Env != nil {
		t.Fatalf("env=%v, want nil", cfg.Env)
	}
}

// TestAgentConfigCustom verifies that the three env vars are parsed as JSON
// arrays and override the defaults.
func TestAgentConfigCustom(t *testing.T) {
	t.Setenv("IDLE_DECK_REMOTE_AGENT_BIN", "/usr/local/bin/myagent")
	t.Setenv("IDLE_DECK_REMOTE_AGENT_ARGS", `["--acp","--port","1234"]`)
	t.Setenv("IDLE_DECK_REMOTE_AGENT_ENV", `["FOO=bar","BAZ=qux"]`)

	cfg := agentConfig()
	if cfg.Bin != "/usr/local/bin/myagent" {
		t.Fatalf("bin=%q", cfg.Bin)
	}
	if len(cfg.Args) != 3 || cfg.Args[0] != "--acp" || cfg.Args[2] != "1234" {
		t.Fatalf("args=%v", cfg.Args)
	}
	if len(cfg.Env) != 2 || cfg.Env[0] != "FOO=bar" {
		t.Fatalf("env=%v", cfg.Env)
	}
}

// TestAgentConfigDrivenHealthProbe exercises the end-to-end config-driven
// path: the health probe must use the SAME agent CLI the runner uses
// (D46). We point the binary at the fake ACP agent (test/fakeacp) and
// propagate FAKE_ACP_MODE via IDLE_DECK_REMOTE_AGENT_ENV. When the mode is
// "ok", the probe reports reachable=true; with "error" it reports false
// (D31 — the probe must reflect model reachability, not liveness).
func TestAgentConfigDrivenHealthProbe(t *testing.T) {
	bin := buildFakeACP(t)

	t.Setenv("IDLE_DECK_REMOTE_AGENT_BIN", bin)
	t.Setenv("IDLE_DECK_REMOTE_AGENT_ARGS", "[]")
	t.Setenv("IDLE_DECK_REMOTE_AGENT_ENV", `["FAKE_ACP_MODE=ok"]`)

	cfg := agentConfig()
	p := newHealthProbe(cfg.Bin, cfg.Args, "fake-model", cfg.Env)

	reachable, model := p.Check()
	if !reachable {
		t.Fatalf("config-driven agent: reachable=false, want true")
	}
	if model != "fake-model" {
		t.Fatalf("model=%q, want fake-model", model)
	}
}

// TestAgentConfigDrivenHealthProbeRateLimited verifies that the config-
// driven agent is used for error propagation: a JSON-RPC error from
// session/prompt means the model is not idle (D31).
func TestAgentConfigDrivenHealthProbeRateLimited(t *testing.T) {
	bin := buildFakeACP(t)

	t.Setenv("IDLE_DECK_REMOTE_AGENT_BIN", bin)
	t.Setenv("IDLE_DECK_REMOTE_AGENT_ARGS", "[]")
	t.Setenv("IDLE_DECK_REMOTE_AGENT_ENV", `["FAKE_ACP_MODE=error"]`)

	cfg := agentConfig()
	p := newHealthProbe(cfg.Bin, cfg.Args, "fake-model", cfg.Env)

	reachable, _ := p.Check()
	if reachable {
		t.Fatalf("config-driven rate-limited agent: reachable=true, want false (D31)")
	}
}
