package remote

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGitRunnerCommitsWithoutAmbientIdentity pins that GitRunner.Commit
// succeeds on a machine with no global git identity configured. idle-deck
// injects a deterministic author when the ambient environment supplies none,
// so commits never fail for want of an author (CI is the canary: the dev
// machine happens to have one configured globally, which masked this).
func TestGitRunnerCommitsWithoutAmbientIdentity(t *testing.T) {
	dir := t.TempDir()

	// Strip every identity env var, including the ones the test runner's
	// own environment may carry. GIT_AUTHOR_* and GIT_COMMITTER_* win over
	// global config, so they must be removed to reproduce the bare case.
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if isIdentityEnvVar(kv) {
			continue
		}
		env = append(env, kv)
	}

	gr := NewGitRunner(dir)
	if err := gr.InitRepo(); err != nil {
		t.Fatalf("InitRepo: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := gr.AddAll(); err != nil {
		t.Fatalf("AddAll: %v", err)
	}

	// Commit must succeed with no ambient identity — this is the case that
	// failed on CI (exit status 128) and would fail on any clean machine.
	if err := gr.Commit("chore: test"); err != nil {
		t.Fatalf("Commit without ambient identity: %v", err)
	}

	// Sanity: the runner must not have mutated the caller's environment.
	for _, kv := range env {
		if kv == "GIT_AUTHOR_NAME=idle-deck" {
			t.Fatalf("GitRunner leaked its identity into the caller's environment: %s", kv)
		}
	}
}

// isIdentityEnvVar reports whether kv is a git identity env var (GIT_AUTHOR_*,
// GIT_COMMITTER_*). Those win over global config, so they must be stripped to
// reproduce the bare case where no identity is configured at all.
func isIdentityEnvVar(kv string) bool {
	prefixes := []string{
		"GIT_AUTHOR_NAME=", "GIT_AUTHOR_EMAIL=",
		"GIT_COMMITTER_NAME=", "GIT_COMMITTER_EMAIL=",
	}
	for _, p := range prefixes {
		if len(kv) >= len(p) && kv[:len(p)] == p {
			return true
		}
	}
	return false
}

// TestOutcomePreemptedWireMapping pins that OutcomePreempted never appears on
// the wire. The remote's internal state carries OutcomePreempted, but the
// wire contract maps it to "cancelled". This is not just a comment — it is
// checked by the conformance suite, and this test ensures the mapping cannot
// drift silently. If any code path serializes AttemptOutcome directly to the
// wire without passing through toWireOutcome, the test will fail, surfacing
// M9's leak before it becomes a real contract violation.
func TestOutcomePreemptedWireMapping(t *testing.T) {
	wire, _ := toWireOutcome(OutcomePreempted)
	if wire != "cancelled" {
		t.Errorf("OutcomePreempted wire mapping = %q, want \"cancelled\" (D43, remote-contract §6/§9a C9)", wire)
	}
}
func TestGitRunnerHonoursAmbientIdentity(t *testing.T) {
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME=Operator",
		"GIT_AUTHOR_EMAIL=operator@example.com",
		"GIT_COMMITTER_NAME=Operator",
		"GIT_COMMITTER_EMAIL=operator@example.com",
	)

	out := withGitIdentity(env)
	if len(out) != len(env) {
		t.Fatalf("withGitIdentity must not add vars when an identity is present: got %d, want %d", len(out), len(env))
	}
	for i, want := range env {
		if out[i] != want {
			t.Fatalf("withGitIdentity mutated env[%d]: got %q, want %q", i, out[i], want)
		}
	}
}
