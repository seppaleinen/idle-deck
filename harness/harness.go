package harness

import "context"

// Harness is the adapter seam for the remote execution service.
// Concrete implementation: one remote JSON/HTTP adapter in the MVP (D13).
type Harness interface {
	// Start begins execution of a run and returns a session id usable for abort (I8).
	Start(ctx context.Context, req RunRequest) (RunID, error)
	// Abort cancels a running session. It returns only when the remote side confirms
	// cancellation (or has already completed).
	Abort(ctx context.Context, id RunID) error
	// Result blocks until the run is terminal and returns its outcome and artifacts.
	Result(ctx context.Context, id RunID) (RunResult, error)
}
