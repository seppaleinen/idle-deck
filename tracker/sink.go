package tracker

import (
	"context"

	"github.com/seppaleinen/idle-deck/queue"
)

// TrackerSink writes diagnostics and labels back onto the tracker.
// It is the only write path out of idle-deck to the tracker (D25, ADR 0012).
//
// Defined here for documentation and discoverability; the worker declares its
// own local copy of this shape so the composition root does not import an
// adapter package. tracker.GitHub satisfies this interface structurally.
//
// TrackerSink must never parse events and must never own retry policy
// (boundaries.md §1 negative list): a failed Comment is logged and retried by
// the worker's retry policy, not by the sink.
type TrackerSink interface {
	// Comment posts a diagnostic comment on a work item.
	Comment(ctx context.Context, ref queue.TrackerRef, body string) error
	// SetLabels idempotently adds and removes labels on a work item.
	SetLabels(ctx context.Context, ref queue.TrackerRef, add, remove []string) error
	// OpenIssue creates a new issue for P3 sweep findings (D43, ADR 0019).
	OpenIssue(ctx context.Context, repo string, title string, body string, labels []string) error
}
