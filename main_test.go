package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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
	cmd := exec.Command(bin, "check")
	dir, err := os.MkdirTemp("", "idle-deck-test-*")
	if err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(dir, "test.db")
	cmd.Env = append(cleanEnv(os.Environ()),
		"IDLE_DECK_GITHUB_TOKEN=ghp_test_token",
		"IDLE_DECK_REPOS=acme/widgets",
		"IDLE_DECK_HARNESS_URL=https://h.example.com",
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
	if !strings.Contains(s, "SQLite: path") {
		t.Errorf("output missing SQLite writable line: %s", s)
	}
	if !strings.Contains(s, "schema: pending") {
		t.Errorf("output missing schema deferred notice: %s", s)
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
	if !strings.Contains(string(out), "idle-deck 0.0.0-dev") {
		t.Errorf("unexpected version output: %s", out)
	}
}
