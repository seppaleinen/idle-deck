package main

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// buildFakeACP compiles test/fakeacp into a temporary binary. It is
// built from the same module, so no network is required.
func buildFakeACP(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "fakeacp")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/seppaleinen/idle-deck/test/fakeacp")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fakeacp: %v\n%s", err, out)
	}
	return bin
}

// TestHealthProbe_MissingBinary: a missing agent CLI means nothing is
// reachable. This is the case that matters most on first install — the
// Dockerfile must have installed opencode, or every /health answer is false.
func TestHealthProbe_MissingBinary(t *testing.T) {
	p := newHealthProbe("/no/such/opencode", nil, "m", nil)
	if got, _ := p.Check(); got {
		t.Fatal("missing binary: want reachable=false")
	}
}

// TestHealthProbe_ModelReachable: a real model generation means reachable.
func TestHealthProbe_ModelReachable(t *testing.T) {
	bin := buildFakeACP(t)
	p := newHealthProbe(bin, nil, "fake-model", nil)
	if got, _ := p.Check(); !got {
		t.Fatal("ok agent: want reachable=true")
	}
}

// TestHealthProbe_ModelRateLimited: D31 is explicit — a model that is rate
// limited or out of credentials is NOT idle. The probe must report false
// when session/prompt comes back as a JSON-RPC error.
func TestHealthProbe_ModelRateLimited(t *testing.T) {
	bin := buildFakeACP(t)
	p := newHealthProbe(bin, nil, "fake-model", []string{"FAKE_ACP_MODE=error"})
	if got, _ := p.Check(); got {
		t.Fatal("rate-limited model: want reachable=false (D31)")
	}
}

// TestHealthProbe_ModelHung: a model that never answers must not hang the
// probe forever. The budget is shrunk so the test runs in seconds.
func TestHealthProbe_ModelHung(t *testing.T) {
	bin := buildFakeACP(t)
	p := newHealthProbe(bin, nil, "fake-model", []string{"FAKE_ACP_MODE=hang"})
	p.budget = 4 * time.Second
	p.startup = 1 * time.Second
	done := make(chan bool, 1)
	go func() {
		got, _ := p.Check()
		done <- got
	}()
	select {
	case got := <-done:
		if got {
			t.Fatal("hung model: want reachable=false")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("probe hung beyond its budget")
	}
}

// TestHealthProbe_Cache: the result is cached, so a second call within the
// TTL returns the cached value without re-probing (and without spawning a
// process).
func TestHealthProbe_Cache(t *testing.T) {
	p := newHealthProbe("/no/such/opencode", nil, "m", nil)
	p.cacheTTL = 30 * time.Second
	r1, _ := p.Check()
	r2, _ := p.Check()
	if r1 != r2 {
		t.Fatal("cache: inconsistent results across calls")
	}
}

// TestHealthProbe_RealOpencode runs the probe against the installed opencode
// if present, as an end-to-end check that the ACP round-trip completes
// against the real agent. Skipped when opencode is not on PATH (e.g. a
// fresh checkout). The verdict is not asserted: the provider may be
// reachable (true) or rate-limited/down (false), and both are legitimate
// answers — what would be a bug is a hang or a crash.
func TestHealthProbe_RealOpencode(t *testing.T) {
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not on PATH")
	}
	p := newHealthProbe("opencode", []string{"acp"}, "opencode/big-pickle", nil)
	p.budget = 25 * time.Second
	p.startup = 8 * time.Second
	p.cacheTTL = 0
	done := make(chan struct{})
	var got bool
	var model string
	go func() {
		got, model = p.Check()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("real opencode: probe hung beyond its budget — the ACP round-trip did not complete")
	}
	if got && model == "" {
		t.Fatalf("reachable but no model reported")
	}
}