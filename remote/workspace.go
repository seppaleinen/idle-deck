package remote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// PRCreator opens a Draft PR against the pushed branch. It is a seam so the
// workspace manager can be tested without a real GitHub: the production
// implementation shells out to `gh pr create`, and tests inject a stub.
type PRCreator interface {
	CreatePR(ctx context.Context, owner, repo, head, base, title, body string) (string, error)
}

// ghPRCreator shells out to `gh pr create --draft` using the configured
// GITHUB_TOKEN. It is the production implementation of PRCreator.
type ghPRCreator struct {
	token string
}

func (g *ghPRCreator) CreatePR(ctx context.Context, owner, repo, head, base, title, body string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "create",
		"--repo", owner+"/"+repo,
		"--title", title,
		"--body", body,
		"--head", head,
		"--base", base,
		"--draft",
	)
	env := append([]string{}, os.Environ()...)
	env = append(env, "GITHUB_TOKEN="+g.token)
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr create: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// WorkspaceManager handles workspace operations for the remote server.
// It provisions worktrees, manages branches, and creates artifacts.
type WorkspaceManager struct {
	// BareDir is the directory where bare repositories are cached.
	// This acts as a shared object store to avoid re-cloning every run.
	BareDir string
	// GitHubToken is the token used for git operations (push, PR creation).
	// This is the repo-scoped token that the remote holds (contract §1).
	GitHubToken string
	// BaseDir is the directory where worktrees are created.
	BaseDir string
	// PR is the PR-creation seam. Nil falls back to ghPRCreator.
	PR PRCreator
}

// NewWorkspaceManager creates a new workspace manager.
// bareDir: where bare repos are cached (e.g. "/tmp/idle-deck-bare")
// baseDir: where worktrees are created (e.g. "/tmp/idle-deck-work")
// githubToken: the repo-scoped token for git operations
func NewWorkspaceManager(bareDir, baseDir, githubToken string) *WorkspaceManager {
	return &WorkspaceManager{
		BareDir:       bareDir,
		BaseDir:       baseDir,
		GitHubToken:   githubToken,
		PR:            &ghPRCreator{token: githubToken},
	}
}

// tokenURL embeds the token into an HTTPS remote URL. SSH URLs are not
// supported by the MVP (D7: one adapter, HTTPS only). Local paths (used in
// tests) pass through unchanged — a local filesystem clone needs no auth.
func tokenURL(rawURL, token string) (string, error) {
	if strings.HasPrefix(rawURL, "https://") {
		return "https://" + token + "@" + strings.TrimPrefix(rawURL, "https://"), nil
	}
	if strings.HasPrefix(rawURL, "git@") {
		return "", errors.New("SSH URLs not supported; use HTTPS")
	}
	if strings.HasPrefix(rawURL, "/") {
		return rawURL, nil
	}
	return "", fmt.Errorf("unsupported protocol in remote URL: %s", rawURL)
}

// PrepareWorkspace prepares the workspace for an agent to run in.
// It creates a detached worktree at the base ref (default branch or explicit).
// The worktree's "origin" is the real upstream (shared from the bare cache),
// so no separate remote setup is needed for pushing.
// The attemptID is used to name the worktree directory to avoid collisions.
func (w *WorkspaceManager) PrepareWorkspace(ws Workspace, attemptID string) (Workspace, error) {
	// Per-repo bare cache (D30: multi-repo, no cross-pollution).
	repoCache := filepath.Join(w.BareDir, BareRepoName(ws.RepoURL))
	worktreeDir := filepath.Join(w.BaseDir, "worktree-"+attemptID)

	// Ensure the base directories exist.
	if err := os.MkdirAll(repoCache, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create bare dir: %w", err)
	}
	if err := os.MkdirAll(w.BaseDir, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create base dir: %w", err)
	}

	// Create or reuse a detached worktree at the base ref.
	if err := WorktreeAt(repoCache, worktreeDir, ws.Branch, ws.RepoURL); err != nil {
		return Workspace{}, fmt.Errorf("worktree: %w", err)
	}

	// Resolve the base branch for PR targeting. When ws.Branch is empty,
	// WorktreeAt resolved the default and checked out origin/<default>.
	// We need the short branch name (e.g. "main"), not "origin/main".
	returnBranch := ws.Branch
	if returnBranch == "" {
		gr := NewGitRunner(worktreeDir)
		// Detached HEAD -> symbolic-ref --short refs/remotes/origin/HEAD
		// yields "origin/main"; strip the prefix to get "main".
		out, err := gr.Run("symbolic-ref", "--short", "refs/remotes/origin/HEAD")
		if err != nil {
			return Workspace{}, fmt.Errorf("resolve default branch: %w", err)
		}
		returnBranch = strings.TrimSpace(strings.TrimPrefix(out, "origin/"))
	}

	return Workspace{
		RepoURL:   ws.RepoURL,
		TicketID:  ws.TicketID,
		Branch:    returnBranch,
		Dir:       worktreeDir,
	}, nil
}

// OnSuccess is called after the agent has completed successfully.
// It takes the worktree where the agent worked and creates the success artifact:
//   - Stages all changes in the worktree
//   - Commits with a standard message
//   - Creates a feature branch: feature/<ticket_id>-<slug>
//   - Pushes the branch to the upstream GitHub remote
//   - Opens a Draft PR targeting the base branch
//   - Returns a pull_request artifact with the PR URL
func (w *WorkspaceManager) OnSuccess(worktree Workspace, ws Workspace, attemptID string) (Artifact, error) {
	if w.GitHubToken == "" {
		return Artifact{}, errors.New("gitHubToken not configured")
	}

	// The PR targets the base branch the worktree was checked out from.
	// PrepareWorkspace resolves an empty Branch to the repo's actual
	// default branch, so ws.Branch is always a real branch name here.
	baseBranch := ws.Branch

	gr := NewGitRunner(worktree.Dir)
	// Set up git to use our token for auth.
	// We'll do this by setting the GITHUB_TOKEN env var for git commands.
	// Alternatively, we could embed the token in the URL, but env is safer.

	// Check if there are any changes to commit.
	if ok, err := gr.IsClean(); err != nil {
		return Artifact{}, fmt.Errorf("check clean: %w", err)
	} else if ok {
		// No changes - still create an empty commit? The agent might have
		// not modified anything but still succeeded.
		// We'll create a commit anyway to have something to push.
		if err := gr.Commit("chore: no changes"); err != nil {
			return Artifact{}, fmt.Errorf("commit empty: %w", err)
		}
	} else {
		// Stage and commit all changes.
		if err := gr.AddAll(); err != nil {
			return Artifact{}, fmt.Errorf("add all: %w", err)
		}
		// Create a commit message from the ticket ID and attempt ID.
		commitMsg := fmt.Sprintf("chore: implement %s", ws.TicketID)
		if ws.TicketID == "" {
			commitMsg = fmt.Sprintf("chore: attempt %s", attemptID)
		}
		if err := gr.Commit(commitMsg); err != nil {
			return Artifact{}, fmt.Errorf("commit: %w", err)
		}
	}

	// Get the current commit hash for the branch name.
	commitHash, err := gr.GetCommitHash()
	if err != nil {
		return Artifact{}, fmt.Errorf("get commit hash: %w", err)
	}
	// Use first 8 chars of hash for uniqueness in branch name.
	hashShort := commitHash[:8]

	// Create the feature branch name.
	// feature/<ticket_id>-<slug> where slug is derived from the prompt.
	// For simplicity, we'll use the attemptID and hash.
	branchName := fmt.Sprintf("feature/%s-%s", ws.TicketID, hashShort)
	if ws.TicketID == "" {
		branchName = fmt.Sprintf("feature/%s-%s", attemptID, hashShort)
	}

	// Create and check out the feature branch.
	if err := gr.CreateBranchFromHead(branchName); err != nil {
		return Artifact{}, fmt.Errorf("create branch: %w", err)
	}

	// Push the feature branch to the upstream repo. The worktree's "origin"
	// is already the real upstream (shared from bare cache), so we push
	// directly via the token-embedded URL.
	if err := PushToUpstream(worktree.Dir, worktree.RepoURL, w.GitHubToken, branchName); err != nil {
		return Artifact{}, fmt.Errorf("push branch: %w", err)
	}

	// Open a Draft PR via the PRCreator seam (production: gh pr create).
	pr := w.PR
	if pr == nil {
		pr = &ghPRCreator{token: w.GitHubToken}
	}
	owner, repo, err := parseOwnerRepo(worktree.RepoURL)
	if err != nil {
		return Artifact{}, err
	}
	title := fmt.Sprintf("feat: implement %s", ws.TicketID)
	if ws.TicketID == "" {
		title = fmt.Sprintf("feat: attempt %s", attemptID)
	}
	body := fmt.Sprintf("This PR was created automatically by idle-deck.\n\nTicket: %s\nAttempt: %s\nCommit: %s", ws.TicketID, attemptID, commitHash)
	prURL, err := pr.CreatePR(context.Background(), owner, repo, branchName, baseBranch, title, body)
	if err != nil {
		return Artifact{}, err
	}

	// Return the pull_request artifact.
	return Artifact{
		Kind:  "pull_request",
		URI:   prURL,
		Body:  "", // not used for pull_request
	}, nil
}

// OnFailure is called after the agent has completed with a failure.
// It takes the worktree where the agent worked and creates the failure artifact:
//   - Stages all changes in the worktree
//   - Commits with a standard message
//   - Creates a debug branch: debug/<attemptID>
//   - Pushes the branch to the upstream GitHub remote
//   - Returns a branch artifact with Kind: branch, BranchPurpose: debug, URI: branch URL
func (w *WorkspaceManager) OnFailure(worktree Workspace, ws Workspace, attemptID string) (Artifact, error) {
	if w.GitHubToken == "" {
		return Artifact{}, errors.New("gitHubToken not configured")
	}

	gr := NewGitRunner(worktree.Dir)
	// Stage and commit all changes.
	if err := gr.AddAll(); err != nil {
		return Artifact{}, fmt.Errorf("add all: %w", err)
	}
	// Create a commit message indicating this is a debug commit.
	commitMsg := fmt.Sprintf("chore: debug attempt %s", attemptID)
	if ws.TicketID != "" {
		commitMsg = fmt.Sprintf("chore: debug %s", ws.TicketID)
	}
	if err := gr.Commit(commitMsg); err != nil {
		return Artifact{}, fmt.Errorf("commit: %w", err)
	}

	// Create the debug branch name.
	branchName := fmt.Sprintf("debug/%s", attemptID)

	// Create and check out the debug branch.
	if err := gr.CreateBranchFromHead(branchName); err != nil {
		return Artifact{}, fmt.Errorf("create branch: %w", err)
	}

	// Push the debug branch to the upstream repo.
	if err := PushToUpstream(worktree.Dir, worktree.RepoURL, w.GitHubToken, branchName); err != nil {
		return Artifact{}, fmt.Errorf("push debug branch: %w", err)
	}

	// Construct the branch URL from the upstream repo URL.
	branchURL, err := webBranchURL(worktree.RepoURL, branchName)
	if err != nil {
		return Artifact{}, err
	}

	// Return the branch artifact.
	return Artifact{
		Kind:          "branch",
		BranchPurpose: stringPtr("debug"),
		URI:           branchURL,
	}, nil
}

// parseOwnerRepo extracts owner/repo from an HTTPS or SSH remote URL.
// For local filesystem paths (used in tests) it returns the path as-is,
// so the caller can build a file:// URL rather than a GitHub URL.
func parseOwnerRepo(rawURL string) (string, string, error) {
	if strings.HasPrefix(rawURL, "/") {
		return "local", strings.TrimSuffix(strings.TrimSuffix(rawURL, "/"), "/"), nil
	}
	if strings.HasPrefix(rawURL, "https://") {
		withoutPrefix := strings.TrimPrefix(rawURL, "https://")
		withoutPrefix = strings.TrimSuffix(withoutPrefix, ".git")
		parts := strings.SplitN(withoutPrefix, "/", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid remote URL: %s", rawURL)
		}
		return parts[0], parts[1], nil
	}
	if strings.HasPrefix(rawURL, "git@") {
		// git@github.com:owner/repo.git
		withoutPrefix := strings.TrimPrefix(rawURL, "git@")
		withoutPrefix = strings.TrimSuffix(withoutPrefix, ":")
		withoutPrefix = strings.TrimSuffix(withoutPrefix, ".git")
		parts := strings.SplitN(withoutPrefix, "/", 2)
		if len(parts) != 2 {
			return "", "", fmt.Errorf("invalid SSH remote URL: %s", rawURL)
		}
		return parts[0], parts[1], nil
	}
	return "", "", fmt.Errorf("unsupported protocol in remote URL: %s", rawURL)
}

// webBranchURL converts a remote URL into a URL for the branch.
// For a GitHub remote this is https://github.com/owner/repo/tree/<branch>;
// for a local path it is file://<path>/tree/<branch>.
func webBranchURL(rawURL, branch string) (string, error) {
	owner, repo, err := parseOwnerRepo(rawURL)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rawURL, "/") {
		return "file://" + repo + "/tree/" + branch, nil
	}
	return fmt.Sprintf("https://github.com/%s/%s/tree/%s", owner, repo, branch), nil
}