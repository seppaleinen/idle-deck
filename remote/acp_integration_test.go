package remote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// TestACP_RealOpencode is a **vendor compatibility smoke test**, not an
// integration test: it runs the ACP adapter against a real opencode process
// and checks that our client can still talk to it. It exists to catch
// opencode ACP drift — a changed sessionId format, a renumbered
// session/update shape, a renamed tool_call field — before it silently
// breaks production runs.
//
// It is NOT the CI gate. `remote/conformance_test.go` drives the same
// acpRunner against `test/fakeacp` (ok/error/hang/die/slow) and covers
// idempotency, server-enforced timeout, confirmed-dead abort, the outcome
// taxonomy, and artifact typing — with no binary, no network, no
// credentials. That suite is the gate, per D38.
//
// Opt-in only: set IDLE_DECK_REAL_ACP_TESTS=1 to run it. It needs a real
// opencode install AND configured provider credentials; without them
// session/prompt returns an auth error and the outcome is empty, which is
// correct behaviour but not a pass. Run it manually after every opencode
// upgrade, on a machine that has credentials.
func TestACP_RealOpencode(t *testing.T) {
	if os.Getenv("IDLE_DECK_REAL_ACP_TESTS") != "1" {
		t.Skip("set IDLE_DECK_REAL_ACP_TESTS=1 to run against a real opencode + model")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not on PATH")
	}

	// A workspace the agent can write to.
	dir := t.TempDir()
	initGitRepo(t, dir)

	runner := NewACP(ACPConfig{
		Bin: "opencode",
		Args: []string{"acp"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sid, err := runner.Start(ctx, Workspace{Dir: dir, RepoURL: dir, TicketID: "42"}, StartRequest{
		Role:   "do",
		Prompt: "Use the bash tool to run: echo 'hello from acp' > OUT.txt. Then run: git log --oneline -1. Reply with DONE.",
		Budget: 10000,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	res, err := runner.Result(ctx, sid)
	if err != nil {
		t.Fatalf("Result: %v", err)
	}
	if res.Outcome != OutcomeSucceeded {
		t.Fatalf("outcome=%q, want succeeded", res.Outcome)
	}

	// The agent ran bash in the workspace; OUT.txt should exist.
	if _, err := os.Stat(filepath.Join(dir, "OUT.txt")); err != nil {
		t.Fatalf("OUT.txt missing: %v", err)
	}

	// opencode acp is a server that stays alive after the prompt; kill it
	// so the test doesn't leak an orphan process.
	_ = runner.Abort(ctx, sid)
}

// TestACP_AbortKillsProcess verifies that Abort kills the agent and that
// Result unblocks (confirmed-dead, I8).
func TestACP_AbortKillsProcess(t *testing.T) {
	if os.Getenv("IDLE_DECK_REAL_ACP_TESTS") != "1" {
		t.Skip("set IDLE_DECK_REAL_ACP_TESTS=1 to run against a real opencode + model")
	}
	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not on PATH")
	}

	dir := t.TempDir()
	initGitRepo(t, dir)

	runner := NewACP(ACPConfig{
		Bin:  "opencode",
		Args: []string{"acp"},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sid, err := runner.Start(ctx, Workspace{Dir: dir, RepoURL: dir, TicketID: "42"}, StartRequest{
		Role:   "do",
		Prompt: "Run a long sleep: sleep 300. Do not stop until I tell you.",
		Budget: 10000,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the agent a moment to begin, then abort.
	time.Sleep(3 * time.Second)

	// Result must be blocked in a goroutine before abort; after abort
	// unblocks it, the outcome is empty (server decides the wire state).
	type resOut struct {
		res RunResult
		err error
	}
	ch := make(chan resOut, 1)
	go func() {
		res, err := runner.Result(ctx, sid)
		ch <- resOut{res, err}
	}()

	if err := runner.Abort(ctx, sid); err != nil {
		t.Fatalf("Abort: %v", err)
	}

	select {
	case out := <-ch:
		if out.err != nil {
			t.Fatalf("Result after abort: %v", out.err)
		}
		// The outcome is empty because the agent was killed before the
		// prompt response; the server's collect() maps that using its flags.
		if out.res.Outcome != "" {
			t.Fatalf("outcome=%q, want empty (server decides)", out.res.Outcome)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Result did not unblock after abort")
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	cmds := [][]string{
		{"git", "init", "-q"},
		{"git", "config", "user.email", "t@t.t"},
		{"git", "config", "user.name", "t"},
	}
	for _, c := range cmds {
		cmd := exec.Command(c[0], c[1:]...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", c[0], err, out)
		}
	}
	// Add a file to commit
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# x\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := exec.Command("git", "add", "-A")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, out)
	}
	cmd = exec.Command("git", "commit", "-qm", "init")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v: %s", err, out)
	}
}

var _ = atomic.LoadInt32
