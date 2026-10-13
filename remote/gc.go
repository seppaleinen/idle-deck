package remote

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GCPeriod is how often the GC loop sweeps BaseDir. It is deliberately coarse
// relative to WorktreeTTL (24h default): an hourly tick is a cheap heartbeat,
// and a stale worktree is never more than one tick old before removal.
const GCPeriod = time.Hour

// worktreeDirPrefix is the naming convention PrepareWorkspace uses for
// worktree directories under BaseDir: "worktree-<attemptID>". GC scans for
// this prefix and nothing else — the per-repo bare cache lives under BareDir,
// which GC never touches.
const worktreeDirPrefix = "worktree-"

// GCWorktrees removes stale worktrees from the server's BaseDir. A worktree is
// removable when its directory mtime is older than Config.WorktreeTTL.
// Worktrees belonging to a running run are never removed, regardless of age.
//
// WorktreeTTL == 0 disables GC entirely (see Config.WorktreeTTL).
//
// Only <BaseDir>/worktree-* entries are touched; the per-repo bare cache is
// never scanned or removed. The caller passes the current time so the clock is
// injectable — tests backdate directories with os.Chtimes instead of waiting
// a real TTL.
//
// This is the teardown half of D27: the remote owns the workspace for the
// whole run lifecycle, and GC is what reclaims it afterwards. (#31)
func (s *Server) GCWorktrees(now time.Time) {
	if s.Config == nil || s.Config.WorkspaceManager == nil {
		return
	}
	ttl := s.Config.WorktreeTTL
	if ttl <= 0 {
		return // 0 means never delete (config.go)
	}
	baseDir := s.Config.WorkspaceManager.BaseDir
	if baseDir == "" {
		return
	}
	entries, err := os.ReadDir(baseDir)
	if err != nil {
		// BaseDir may not exist yet (no run ever prepared a workspace).
		return
	}
	active := s.activeWorktreeDirs()
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), worktreeDirPrefix) {
			continue
		}
		dir := filepath.Join(baseDir, e.Name())
		if active[filepath.Clean(dir)] {
			continue // a running run is using it — never remove
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) < ttl {
			continue // not old enough yet
		}
		bareDir, err := bareDirForWorktree(dir)
		if err != nil {
			// Can't determine the bare repo — not safe to remove. Skip.
			continue
		}
		if err := RemoveWorktree(bareDir, dir); err != nil {
			// One bad worktree must not stall GC. Log and move on.
			log.Printf("remote/gc: remove %s: %v", dir, err)
		}
	}
}

// activeWorktreeDirs returns the worktree directories currently in use by a
// running run. Read under s.mu — the same lock handleRuns takes when it
// registers a run — so the snapshot is consistent with the run table.
//
// The age gate in GCWorktrees is the secondary guard: a worktree prepared
// during a GC pass (between PrepareWorkspace and NewRun registering the run)
// has a fresh mtime and cannot exceed TTL, so the prepare→register window is
// safe even though the run is not yet in the map.
func (s *Server) activeWorktreeDirs() map[string]bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := map[string]bool{}
	for _, r := range s.Runs {
		if r.Status == StatusRunning && r.Workspace.Dir != "" {
			active[filepath.Clean(r.Workspace.Dir)] = true
		}
	}
	return active
}

// bareDirForWorktree returns the bare repository directory a linked worktree
// was created from. Linked worktrees share the bare's object store; the
// worktree's .git file records the admin path, and rev-parse --git-common-dir
// resolves the bare repo root from it. This is how GC finds the bare repo to
// pass to RemoveWorktree without the worktree carrying a BareDir field.
func bareDirForWorktree(worktreeDir string) (string, error) {
	gr := NewGitRunner(worktreeDir)
	out, err := gr.Run("rev-parse", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("git-common-dir: %w", err)
	}
	return strings.TrimSpace(out), nil
}