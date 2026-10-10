package remote

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

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
	}
}

// PrepareWorkspace prepares the workspace for an agent to run in.
// It clones the repo (if needed) and creates a worktree checking out the base branch.
// It returns the Workspace for the agent to run in.
// The attemptID is used to name the worktree directory to avoid collisions.
func (w *WorkspaceManager) PrepareWorkspace(ws Workspace, attemptID string) (Workspace, error) {
	// Determine the base branch to check out.
	baseBranch := ws.Branch
	if baseBranch == "" {
		baseBranch = "HEAD" // default to the default branch
	}

	// Create a unique worktree directory for this attempt.
	// We use attemptID to avoid collisions between retries of the same task.
	worktreeDir := filepath.Join(w.BaseDir, "worktree-"+attemptID)

	// Ensure the base directories exist.
	if err := os.MkdirAll(w.BareDir, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create bare dir: %w", err)
	}
	if err := os.MkdirAll(w.BaseDir, 0o755); err != nil {
		return Workspace{}, fmt.Errorf("create base dir: %w", err)
	}

	// Use the shared bare repo cache and create a worktree.
	if err := WorktreeAt(w.BareDir, worktreeDir, baseBranch, ws.RepoURL); err != nil {
		return Workspace{}, fmt.Errorf("worktree: %w", err)
	}

	// Return the Workspace with the directory set.
	return Workspace{
		RepoURL:   ws.RepoURL,
		TicketID:  ws.TicketID,
		Branch:    ws.Branch,
		Dir:       worktreeDir,
	}, nil
}

// OnSuccess is called after the agent has completed successfully.
// It takes the worktree where the agent worked and creates the success artifact:
//   - Stages all changes in the worktree
//   - Commits with a standard message
//   - Creates a feature branch: feature/<ticket_id>-<slug>
//   - Pushes the branch to origin
//   - Opens a Draft PR targeting the base branch
//   - Returns a pull_request artifact with the PR URL
func (w *WorkspaceManager) OnSuccess(worktree Workspace, ws Workspace, attemptID string) (Artifact, error) {
	if w.GitHubToken == "" {
		return Artifact{}, errors.New("gitHubToken not configured")
	}

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

	// Push the branch to origin and set upstream.
	// We need to auth with our token for git operations.
	// We'll do this by embedding the token in the URL for the push operation.
	//
	// First, get the current origin URL.
	originURL, err := gr.Run("remote", "get-url", "origin")
	if err != nil {
		return Artifact{}, fmt.Errorf("get origin: %w", err)
	}
	originURL = strings.TrimSpace(originURL)

	// Create a new remote with the token embedded.
	// We only support HTTPS URLs for now.
	var pushURL string
	if strings.HasPrefix(originURL, "https://") {
		// Insert the token after https://
		pushURL = "https://" + w.GitHubToken + "@" + strings.TrimPrefix(originURL, "https://")
	} else if strings.HasPrefix(originURL, "git@") {
		// SSH URL - we'd need to use ssh key auth, which is more complex.
		// For v1, we'll only support HTTPS.
		return Artifact{}, errors.New("SSH URLs not supported; use HTTPS")
	} else {
		return Artifact{}, fmt.Errorf("unsupported protocol in origin URL: %s", originURL)
	}

	// Add a temporary remote for pushing.
	if err := gr.RunError("remote", "add", "origin-with-token", pushURL); err != nil {
		return Artifact{}, fmt.Errorf("add token remote: %w", err)
	}
	// Push to the token remote.
	if err := gr.PushBranch(branchName, false); err != nil {
		_ = gr.RunError("remote", "remove", "origin-with-token") // cleanup
		return Artifact{}, fmt.Errorf("push branch: %w", err)
	}
	// Remove the temporary remote.
	if err := gr.RunError("remote", "remove", "origin-with-token"); err != nil {
		// Non-fatal - we'll leave it and clean up later.
	}

	// Now open a Draft PR using the GitHub CLI.
	// We need to pass the token via GITHUB_TOKEN env.
	// We'll use `gh pr create` to create a Draft PR.
	//
	// First, get the repo owner/name from the origin URL.
	var owner, repo string
	if strings.HasPrefix(originURL, "https://") {
		// Format: https://github.com/owner/repo.git
		withoutPrefix := strings.TrimPrefix(originURL, "https://")
		withoutPrefix = strings.TrimSuffix(withoutPrefix, ".git")
		parts := strings.SplitN(withoutPrefix, "/", 2)
		if len(parts) != 2 {
			return Artifact{}, fmt.Errorf("invalid origin URL: %s", originURL)
		}
		owner, repo = parts[0], parts[1]
	} else {
		return Artifact{}, errors.New("cannot parse origin URL for PR creation")
	}

	// Set up the environment for gh.
	env := append([]string{}, os.Environ()...)
	env = append(env, "GITHUB_TOKEN="+w.GitHubToken)

	// Create the PR title and body.
	title := fmt.Sprintf("feat: implement %s", ws.TicketID)
	if ws.TicketID == "" {
		title = fmt.Sprintf("feat: attempt %s", attemptID)
	}
	body := fmt.Sprintf("This PR was created automatically by idle-deck.\n\nTicket: %s\nAttempt: %s\nCommit: %s", ws.TicketID, attemptID, commitHash)

	// Run gh pr create.
	// We'll use --draft to create a Draft PR.
	// We'll specify --title and --body via args or stdin.
	// Using stdin is safer for special characters.
	prCmd := exec.Command("gh", "pr", "create",
		"--repo", owner+"/"+repo,
		"--title", title,
		"--body", body,
		"--head", branchName,
		"--base", ws.Branch, // target base branch
		"--draft",
	)
	prCmd.Dir = worktree.Dir
	prCmd.Env = env
	out, err := prCmd.CombinedOutput()
	if err != nil {
		return Artifact{}, fmt.Errorf("gh pr create: %v: %s", err, out)
	}
	// The output is the PR URL.
	prURL := strings.TrimSpace(string(out))

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
//   - Pushes the branch to origin
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

	// Push the debug branch to origin (similar to OnSuccess).
	// Get the origin URL.
	originURL, err := gr.Run("remote", "get-url", "origin")
	if err != nil {
		return Artifact{}, fmt.Errorf("get origin: %w", err)
	}
	originURL = strings.TrimSpace(originURL)

	// Create a new remote with the token embedded.
	var pushURL string
	if strings.HasPrefix(originURL, "https://") {
		// Insert the token after https://
		pushURL = "https://" + w.GitHubToken + "@" + strings.TrimPrefix(originURL, "https://")
	} else if strings.HasPrefix(originURL, "git@") {
		// SSH URL - we'd need to use ssh key auth, which is more complex.
		// For v1, we'll only support HTTPS.
		return Artifact{}, errors.New("SSH URLs not supported; use HTTPS")
	} else {
		return Artifact{}, fmt.Errorf("unsupported protocol in origin URL: %s", originURL)
	}

	// Add a temporary remote for pushing.
	if err := gr.RunError("remote", "add", "origin-with-token", pushURL); err != nil {
		return Artifact{}, fmt.Errorf("add token remote: %w", err)
	}
	// Push to the token remote.
	if err := gr.PushBranch(branchName, false); err != nil {
		_ = gr.RunError("remote", "remove", "origin-with-token") // cleanup
		return Artifact{}, fmt.Errorf("push debug branch: %w", err)
	}
	// Remove the temporary remote.
	if err := gr.RunError("remote", "remove", "origin-with-token"); err != nil {
		// Non-fatal - we'll leave it and clean up later.
	}

	// Get the remote URL to construct the branch URL.
	originURL, err = gr.Run("remote", "get-url", "origin")
	if err != nil {
		return Artifact{}, fmt.Errorf("get origin: %w", err)
	}
	originURL = strings.TrimSpace(originURL)

	// Convert to a web URL for the branch.
	var branchURL string
	if strings.HasPrefix(originURL, "https://") {
		// https://github.com/owner/repo.git -> https://github.com/owner/repo/tree/branchName
		withoutPrefix := strings.TrimPrefix(originURL, "https://")
		withoutPrefix = strings.TrimSuffix(withoutPrefix, ".git")
		branchURL = "https://" + withoutPrefix + "/tree/" + branchName
	} else if strings.HasPrefix(originURL, "git@") {
		// git@github.com:owner/repo.git -> https://github.com/owner/repo/tree/branchName
		withoutPrefix := strings.TrimPrefix(originURL, "git@")
		withoutPrefix = strings.TrimSuffix(withoutPrefix, ":")
		withoutPrefix = strings.TrimSuffix(withoutPrefix, ".git")
		branchURL = "https://" + withoutPrefix + "/tree/" + branchName
	} else {
		return Artifact{}, fmt.Errorf("unsupported protocol in origin URL: %s", originURL)
	}

	// Return the branch artifact.
	return Artifact{
		Kind:          "branch",
		BranchPurpose: stringPtr("debug"),
		URI:           branchURL,
	}, nil
}