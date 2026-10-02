package worker

// Worker is the core composition root (not an adapter).
// Concrete implementation: one Go daemon, one worker loop (D13).
type Worker interface{}