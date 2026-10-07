package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/seppaleinen/idle-deck/test/stubs"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "idle-deck-test-*")
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "idle-deck")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func repoRoot(t *testing.T) string {
	t.Helper()
	// Tests run from the module root for the main package.
	return "."
}

func cleanEnv(env []string) []string {
	var out []string
	for _, e := range env {
		if strings.HasPrefix(e, "IDLE_DECK_") {
			continue
		}
		out = append(out, e)
	}
	return out
}

func TestCheckNoConfig(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "check")
	cmd.Env = cleanEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit with no config")
	}
	s := string(out)
	for _, name := range []string{"IDLE_DECK_GITHUB_TOKEN", "IDLE_DECK_REPOS", "IDLE_DECK_HARNESS_URL", "IDLE_DECK_HARNESS_TOKEN"} {
		if !strings.Contains(s, name) {
			t.Errorf("output missing required var name %q: %s", name, s)
		}
	}
}

func TestCheckAllSet(t *testing.T) {
	bin := buildBinary(t)

	// Start tracker and harness stubs (D38: base-URL overrides for dev loop).
	tState := stubs.NewTrackerState("acme/widgets")
	tServer := stubs.NewTrackerServer(tState)
	defer tServer.Close()
	tState.AddRepoLabel("idle-hotfix")
	tState.AddRepoLabel("idle-ready")
	tState.AddRepoLabel("idle-redo")
	tState.AddRepoLabel("idle-needs-human")

	hState := stubs.NewHarnessState("stub-gpt-4o")
	hServer := stubs.NewHarnessServer(hState)
	defer hServer.Close()

	dir, err := os.MkdirTemp("", "idle-deck-test-*")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "test.db")
	cmd := exec.Command(bin, "check")
	cmd.Env = append(cleanEnv(os.Environ()),
		"IDLE_DECK_GITHUB_TOKEN=ghp_test_token",
		"IDLE_DECK_GITHUB_API="+tServer.URL(),
		"IDLE_DECK_REPOS=acme/widgets",
		"IDLE_DECK_HARNESS_URL="+hServer.URL(),
		"IDLE_DECK_HARNESS_TOKEN=htok",
		"IDLE_DECK_DB="+dbPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("expected exit 0: %v\n%s", err, out)
	}
	s := string(out)
	if !strings.Contains(s, "<redacted>") {
		t.Errorf("output missing <redacted>: %s", s)
	}
	if strings.Count(s, "<redacted>") < 2 {
		t.Errorf("expected at least 2 <redacted>, got %d: %s", strings.Count(s, "<redacted>"), s)
	}
	if strings.Contains(s, "ghp_test_token") || strings.Contains(s, "htok") {
		t.Errorf("output leaks a token: %s", s)
	}
	if !strings.Contains(s, "sqlite: schema current") {
		t.Errorf("output missing schema current line: %s", s)
	}
	if !strings.Contains(s, "tracker: authenticated read OK") {
		t.Errorf("output missing tracker read OK: %s", s)
	}
	if !strings.Contains(s, "harness: /health reachable") {
		t.Errorf("output missing harness health OK: %s", s)
	}
	if !strings.Contains(s, "idle-hotfix: present") {
		t.Errorf("output missing label present: %s", s)
	}
}

func TestRunRefusesMissing(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "run")
	cmd.Env = cleanEnv(os.Environ())
	_, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected non-zero exit with no config")
	}
}

func TestVersion(t *testing.T) {
	bin := buildBinary(t)
	cmd := exec.Command(bin, "--version")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("version exited non-zero: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "idle-deck v0.1.0") {
		t.Errorf("unexpected version output: %s", out)
	}
}
