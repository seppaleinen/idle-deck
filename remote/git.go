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

// PushBranch pushes the given branch to origin and sets the upstream.
// If force is true, it uses --force-with-lease.
func (g *GitRunner) PushBranch(branch string, force bool) error {
	args := []string{"push", "origin", branch}
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

// WorktreeAt returns the absolute path of a worktree linked to the bare repo at bareDir,
// checking out the given branch. If the worktree already exists, it is reused.
// The caller is responsible for removing the worktree when done (via git worktree remove).
func WorktreeAt(bareDir, worktreeDir, branch string, repoURL string) error {
	// Ensure the bare repo exists.
	if _, err := os.Stat(bareDir); err != nil {
		if err := os.MkdirAll(bareDir, 0o755); err != nil {
			return fmt.Errorf("mkdir bare: %w", err)
		}
		// Initialize as a bare repo.
		if err := exec.Command("git", "init", "--bare", bareDir).Run(); err != nil {
			return fmt.Errorf("init bare: %w", err)
		}
		gr := NewGitRunner(bareDir)
		if err := gr.SetRemoteOrigin(repoURL); err != nil {
			return fmt.Errorf("set origin: %w", err)
		}
		if err := gr.Fetch(); err != nil {
			return fmt.Errorf("fetch: %w", err)
		}
	}
	// If the worktree already exists, just check out the branch.
	if _, err := os.Stat(worktreeDir); err == nil {
		wt := NewGitRunner(worktreeDir)
		if err := wt.SetRemoteOrigin(bareDir); err != nil {
			return fmt.Errorf("set origin: %w", err)
		}
		return wt.CheckoutBranch(branch, "")
	}
	// Add the worktree linked to the bare repo.
	if err := exec.Command("git", "worktree", "add", worktreeDir, branch).Run(); err != nil {
		return fmt.Errorf("worktree add: %w", err)
	}
	return nil
}

// RemoveWorktree removes the worktree at the given path and prunes it from the bare repo.
func RemoveWorktree(worktreeDir string) error {
	if err := exec.Command("git", "worktree", "remove", worktreeDir, "--force").Run(); err != nil {
		return fmt.Errorf("worktree remove: %w", err)
	}
	return nil
}