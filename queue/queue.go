package queue

import (
	"context"
	"errors"
)

// TaskTier is the authoritative priority of a Task (D8, D16).
type TaskTier string

const (
	TierP0 TaskTier = "P0"
	TierP1 TaskTier = "P1"
	TierP2 TaskTier = "P2"
	TierP3 TaskTier = "P3"
)

// TaskState is the lifecycle of a Task (ontology TaskState value set).
type TaskState string

const (
	StateQueued    TaskState = "queued"
	StateRunning   TaskState = "running"
	StateSucceeded TaskState = "succeeded"
	StateEscalated TaskState = "escalated"
)

// AttemptOutcome is the terminal outcome of a TaskAttempt
// (ontology AttemptOutcome value set).
type AttemptOutcome string

const (
	OutcomeSucceeded           AttemptOutcome = "succeeded"
	OutcomeRetryableFailure    AttemptOutcome = "retryable_failure"
	OutcomeNonRetryableFailure AttemptOutcome = "non_retryable_failure"
	OutcomeTimeout             AttemptOutcome = "timeout"
	OutcomePreempted           AttemptOutcome = "preempted"
)

// ArtifactKind is the discriminated kind of an Artifact
// (ontology ArtifactKind value set).
type ArtifactKind string

const (
	ArtifactComment     ArtifactKind = "comment"
	ArtifactPullRequest ArtifactKind = "pull_request"
	ArtifactBranch      ArtifactKind = "branch"
	ArtifactReport      ArtifactKind = "report"
	ArtifactLog         ArtifactKind = "log"
)

// BranchPurpose is the intent of a branch artifact (ontology BranchPurpose).
type BranchPurpose string

const (
	BranchFeature BranchPurpose = "feature"
	BranchDebug   BranchPurpose = "debug"
)

// TaskRole is derived from TaskTier and is never stored (I11).
type TaskRole string

const (
	RolePlan   TaskRole = "plan"
	RoleDo     TaskRole = "do"
	RoleSweep  TaskRole = "sweep"
)

// RoleForTier maps a TaskTier to its TaskRole (I11, ontology table).
func RoleForTier(tier TaskTier) TaskRole {
	switch tier {
	case TierP0:
		return RoleDo
	case TierP1:
		return RolePlan
	case TierP2:
		return RoleDo
	case TierP3:
		return RoleSweep
	default:
		return RoleDo
	}
}

// TriggeredBy is the provenance value object (ontology).
type TriggeredBy struct {
	EventType  string
	DedupeKey  string
	ReceivedAt int64 // unix nanoseconds
}

// TrackerRef is a tracker's own identity for a work item (ontology).
type TrackerRef struct {
	Tracker      string
	RepositoryID string
	ExternalID   string
	URL          string
}

// Task is the durable, immutable statement of intent (ontology).
// Its identity is its problem statement; only state mutates.
type Task struct {
	ID             string
	RepositoryID   string
	Ticket         TrackerRef
	Tier           TaskTier
	Prompt         string
	Payload        string
	TimeoutSeconds int
	Budget         int
	State          TaskState
	TriggeredBy    TriggeredBy
	DerivedFrom    string
	CreatedAt      int64 // unix nanoseconds
}

// TaskAttempt is one execution of a Task (ontology).
// Retry count is len(attempts) — there is no retry-count column.
type TaskAttempt struct {
	ID               string
	TaskID           string
	Ordinal          int
	Outcome          AttemptOutcome
	StartedAt        int64
	FinishedAt       int64
	TimeoutSeconds   int
	RemoteSessionID  string
	Artifacts        []Artifact
}

// Artifact is something an attempt produced that outlives it (ontology).
type Artifact struct {
	ID            string
	AttemptID     string
	Kind          ArtifactKind
	BranchPurpose BranchPurpose
	URI           string
	ProducedAt    int64
}

// Lease is the claim a worker holds on a dequeued task.
// It carries an expiry; the worker must Ack, Nack, or Release before it.
type Lease struct {
	TaskID    string
	AttemptID string
	ExpiresAt int64 // unix nanoseconds
	Tier      TaskTier
}

// QueueEvent is a notification emitted on Enqueue, carrying the tier so a
// busy worker can learn that higher-priority work arrived (D16/I7).
type QueueEvent struct {
	Tier TaskTier
}

// Sentinel errors.
var (
	ErrLeaseExpired = errors.New("queue: lease expired before ack/nack")
	ErrNotLeased    = errors.New("queue: task is not leased by this caller")
	ErrDoubleClaim  = errors.New("queue: task already leased by another caller")
	ErrNotFound     = errors.New("queue: task not found")
	ErrDuplicate    = errors.New("queue: task with this dedupe_key already enqueued")
	ErrPreempted    = errors.New("queue: task was preempted by a higher-tier enqueue")
	ErrNoWork       = errors.New("queue: no work available")
)

// Queue is the adapter seam for the work queue. The lease is the whole
// fault-tolerance story: a daemon crash must never strand a task (D14).
// Concrete implementation: SQLite in the MVP (D13, ADR 0008).
//
// Specified verbatim in docs/architecture/boundaries.md §2.
type Queue interface {
	// Enqueue adds a task (idempotent per dedupe_key).
	Enqueue(ctx context.Context, task Task) error
	// Dequeue blocks until work is available and atomically claims the highest-tier
	// item (FIFO within tier). Returns a lease with an expiry.
	Dequeue(ctx context.Context) (Lease, error)
	// Ack marks the leased task's current attempt succeeded (I2 satisfied
	// via worker).
	Ack(ctx context.Context, lease Lease, attemptID string) error
	// Nack records the attempt outcome and lets retry policy decide (I6).
	Nack(ctx context.Context, lease Lease, attemptID string, outcome AttemptOutcome) error
	// Events returns a stream of enqueue notifications carrying the tier, so a busy
	// worker can learn that higher-priority work arrived (preemption, D16/I7).
	Events(ctx context.Context) (<-chan QueueEvent, error)
	// Release returns the lease without recording an outcome (only valid before the
	// attempt is started).
	Release(ctx context.Context, lease Lease) error
}
