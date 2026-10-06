package stubs

import (
	"strconv"
	"time"

	"github.com/google/uuid"
)

// P1Issue creates a fixture for a P1 (new issue) candidate.
func P1Issue(number int, title, body string, createdAt, updatedAt time.Time) *GitHubIssue {
	if number == 0 {
		number = 412
	}
	if createdAt.IsZero() {
		createdAt = time.Date(2026, 9, 29, 9, 58, 0, 0, time.UTC)
	}
	if updatedAt.IsZero() {
		updatedAt = createdAt
	}
	return &GitHubIssue{
		Number:    number,
		Title:     title,
		Body:      body,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
		Labels:    []GitHubLabel{},
		User:      GitHubUser{Type: "User"},
		HTMLURL:   "https://github.com/acme/widgets/issues/" + strconv.Itoa(number),
	}
}

// P1IssueWithLabels creates a P1 issue with specific labels.
func P1IssueWithLabels(number int, title, body string, labels []GitHubLabel, createdAt time.Time) *GitHubIssue {
	issue := P1Issue(number, title, body, createdAt, createdAt)
	issue.Labels = labels
	return issue
}

// P2Issue creates a fixture for a P2 (idle-ready labeled) candidate.
func P2Issue(number int, title, body string, createdAt time.Time) *GitHubIssue {
	issue := P1Issue(number, title, body, createdAt, createdAt)
	issue.Labels = []GitHubLabel{LabelIdleReady}
	return issue
}

// P0Issue creates a fixture for a P0 (idle-hotfix labeled) candidate.
func P0Issue(number int, title, body string, createdAt time.Time) *GitHubIssue {
	issue := P1Issue(number, title, body, createdAt, createdAt)
	issue.Labels = []GitHubLabel{LabelIdleHotfix}
	return issue
}

// RedoIssue creates a fixture for a redefinition (idle-redo labeled) candidate.
func RedoIssue(number int, title, body string, createdAt time.Time) *GitHubIssue {
	issue := P1Issue(number, title, body, createdAt, createdAt)
	issue.Labels = []GitHubLabel{LabelIdleRedo}
	return issue
}

// EscalatedIssue creates a fixture for an escalated issue with idle-needs-human label.
func EscalatedIssue(number int, title, body string, createdAt time.Time) *GitHubIssue {
	issue := P1Issue(number, title, body, createdAt, createdAt)
	issue.Labels = []GitHubLabel{LabelIdleNeedsHuman}
	return issue
}

// BotIssue creates a fixture for a bot-authored issue (should be excluded from P1).
func BotIssue(number int, title, body string, createdAt time.Time) *GitHubIssue {
	issue := P1Issue(number, title, body, createdAt, createdAt)
	issue.User.Type = "Bot"
	return issue
}

// PRIssue creates a fixture for a pull request (should be filtered out).
func PRIssue(number int, title, body string, createdAt time.Time) *GitHubIssue {
	issue := P1Issue(number, title, body, createdAt, createdAt)
	issue.PullRequest = &struct{}{}
	return issue
}

// P1RunResponse creates a terminal run response for a P1 plan run with comment artifact.
func P1RunResponse(runID string, commentBody string) *HarnessRun {
	now := time.Now().UTC()
	return &HarnessRun{
		ID:     runID,
		Status: "terminal",
		Outcome: &HarnessOutcome{
			State:   "completed",
			Code:    nil,
			Message: "",
		},
		Artifacts: []HarnessArtifact{
			{
				Kind: "comment",
				URI:  "https://github.com/acme/widgets/issues/412#issuecomment-" + uuid.New().String()[:8],
				Body: commentBody,
			},
			{
				Kind: "log",
				URI:  "artifact-store/" + runID + "/run.log",
			},
		},
		LastActivityAt: now,
	}
}

// P2RunResponse creates a terminal run response for a P2 do run with PR and feature branch artifacts.
func P2RunResponse(runID string, prNumber int, branchName string) *HarnessRun {
	now := time.Now().UTC()
	branchPurpose := "feature"
	return &HarnessRun{
		ID:     runID,
		Status: "terminal",
		Outcome: &HarnessOutcome{
			State:   "completed",
			Code:    nil,
			Message: "",
		},
		Artifacts: []HarnessArtifact{
			{
				Kind: "pull_request",
				URI:  "https://github.com/acme/widgets/pull/" + strconv.Itoa(prNumber),
			},
			{
				Kind:          "branch",
				BranchPurpose: &branchPurpose,
				URI:           "https://github.com/acme/widgets/tree/" + branchName,
			},
			{
				Kind: "log",
				URI:  "artifact-store/" + runID + "/run.log",
			},
		},
		LastActivityAt: now,
	}
}

// EscalatedRunResponse creates a terminal run response for an escalated run with debug branch.
func EscalatedRunResponse(runID, debugBranch string) *HarnessRun {
	now := time.Now().UTC()
	branchPurpose := "debug"
	code := "agent_error"
	return &HarnessRun{
		ID:     runID,
		Status: "terminal",
		Outcome: &HarnessOutcome{
			State:   "failed",
			Code:    &code,
			Message: "Agent failed to complete the task",
		},
		Artifacts: []HarnessArtifact{
			{
				Kind:          "branch",
				BranchPurpose: &branchPurpose,
				URI:           "https://github.com/acme/widgets/tree/" + debugBranch,
			},
			{
				Kind: "log",
				URI:  "artifact-store/" + runID + "/partial.log",
			},
		},
		LastActivityAt: now,
	}
}

// TimeoutRunResponse creates a terminal run response for a timed out run.
func TimeoutRunResponse(runID string) *HarnessRun {
	now := time.Now().UTC()
	return &HarnessRun{
		ID:     runID,
		Status: "terminal",
		Outcome: &HarnessOutcome{
			State:   "timed_out",
			Code:    nil,
			Message: "Run exceeded timeout",
		},
		Artifacts: []HarnessArtifact{
			{
				Kind: "log",
				URI:  "artifact-store/" + runID + "/run.log",
			},
		},
		LastActivityAt: now,
	}
}

// PreemptedRunResponse creates a terminal run response for a preempted (cancelled) run.
func PreemptedRunResponse(runID string) *HarnessRun {
	now := time.Now().UTC()
	return &HarnessRun{
		ID:     runID,
		Status: "terminal",
		Outcome: &HarnessOutcome{
			State:   "cancelled",
			Code:    nil,
			Message: "aborted by client",
		},
		Artifacts: []HarnessArtifact{
			{
				Kind: "log",
				URI:  "artifact-store/" + runID + "/partial.log",
			},
		},
		LastActivityAt: now,
	}
}

// SweepRunResponse creates a terminal run response for a P3 sweep run with report artifact.
func SweepRunResponse(runID, reportURI string) *HarnessRun {
	now := time.Now().UTC()
	return &HarnessRun{
		ID:     runID,
		Status: "terminal",
		Outcome: &HarnessOutcome{
			State:   "completed",
			Code:    nil,
			Message: "",
		},
		Artifacts: []HarnessArtifact{
			{
				Kind: "report",
				URI:  reportURI,
			},
			{
				Kind: "log",
				URI:  "artifact-store/" + runID + "/run.log",
			},
		},
		LastActivityAt: now,
	}
}

// RepositoryInfoForTest creates a test repository info.
func RepositoryInfoForTest() RepositoryInfo {
	return RepositoryInfo{
		ID:      "repo-1",
		Name:    "acme/widgets",
		URL:     "https://github.com/acme/widgets.git",
		Tracker: "github",
	}
}

// TicketInfoForTest creates a test ticket info.
func TicketInfoForTest(externalID string) *TicketInfo {
	return &TicketInfo{
		Tracker:    "github",
		ExternalID: externalID,
		URL:        "https://github.com/acme/widgets/issues/" + externalID,
	}
}
