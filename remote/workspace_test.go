package remote

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// execCommand builds an exec.Cmd without running it (for git commands in
// test helpers that need per-repo env or a different Dir).
func execCommand(name string, args ...string) *exec.Cmd {
	return exec.Command(name, args...)
}

// ---------------------------------------------------------------------------
// Acceptance tests for issue #28 — Workspace management
// ---------------------------------------------------------------------------
// These tests use a real local git repo as the "upstream GitHub", exercising
// the full PrepareWorkspace → OnSuccess/OnFailure path without network access.
// The PRCreator seam lets OnSuccess tests run without a real GitHub API.
// ---------------------------------------------------------------------------

// newUpstreamRepo creates a local git repo with one initial commit on the
// default branch and returns the repo's absolute path. A local path can be
// used as a Workspace's RepoURL because git accepts filesystem paths for
// clone/fetch/push.
func newUpstreamRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	// git refuses commits without an identity; set it per-repo.
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) {
		t.Helper()
		cmd := execCommand("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "-A")
	run("commit", "-m", "initial")
	return dir
}

// stubPR implements PRCreator and records every call.
type stubPR struct {
	mu     sync.Mutex
	calls  []stubPRCall
	failAt int // fail on the Nth call (1-based); 0 = never fail
	fail   string
}

type stubPRCall struct {
	Owner, Repo, Head, Base, Title, Body string
}

func (s *stubPR) CreatePR(_ context.Context, owner, repo, head, base, title, body string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, stubPRCall{owner, repo, head, base, title, body})
	if len(s.calls) == s.failAt {
		return "", fmt.Errorf("%s", s.fail)
	}
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", owner, repo, len(s.calls)+100), nil
}

// newWorkspaceManagerForTest wires a WorkspaceManager with a stub PRCreator.
func newWorkspaceManagerForTest(t *testing.T, token string) *WorkspaceManager {
	t.Helper()
	wm := NewWorkspaceManager(t.TempDir(), t.TempDir(), token)
	wm.PR = &stubPR{}
	return wm
}

// upstreamBranchExists checks whether the upstream repo contains the branch.
func upstreamBranchExists(t *testing.T, upstreamDir, branch string) bool {
	t.Helper()
	cmd := execCommand("git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	cmd.Dir = upstreamDir
	err := cmd.Run()
	return err == nil
}

// ---------------------------------------------------------------------------
// Acceptance 1: PrepareWorkspace returns a valid worktree directory
// ---------------------------------------------------------------------------

func TestWorkspacePrepValidWorktree(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := newWorkspaceManagerForTest(t, "tok-123")

	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-1")
	if err != nil {
		t.Fatalf("PrepareWorkspace: %v", err)
	}

	if _, err := os.Stat(ws.Dir); err != nil {
		t.Fatalf("worktree dir %q does not exist: %v", ws.Dir, err)
	}
	// The initial file from the upstream repo must be present.
	if _, err := os.Stat(filepath.Join(ws.Dir, "README.md")); err != nil {
		t.Fatalf("worktree missing README.md: %v", err)
	}
	// Branch must be resolved to a real name so OnSuccess can target it as
	// the PR base.
	if ws.Branch == "" || strings.HasPrefix(ws.Branch, "origin/") {
		t.Errorf("Branch=%q, want a short branch name (default of upstream)", ws.Branch)
	}
}

// PrepareWorkspace on a second attempt must reuse the bare cache: the
// upstream repo's objects are not re-cloned (Acceptance 5).
func TestWorkspacePrepReusesBareCache(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := newWorkspaceManagerForTest(t, "tok-123")

	if _, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-1"); err != nil {
		t.Fatalf("first prepare: %v", err)
	}

	before := countPackObjects(t, repoBareDir(wm, upstream))

	if _, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-2"); err != nil {
		t.Fatalf("second prepare: %v", err)
	}

	after := countPackObjects(t, repoBareDir(wm, upstream))
	if after > before {
		t.Errorf("packed objects grew from %d to %d — second prepare must reuse the cache, not re-clone", before, after)
	}
	// Second worktree must also be valid.
	if _, err := os.Stat(filepath.Join(wm.BaseDir, "worktree-attempt-2", "README.md")); err != nil {
		t.Fatalf("second worktree invalid: %v", err)
	}
}

func repoBareDir(wm *WorkspaceManager, repoURL string) string {
	return filepath.Join(wm.BareDir, BareRepoName(repoURL))
}

func countPackObjects(t *testing.T, dir string) int {
	t.Helper()
	cmd := execCommand("git", "count-objects", "-v")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("count-objects: %v: %s", err, out)
	}
	var packed, loose int
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "count: ") {
			loose = atoiTrim(line)
		}
		if strings.HasPrefix(line, "size-pack: ") {
			packed = atoiTrim(line)
		}
	}
	return packed + loose
}

func atoiTrim(line string) int {
	s := strings.TrimSpace(strings.TrimPrefix(line, "count: "))
	s = strings.TrimPrefix(s, "size-pack: ")
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// Two attempts at the same base must not conflict (the detached-worktree
// property that plain branch checkouts do not have).
func TestWorkspacePrepSecondAttemptSameBase(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := newWorkspaceManagerForTest(t, "tok-123")

	if _, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-1"); err != nil {
		t.Fatalf("first prepare: %v", err)
	}
	if _, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-2"); err != nil {
		t.Fatalf("second prepare (same base): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Acceptance 2: OnSuccess returns a pull_request artifact with a valid PR URL
// ---------------------------------------------------------------------------

func TestWorkspaceOnSuccessPRArtifact(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := newWorkspaceManagerForTest(t, "tok-123")

	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream, TicketID: "42"}, "attempt-1")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	// Simulate the agent making a change.
	if err := os.WriteFile(filepath.Join(ws.Dir, "change.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write change: %v", err)
	}

	art, err := wm.OnSuccess(ws, ws, "attempt-1")
	if err != nil {
		t.Fatalf("OnSuccess: %v", err)
	}

	if art.Kind != "pull_request" {
		t.Fatalf("Kind=%q, want pull_request", art.Kind)
	}
	if !strings.Contains(art.URI, "/pull/") {
		t.Fatalf("URI=%q, want a PR URL", art.URI)
	}

	// The pushed feature branch must exist in the upstream repo.
	// (For a local-path "GitHub", this is the direct analogue of the
	// pushed branch being fetchable from origin.)
	stub := wm.PR.(*stubPR)
	stub.mu.Lock()
	defer stub.mu.Unlock()
	if len(stub.calls) != 1 {
		t.Fatalf("PRCreator called %d times, want 1", len(stub.calls))
	}
	c := stub.calls[0]
	if !strings.HasPrefix(c.Head, "feature/42-") {
		t.Errorf("Head=%q, want feature/42-<hash> (D15: feature/<ticket_id>-<slug>)", c.Head)
	}
	if c.Base != ws.Branch {
		t.Errorf("Base=%q, want %q (resolved by PrepareWorkspace)", c.Base, ws.Branch)
	}
	if !upstreamBranchExists(t, upstream, c.Head) {
		t.Errorf("feature branch %q not found in upstream after OnSuccess", c.Head)
	}
}

// OnSuccess with no token must fail fast.
func TestWorkspaceOnSuccessNoToken(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := NewWorkspaceManager(t.TempDir(), t.TempDir(), "")
	wm.PR = &stubPR{}

	ws, _ := wm.PrepareWorkspace(Workspace{RepoURL: upstream, TicketID: "1"}, "a1")
	_, err := wm.OnSuccess(ws, ws, "a1")
	if err == nil {
		t.Fatal("OnSuccess: expected error when GitHubToken is empty")
	}
}

// PRCreator failures must propagate (a PR that cannot be opened is a
// failure, not a silent success).
func TestWorkspaceOnSuccessPRFail(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := newWorkspaceManagerForTest(t, "tok-123")
	wm.PR = &stubPR{failAt: 1, fail: "gh: not logged in"}

	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream, TicketID: "42"}, "attempt-1")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err = wm.OnSuccess(ws, ws, "attempt-1")
	if err == nil {
		t.Fatal("OnSuccess: expected error when PRCreator fails")
	}
	if !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("error should surface the PRCreator failure: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Acceptance 3: OnFailure returns a branch artifact with BranchPurpose: debug
// ---------------------------------------------------------------------------

func TestWorkspaceOnFailureDebugBranch(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := newWorkspaceManagerForTest(t, "tok-123")

	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream, TicketID: "99"}, "attempt-fail-1")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	// Simulate the agent making a (partial, broken) change.
	if err := os.WriteFile(filepath.Join(ws.Dir, "broken.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	art, err := wm.OnFailure(ws, ws, "attempt-fail-1")
	if err != nil {
		t.Fatalf("OnFailure: %v", err)
	}

	if art.Kind != "branch" {
		t.Fatalf("Kind=%q, want branch", art.Kind)
	}
	if art.BranchPurpose == nil || *art.BranchPurpose != "debug" {
		t.Fatalf("BranchPurpose=%v, want debug", art.BranchPurpose)
	}
	if !strings.Contains(art.URI, "/tree/debug/attempt-fail-1") {
		t.Fatalf("URI=%q, want /tree/debug/attempt-fail-1", art.URI)
	}

	// The debug branch must actually have been pushed to the upstream.
	if !upstreamBranchExists(t, upstream, "debug/attempt-fail-1") {
		t.Fatal("debug/attempt-fail-1 not found in upstream after OnFailure")
	}
}

// OnFailure with no token must fail fast.
func TestWorkspaceOnFailureNoToken(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := NewWorkspaceManager(t.TempDir(), t.TempDir(), "")
	ws, _ := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "a1")
	_, err := wm.OnFailure(ws, ws, "a1")
	if err == nil {
		t.Fatal("OnFailure: expected error when GitHubToken is empty")
	}
}

// ---------------------------------------------------------------------------
// Acceptance 4: git push authenticates with the configured GitHubToken
// ---------------------------------------------------------------------------

// tokenURL is the credential-embedding step used by PushToUpstream: for an
// HTTPS upstream the token must appear in the push URL; for local paths it
// must pass through (local filesystem clones need no auth).
func TestTokenURL(t *testing.T) {
	cases := []struct {
		url   string
		token string
		want  string
	}{
		{"https://github.com/acme/widgets.git", "ghp_abc", "https://ghp_abc@github.com/acme/widgets.git"},
		{"https://github.com/acme/widgets", "ghp_abc", "https://ghp_abc@github.com/acme/widgets"},
		{"/tmp/local/repo", "tok", "/tmp/local/repo"},
	}
	for _, c := range cases {
		got, err := tokenURL(c.url, c.token)
		if err != nil {
			t.Errorf("tokenURL(%q): unexpected error: %v", c.url, err)
			continue
		}
		if got != c.want {
			t.Errorf("tokenURL(%q) = %q, want %q", c.url, got, c.want)
		}
	}
	// SSH is unsupported (MVP).
	if _, err := tokenURL("git@github.com:acme/widgets.git", "tok"); err == nil {
		t.Error("tokenURL(ssh): expected error")
	}
}

// webBranchURL tests for GHE-correct branch artifact URIs (issue #40).
// The host is derived from the upstream URL, not hardcoded to github.com.
func TestWebBranchURL_GHE(t *testing.T) {
	cases := []struct {
		url       string
		branch    string
		want      string
		wantHost  string // expected host in the URL
	}{
		// GitHub (default behavior unchanged)
		{"https://github.com/acme/widgets.git", "feature/abc123", "https://github.com/acme/widgets/tree/feature/abc123", "github.com"},
		// GitHub Enterprise
		{"https://git.acme.com/o/r.git", "debug/attempt-1", "https://git.acme.com/o/r/tree/debug/attempt-1", "git.acme.com"},
		// HTTPS without .git suffix
		{"https://github.com/acme/widgets", "feature/xyz", "https://github.com/acme/widgets/tree/feature/xyz", "github.com"},
		// Local path (file:// behaviour unchanged)
		{"/tmp/local/repo", "debug/123", "file:///tmp/local/repo/tree/debug/123", ""},
	}
	for _, c := range cases {
		got, err := webBranchURL(c.url, c.branch)
		if err != nil {
			t.Errorf("webBranchURL(%q, %q): unexpected error: %v", c.url, c.branch, err)
			continue
		}
		if got != c.want {
			t.Errorf("webBranchURL(%q, %q) = %q, want %q", c.url, c.branch, got, c.want)
		}
		// Additional check: host present in URL
		if c.wantHost != "" && !strings.Contains(got, "https://"+c.wantHost) {
			t.Errorf("webBranchURL(%q, %q) = %q, expected host %q", c.url, c.branch, got, c.wantHost)
		}
	}
}

// PushToUpstream pushes the local branch to the given upstream URL. After a
// successful push the branch exists in the upstream — the end-to-end proof
// that the push path (including its auth URL) works.
func TestPushToUpstreamDeliversBranch(t *testing.T) {
	upstream := newUpstreamRepo(t)
	wm := newWorkspaceManagerForTest(t, "tok-123")
	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream, TicketID: "5"}, "attempt-push")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws.Dir, "y.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	gr := NewGitRunner(ws.Dir)
	if err := gr.RunError("add", "-A"); err != nil {
		t.Fatal(err)
	}
	if err := gr.RunError("commit", "-m", "change"); err != nil {
		t.Fatal(err)
	}
	if err := gr.RunError("checkout", "-b", "feature/5-test"); err != nil {
		t.Fatal(err)
	}
	if err := PushToUpstream(ws.Dir, upstream, "tok-123", "feature/5-test"); err != nil {
		t.Fatalf("PushToUpstream: %v", err)
	}
	if !upstreamBranchExists(t, upstream, "feature/5-test") {
		t.Fatal("feature/5-test not found in upstream after PushToUpstream")
	}
}
