package remote

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// GitRunner runs git commands in a given directory.
type GitRunner struct {
	Dir string
}

// NewGitRunner creates a GitRunner for the given working directory.
func NewGitRunner(dir string) *GitRunner {
	return &GitRunner{Dir: dir}
}

// Run executes the git command and returns its combined output.
// It does not interpret the exit code; the caller must check it.
func (g *GitRunner) Run(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = g.Dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// RunError executes the git command and returns only the error.
// It discards the output.
func (g *GitRunner) RunError(args ...string) error {
	_, err := g.Run(args...)
	return err
}

// InitRepo initializes an empty git repository if the directory is not already a repo.
// It does not change an existing repository.
func (g *GitRunner) InitRepo() error {
	_, err := g.Run("rev-parse", "--is-inside-work-tree")
	if err == nil {
		return nil // already a repo
	}
	return g.RunError("init")
}

// SetRemoteOrigin sets the origin remote to the given URL.
// If a remote named origin already exists, it updates its URL.
func (g *GitRunner) SetRemoteOrigin(url string) error {
	// Check if origin exists.
	out, err := g.Run("remote", "get-url", "origin")
	if err == nil && strings.TrimSpace(out) == url {
		return nil // already set
	}
	// Remove if exists (ignore error if not).
	_ = g.RunError("remote", "remove", "origin")
	return g.RunError("remote", "add", "origin", url)
}

// Fetch fetches from origin.
func (g *GitRunner) Fetch() error {
	return g.RunError("fetch", "origin")
}

// CheckoutBranch checks out the given branch, creating it if it doesn't exist.
// If startPoint is empty, it checks out the branch as is.
func (g *GitRunner) CheckoutBranch(branch, startPoint string) error {
	if startPoint != "" {
		// Create and check out the branch from startPoint.
		return g.RunError("checkout", "-B", branch, startPoint)
	}
	// Check out the branch (must exist).
	return g.RunError("checkout", branch)
}

// CreateBranchFromHead creates a new branch from HEAD and checks it out.
func (g *GitRunner) CreateBranchFromHead(branch string) error {
	return g.RunError("checkout", "-b", branch)
}

// PushBranch pushes the given branch to the named remote and sets the
// upstream. If force is true, it uses --force-with-lease.
//
// The remote name is explicit because a worktree's "origin" is the local
// bare cache (the shared object store), not the upstream GitHub remote.
// Pushing to "origin" would push into the cache and never reach the repo.
func (g *GitRunner) PushBranch(branch, remote string, force bool) error {
	args := []string{"push", remote, branch}
	if force {
		args = append(args, "--force-with-lease")
	} else {
		args = append(args, "-u")
	}
	return g.RunError(args...)
}

// GetCurrentBranch returns the name of the current branch, or empty if detached.
func (g *GitRunner) GetCurrentBranch() (string, error) {
	out, err := g.Run("rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// GetCommitHash returns the full hash of the current HEAD.
func (g *GitRunner) GetCommitHash() (string, error) {
	out, err := g.Run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// StatusPorcelain returns the output of `git status --porcelain=v1`.
// It returns an empty string if there are no changes.
func (g *GitRunner) StatusPorcelain() (string, error) {
	out, err := g.Run("status", "--porcelain=v1")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// AddAll stages all changes (including new and deleted files) for commit.
func (g *GitRunner) AddAll() error {
	return g.RunError("add", "-A")
}

// Commit creates a commit with the given message.
// It returns an error if there are no changes to commit.
func (g *GitRunner) Commit(message string) error {
	// Check if there are changes to commit.
	status, err := g.StatusPorcelain()
	if err != nil {
		return err
	}
	if status == "" {
		return errors.New("no changes to commit")
	}
	return g.RunError("commit", "-m", message)
}

// ApplyStash applies the most recent stash and drops it.
// It returns an error if there is no stash to apply.
func (g *GitRunner) ApplyStash() error {
	return g.RunError("stash", "pop", "--index")
}

// StashChanges stashes all changes, including untracked files.
// It returns the stash reference (e.g. "stash@{0}").
func (g *GitRunner) StashChanges() (string, error) {
	out, err := g.Run("stash", "push", "-u", "-m", "wip")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// IsClean returns true if there are no changes staged or unstaged.
func (g *GitRunner) IsClean() (bool, error) {
	status, err := g.StatusPorcelain()
	if err != nil {
		return false, err
	}
	return status == "", nil
}

// CloneInto clones the repository into the given directory.
// If the directory already exists and is a git repo, it fetches and resets to origin/HEAD.
// This function is safe to call repeatedly — it will update an existing clone.
func CloneInto(repoURL, destDir string) error {
	// If the directory exists and is a repo, fetch and reset.
	if _, err := os.Stat(destDir); err == nil {
		gr := NewGitRunner(destDir)
		// Check if it's a git repo.
		if _, err := gr.Run("rev-parse", "--is-inside-work-tree"); err == nil {
			// Set origin and fetch.
			if err := gr.SetRemoteOrigin(repoURL); err != nil {
				return fmt.Errorf("set origin: %w", err)
			}
			if err := gr.Fetch(); err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			// Reset hard to origin/HEAD.
			if err := gr.RunError("reset", "--hard", "origin/HEAD"); err != nil {
				return fmt.Errorf("reset: %w", err)
			}
			return nil
		}
		// Not a repo: remove and clone fresh.
		if err := os.RemoveAll(destDir); err != nil {
			return fmt.Errorf("remove existing: %w", err)
		}
	}
	// Clone fresh.
	cmd := exec.Command("git", "clone", repoURL, destDir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("clone: %v: %s", err, out)
	}
	return nil
}

// BareRepoName derives a safe on-disk name for a repo's bare cache from its
// URL, so multiple allowlisted repos can share one bareDir without
// cross-polluting the same cache (D30).
func BareRepoName(repoURL string) string {
	name := repoURL
	name = strings.TrimPrefix(name, "https://")
	name = strings.TrimPrefix(name, "http://")
	name = strings.TrimPrefix(name, "git@")
	name = strings.ReplaceAll(name, ":", "/")
	name = strings.TrimSuffix(name, ".git")
	return strings.Trim(name, "/")
}

// ensureBareRepoInitialised creates and configures the bare repo for repoURL
// under bareDir, if it is not one yet. An existing but empty directory
// (e.g. pre-created by MkdirAll) is initialised, not skipped.
func ensureBareRepoInitialised(bareDir, repoURL string) error {
	gr := NewGitRunner(bareDir)
	out, err := gr.Run("rev-parse", "--is-bare-repository")
	if err == nil && strings.TrimSpace(out) == "true" {
		return nil
	}
	if err := os.MkdirAll(bareDir, 0o755); err != nil {
		return fmt.Errorf("mkdir bare: %w", err)
	}
	if err := exec.Command("git", "init", "--bare", bareDir).Run(); err != nil {
		return fmt.Errorf("init bare: %w", err)
	}
	if err := gr.SetRemoteOrigin(repoURL); err != nil {
		return fmt.Errorf("set origin: %w", err)
	}
	return nil
}

// ResolveBaseRef resolves the base ref to check out in a worktree.
// An empty branch name means "the repo default" — resolved via
// refs/remotes/origin/HEAD. An explicit branch is returned as
// "origin/<branch>".
func ResolveBaseRef(bareDir, branch string) (string, error) {
	ref := branch
	if ref == "" {
		gr := NewGitRunner(bareDir)
		out, err := gr.Run("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		if err != nil {
			return "", fmt.Errorf("resolve default branch: %w (bare repo may be empty)", err)
		}
		ref = strings.TrimSpace(out)
	} else {
		ref = "origin/" + ref
	}
	return ref, nil
}

// WorktreeAt creates (or reuses) a DETACHED worktree at worktreeDir on the
// bare repo in bareDir, checked out at the base ref (origin/<branch> or the
// resolved default). Detached is deliberate:
//   - "git worktree add <path> HEAD" fails on a fresh bare repo (HEAD is
//     not a valid branch name); a branch name fails a second time because
//     the branch is already used by the first worktree.
//   - Detached checkouts never conflict: any number of attempts can share
//     the same base commit. OnSuccess/OnFailure create the real branches
//     (feature/<ticket_id>-..., debug/<attempt_id>) from HEAD.
//
// The worktree's "origin" remote is the bare repo's origin — i.e. the real
// upstream — because linked worktrees share the bare's remote config.
func WorktreeAt(bareDir, worktreeDir, branch string, repoURL string) error {
	if err := ensureBareRepoInitialised(bareDir, repoURL); err != nil {
		return err
	}
	gr := NewGitRunner(bareDir)
	// Fetch on every prepare, not just first init: a cached base is a stale
	// base. (The worktree is built from the base commit the agent's branch
	// will be based on; a stale base silently re-bases the work.)
	if err := gr.Fetch(); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	ref, err := ResolveBaseRef(bareDir, branch)
	if err != nil {
		return err
	}

	if _, err := os.Stat(worktreeDir); err == nil {
		// Reuse an existing worktree directory (e.g. a retry of the same
		// attempt_id): just re-check out the base.
		wt := NewGitRunner(worktreeDir)
		return wt.RunError("checkout", "--detach", ref)
	}

	cmd := exec.Command("git", "worktree", "add", "--detach", worktreeDir, ref)
	cmd.Dir = bareDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("worktree add: %v: %s", err, out)
	}
	return nil
}

// RemoveWorktree removes the worktree at worktreeDir, running from the
// bare repo so the worktree admin files are pruned (cmd.Dir is required —
// without it git resolves the worktree registry relative to the CWD).
func RemoveWorktree(bareDir, worktreeDir string) error {
	cmd := exec.Command("git", "worktree", "remove", worktreeDir, "--force")
	cmd.Dir = bareDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("worktree remove: %v: %s", err, out)
	}
	// Prune stale worktree registrations left by crashes.
	prune := exec.Command("git", "worktree", "prune")
	prune.Dir = bareDir
	_ = prune.Run()
	return nil
}

// PushToUpstream pushes a local branch to the upstream remote URL directly,
// using the token-embedded URL. It does not modify the worktree's git config
// (no token-bearing remote on disk).
func PushToUpstream(worktreeDir, upstreamURL, token, branch string) error {
	pushURL, err := tokenURL(upstreamURL, token)
	if err != nil {
		return err
	}
	gr := NewGitRunner(worktreeDir)
	out, err := gr.Run("push", pushURL, "HEAD:refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("push: %v: %s", err, out)
	}
	return nil
}