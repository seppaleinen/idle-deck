package harness

import "time"

// RunID is a session identifier for a remote run, used as the key for Abort/Result.
type RunID string

// RunRequest is the request sent to the remote harness to start a run.
// It carries a role, never a model name (D6, I11).
type RunRequest struct {
	Role      string // plan | do | sweep
	Prompt    string
	Budget    int           // generation tokens — ceiling the harness respects (D37)
	Timeout   time.Duration // server enforces this (D17)
	RepoRef   Repository    // identity + allowlisted URL
	TicketRef TicketRef     // may be nil (sweep tier, D44)
	AttemptID string        // idempotency key = TaskAttempt.id
	Findings  []Finding     // populated by the sweep agent; forwarded to TrackerSink.OpenIssue
}

// RunResult is the result returned by Harness.Result when a run is terminal.
type RunResult struct {
	Outcome   AttemptOutcome // succeeded | retryable_failure | non_retryable_failure | timeout | preempted
	Artifacts []Artifact     // pull_request | branch | comment | report | log
	Usage     *Usage         // token consumption, nil if the remote does not report usage
}

// MaxTokensPerRun is the maximum token count accepted per run. Any usage
// reporting more than this is rejected as a DoS attempt (issue #69).
const MaxTokensPerRun = 10_000_000

// Usage is token consumption for one run, as reported by the remote.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Valid returns true if the usage values are sane. It rejects negative
// values, totals exceeding MaxTokensPerRun, and totals that don't match
// the sum of prompt + completion (allowing a small slack for remote
// accounting differences).
func (u *Usage) Valid() bool {
	if u == nil {
		return false
	}
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 {
		return false
	}
	if u.TotalTokens > MaxTokensPerRun {
		return false
	}
	// Allow up to 10% slack for remote accounting differences (e.g. cached tokens).
	sum := u.PromptTokens + u.CompletionTokens
	if u.TotalTokens > sum+sum/10 {
		return false
	}
	if u.TotalTokens < sum-sum/10 {
		return false
	}
	return true
}

// Artifact is a typed artifact produced by a remote run.
// Kind discriminates the type; BranchPurpose is only meaningful when Kind=branch.
// Findings is populated for kind=report artifacts (P3 sweep structured findings).
type Artifact struct {
	Kind          string    `json:"kind"`           // comment | pull_request | branch | report | log
	BranchPurpose *string   `json:"branch_purpose"` // feature | debug (only for kind=branch)
	URI           string    `json:"uri"`
	Body          string    `json:"body,omitempty"`     // only for kind=comment
	Findings      []Finding `json:"findings,omitempty"` // for kind=report
}

// Finding is a structured finding from a P3 sweep, describing a code health
// marker or other concern. The sweep agent collects these; idle-deck files one
// issue per finding via TrackerSink.OpenIssue.
type Finding struct {
	Title       string
	Description string
	Category    string // e.g. "stale-TODO", "coverage-gap", "dependency-drift"
	Severity    string // optional: "low" | "medium" | "high"
}

// TicketRef is a tracker's own identity for a work item.
type TicketRef struct {
	Tracker      string
	RepositoryID string
	ExternalID   string
	URL          string
}

// Repository is the identity + allowlisted URL for a repo.
type Repository struct {
	ID      string
	Name    string
	URL     string
	Tracker string
}

// AttemptOutcome is the terminal outcome of a TaskAttempt.
// (ontology AttemptOutcome value set).
type AttemptOutcome string

const (
	OutcomeSucceeded           AttemptOutcome = "succeeded"
	OutcomeRetryableFailure    AttemptOutcome = "retryable_failure"
	OutcomeNonRetryableFailure AttemptOutcome = "non_retryable_failure"
	OutcomeTimeout             AttemptOutcome = "timeout"
	OutcomePreempted           AttemptOutcome = "preempted"
)

// Wire response types — these are the JSON shapes sent over the wire.

// usageResponse is the usage shape in a terminal run response.
type usageResponse struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// runResponse is the response from POST /v1/runs and GET /v1/runs/{id}.
type runResponse struct {
	Status    string           `json:"status"`
	RunID     string           `json:"run_id,omitempty"`
	Outcome   *outcomeResponse `json:"outcome,omitempty"`
	Artifacts []Artifact       `json:"artifacts,omitempty"`
	Usage     *usageResponse   `json:"usage,omitempty"`
}

// outcomeResponse is the nested outcome shape within a run response.
type outcomeResponse struct {
	State   string  `json:"state"` // completed | failed | timed_out | cancelled
	Code    *string `json:"code"`  // set when state = failed
	Message string  `json:"message"`
}

// artifactResponse is the shape for a single artifact in a run response.
type artifactResponse struct {
	Kind          string  `json:"kind"`           // comment | pull_request | branch | report | log
	BranchPurpose *string `json:"branch_purpose"` // feature | debug (only for kind=branch)
	URI           string  `json:"uri"`
	Body          string  `json:"body,omitempty"` // only for kind=comment
}

// errorBody is the error shape returned by the wire.
type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// /health response shape lives in the remote package
// (remote/healthResponse), and the sole consumer is idle.HarnessIdlePolicy
// (idle/idle.go). The harness package carries no health type: D31 defines one
// probe, one producer, and one consumer, and duplicating the shape here is
// what let three implementations drift apart. (Architecture audit H6.)
