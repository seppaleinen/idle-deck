package remote

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// newGCServer builds a Server whose WorkspaceManager points at temp dirs,
// with a short WorktreeTTL so tests don't wait 24h. The clock is injected
// per-call via GCWorktrees(now), so tests backdate directories with
// os.Chtimes instead of waiting a real TTL.
func newGCServer(t *testing.T, ttl time.Duration) *Server {
	t.Helper()
	cfg := DefaultConfig(t.TempDir(), t.TempDir(), "")
	cfg.WorktreeTTL = ttl
	return &Server{
		Runs:       make(map[string]*Run),
		AttemptMap: make(map[string]string),
		Runner:     newStubRunner(),
		Health:     func() (bool, string) { return true, "stub-model" },
		Config:     cfg,
		HTTPToken:  "",
	}
}

// backdate sets a directory's mtime to now - d.
func backdate(t *testing.T, dir string, d time.Duration) {
	t.Helper()
	if err := os.Chtimes(dir, time.Now().UTC().Add(-d), time.Now().UTC().Add(-d)); err != nil {
		t.Fatalf("Chtimes(%s): %v", dir, err)
	}
}

func dirExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(dir)
	return err == nil
}

// ---------------------------------------------------------------------------
// Acceptance 1: a worktree older than WorktreeTTL is removed.
// ---------------------------------------------------------------------------

func TestGC_RemovesStaleWorktree(t *testing.T) {
	srv := newGCServer(t, time.Hour)
	wm := srv.Config.WorkspaceManager
	upstream := newUpstreamRepo(t)
	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-gc-1")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	backdate(t, ws.Dir, 2*time.Hour) // older than the 1h TTL

	srv.GCWorktrees(time.Now().UTC())
	if dirExists(t, ws.Dir) {
		t.Fatalf("stale worktree %q should have been removed", ws.Dir)
	}
	// The bare cache must survive GC — only worktrees are touched.
	if !dirExists(t, filepath.Join(wm.BareDir, BareRepoName(upstream))) {
		t.Fatal("bare cache dir was removed — GC must only touch worktrees")
	}
}

// ---------------------------------------------------------------------------
// Acceptance 2: a worktree a running run is using is never removed.
// ---------------------------------------------------------------------------

func TestGC_SkipsRunningRun(t *testing.T) {
	srv := newGCServer(t, time.Hour)
	wm := srv.Config.WorkspaceManager
	upstream := newUpstreamRepo(t)
	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-gc-2")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	backdate(t, ws.Dir, 2*time.Hour)

	// Register a running run using this worktree.
	srv.Runs["run-active"] = &Run{
		ID:        "run-active",
		Status:    StatusRunning,
		Workspace: ws,
	}

	srv.GCWorktrees(time.Now().UTC())
	if !dirExists(t, ws.Dir) {
		t.Fatal("worktree of a running run was removed — GC must skip active runs")
	}
}

// ---------------------------------------------------------------------------
// Acceptance 3: a worktree newer than WorktreeTTL is left alone.
// ---------------------------------------------------------------------------

func TestGC_LeavesFreshWorktree(t *testing.T) {
	srv := newGCServer(t, time.Hour)
	wm := srv.Config.WorkspaceManager
	upstream := newUpstreamRepo(t)
	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-gc-3")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}

	srv.GCWorktrees(time.Now().UTC())
	if !dirExists(t, ws.Dir) {
		t.Fatal("fresh worktree was removed — GC must respect WorktreeTTL")
	}
}

// ---------------------------------------------------------------------------
// Acceptance 4: WorktreeTTL == 0 disables GC entirely.
// ---------------------------------------------------------------------------

func TestGC_TTLZeroDisabled(t *testing.T) {
	srv := newGCServer(t, 0) // 0 = never delete
	wm := srv.Config.WorkspaceManager
	upstream := newUpstreamRepo(t)
	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-gc-4")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	backdate(t, ws.Dir, 7*24*time.Hour) // very old, but TTL=0

	srv.GCWorktrees(time.Now().UTC())
	if !dirExists(t, ws.Dir) {
		t.Fatal("worktree removed with WorktreeTTL=0 — GC must be disabled")
	}
}

// ---------------------------------------------------------------------------
// Acceptance 5: GC only touches worktree-* directories. A stale
// non-worktree directory under BaseDir is left alone.
// ---------------------------------------------------------------------------

func TestGC_OnlyWorktreeDirs(t *testing.T) {
	srv := newGCServer(t, time.Hour)
	wm := srv.Config.WorkspaceManager
	upstream := newUpstreamRepo(t)

	// A stale worktree — should be removed.
	ws, err := wm.PrepareWorkspace(Workspace{RepoURL: upstream}, "attempt-gc-5")
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	backdate(t, ws.Dir, 2*time.Hour)

	// A non-worktree directory that is old — must be left alone.
	other := filepath.Join(wm.BaseDir, "not-a-worktree")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatalf("mkdir other: %v", err)
	}
	backdate(t, other, 2*time.Hour)

	srv.GCWorktrees(time.Now().UTC())
	if dirExists(t, ws.Dir) {
		t.Fatal("stale worktree should have been removed")
	}
	if !dirExists(t, other) {
		t.Fatal("non-worktree directory was removed — GC must only touch worktree-*")
	}
}
