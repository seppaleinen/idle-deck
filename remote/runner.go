// Package remote is the remote execution service: the server that sits between
// idle-deck and an agent CLI, implementing the locked wire contract
// (docs/architecture/remote-contract.md, D27).
//
// idle-deck talks to this service over HTTP; it never touches the workspace,
// never names a model, and never runs git (D26, D40). This service owns the
// workspace (D27): clone, checkout, branch, PR, cleanup.
package remote

import "context"

// Runner is the agent-execution seam. The server is agnostic to which agent
// runs a task; it only knows how to start, abort, and read the result.
//
// The first implementation is the ACP adapter (opencode). A proprietary recipe
// (claude, codex, cursor) implements the same interface — that is why the
// contract is one skeleton and the launch recipe is a table.
type Runner interface {
	// Start begins execution of a run in the given workspace and returns a
	// runner-local session id. The server enforces timeout_seconds itself (D17).
	Start(ctx context.Context, ws Workspace, req StartRequest) (SessionID, error)

	// Abort cancels a running session and returns only when the agent has
	// confirmed cancellation (or the session was already terminal).
	Abort(ctx context.Context, id SessionID) error

	// Result blocks until the session is terminal and returns its outcome and
	// typed artifacts (I4, I5).
	Result(ctx context.Context, id SessionID) (RunResult, error)
}

// SessionID is a runner-local handle for a running session.
type SessionID string

// StartRequest carries everything the runner needs to execute a task.
// It mirrors harness.RunRequest over the wire: a role, never a model (D6, I11).
type StartRequest struct {
	Role     string // plan | do | sweep
	Prompt   string
	Budget   int       // generation tokens — ceiling the runner respects (D37)
	Findings []Finding // populated by the sweep agent (D43)
}

// RunResult is the terminal result of a session, mirroring harness.RunResult.
type RunResult struct {
	Outcome   AttemptOutcome
	Artifacts []Artifact
	Usage     *Usage         // token consumption, nil if the runner does not report usage
}

// Usage is token consumption for one run, as reported by the runner.
// Mirrors harness.Usage.
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Artifact is a typed artifact produced by a run (I4, I5).
type Artifact struct {
	Kind          string  // comment | pull_request | branch | report | log
	BranchPurpose *string // feature | debug (only for kind=branch)
	URI           string
	Body          string    // only for kind=comment
	Findings      []Finding // for kind=report (D43)
}

// Finding is a structured finding from a P3 sweep (D43).
type Finding struct {
	Title       string
	Description string
	Category    string
	Severity    string
}

// AttemptOutcome is the terminal outcome of a run.
type AttemptOutcome string

const (
	OutcomeSucceeded           AttemptOutcome = "succeeded"
	OutcomeRetryableFailure    AttemptOutcome = "retryable_failure"
	OutcomeNonRetryableFailure AttemptOutcome = "non_retryable_failure"
	OutcomeTimeout             AttemptOutcome = "timeout"
	OutcomePreempted           AttemptOutcome = "preempted"
)

// Workspace is the checkout the runner executes in. The server owns it (D27):
// it provisions the worktree, and it tears it down per the lifecycle.
type Workspace struct {
	RepoURL  string // allowlisted clone URL (I1 already gated this at ingestion)
	TicketID string // for feature/<ticket_id>-<slug> branch names (D15)
	Branch   string // base branch to check out, empty = default
	Dir      string // absolute path of the worktree the runner executes in
}
