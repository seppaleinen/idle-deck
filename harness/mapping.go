package harness

import "github.com/seppaleinen/idle-deck/queue"

// mapWireOutcome maps the wire outcome state and code to an AttemptOutcome
// per remote-contract §6/§7.
//
// Wire outcome.state → AttemptOutcome:
//
//	completed          → succeeded
//	failed + code      → retryable_failure / non_retryable_failure per code list
//	timed_out          → timeout (retryable, D17)
//	cancelled          → preempted (client-side steering, I7 — never on wire)
//
// HTTP-class mapping (supplemental, handled at the transport layer):
//
//	429 + Retry-After    → retryable_failure  (transient overload)
//	401 / 403 / 404     → non_retryable_failure (config / bug)
//	422 budget_exceeded → non_retryable_failure (ceiling refused)
//	5xx                 → retryable_failure (server error)
//	network error       → retryable_failure
func mapWireOutcome(state string, code *string) queue.AttemptOutcome {
	switch state {
	case "completed":
		return queue.OutcomeSucceeded
	case "failed":
		if code != nil {
			switch *code {
			case "agent_error", "server-internal":
				return queue.OutcomeRetryableFailure
			case "test_failure", "budget_exceeded":
				return queue.OutcomeNonRetryableFailure
			}
		}
		return queue.OutcomeRetryableFailure // failed without code is retryable by default
	case "timed_out":
		return queue.OutcomeTimeout // retryable (D17)
	case "cancelled":
		// Steered client-side to preempted (D16/I7) — never crosses the wire
		return queue.OutcomePreempted
	default:
		return queue.OutcomeNonRetryableFailure
	}
}

// httpErrorOutcome maps an HTTP status code to the corresponding outcome,
// per remote-contract §7 error taxonomy. This is used when the wire returns
// an HTTP error rather than a structured outcome object.
func httpErrorOutcome(statusCode int) queue.AttemptOutcome {
	switch {
	case statusCode >= 500:
		return queue.OutcomeRetryableFailure // server error
	case statusCode == 429:
		return queue.OutcomeRetryableFailure // transient overload (with Retry-After)
	case statusCode >= 400 && statusCode <= 499:
		switch statusCode {
		case 401, 403, 404:
			return queue.OutcomeNonRetryableFailure // auth/config/bad request
		case 422:
			return queue.OutcomeNonRetryableFailure // budget_exceeded
		default:
			// 400, 409, etc. — treat as non-retryable configuration/bug
			return queue.OutcomeNonRetryableFailure
		}
	default:
		return queue.OutcomeRetryableFailure // shouldn't happen, but be safe
	}
}
