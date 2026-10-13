package remote

import (
	"time"
)

// Config holds the configuration for the remote server (wrapper).
type Config struct {
	// RoleToModel maps the role from RunRequest to a model name.
	// This is where the wrapper names the model (internally, not in the wire).
	RoleToModel map[string]string
	// GitHubToken is the token used for git operations (push, PR creation).
	// This is the repo-scoped token that the remote holds (contract §1).
	// It must be kept secret and never logged.
	GitHubToken string
	// HTTPTimeout is the timeout for the HTTP server itself.
	HTTPTimeout time.Duration
	// WorktreeTTL is how long to keep worktrees before garbage collection.
	// 0 means never delete.
	WorktreeTTL time.Duration
	// GitHubAPIToken is the token used for GitHub API calls (gh pr create, etc).
	// This could be the same as GitHubToken or different.
	GitHubAPIToken string
	// WorkspaceManager handles workspace operations.
	WorkspaceManager *WorkspaceManager
	// TestUpstreamURL is a real local git repo used by conformance tests as
	// the upstream for PrepareWorkspace. Empty in production — the server
	// never guesses at a URL; every repo comes from the allowlist (D30).
	TestUpstreamURL string
}

// DefaultConfig returns a configuration with sensible defaults.
// The RoleToModel mapping should be customized by the operator.
func DefaultConfig(bareDir, baseDir, gitHubToken string) *Config {
	return &Config{
		RoleToModel: map[string]string{
			"plan":  "opencode/big-pickle",
			"do":    "opencode/big-pickle",
			"sweep": "opencode/big-pickle",
		},
		GitHubToken:   gitHubToken,
		HTTPTimeout:     30 * time.Second,
		WorktreeTTL:     24 * time.Hour,
		GitHubAPIToken:  "", // same as GitHubToken if empty
		WorkspaceManager: NewWorkspaceManager(bareDir, baseDir, gitHubToken),
	}
}