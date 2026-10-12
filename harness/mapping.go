package harness

import (
	"net/http"

	"github.com/seppaleinen/idle-deck/queue"
)

// HarnessError is a typed transport error returned by RemoteHarness when the
// wire refuses or misbehaves. It carries the §7 outcome so the worker's retry
// policy (D14) sees the real classification instead of a plain error string.
//
// This is the fix for the conformance-suite gap: previously Start returned a
// plain fmt.Errorf on any non-2xx and the worker hardcoded
// OutcomeRetryableFailure, so a 401, 403, 404, and 422 all collapsed to
// "retryable" — the contract's retryable/non_retryable distinction was silently
// lost at the transport. Result looped forever on any non-200, so a 429
// mid-poll never mapped to retryable_failure either. Both paths now build a
// HarnessError and the worker errors.As it.
//
// D36: the token is never logged. Only the status code and the wire error
// body (which the server writes, not the credential) are retained.
type HarnessError struct {
	Outcome AttemptOutcome
	Message string
}

func (e *HarnessError) Error() string {
	return "harness: " + e.Message
}

// AttemptOutcomeValue returns the §7-classified attempt outcome.
func (e *HarnessError) AttemptOutcomeValue() AttemptOutcome { return e.Outcome }

// newHarnessError builds a HarnessError from an HTTP response. statusCode == 0
// means no response was received at all (network error / server unreachable),
// which §7 classifies as retryable_failure.
func newHarnessError(statusCode int, body []byte) *HarnessError {
	if statusCode == 0 {
		return &HarnessError{
			Outcome: OutcomeRetryableFailure,
			Message: "network error: server unreachable",
		}
	}
	he := &HarnessError{
		Outcome: AttemptOutcome(HTTPErrorOutcome(statusCode)),
		Message: string(body),
	}
	if he.Message == "" {
		he.Message = http.StatusText(statusCode)
	}
	return he
}

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
// HTTP-class mapping (supplemental, applied at the transport layer by
// RemoteHarness via HTTPErrorOutcome — see §7):
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

// HTTPErrorOutcome maps an HTTP status code to the corresponding outcome,
// per remote-contract §7 error taxonomy. This is applied at the transport
// layer by RemoteHarness (Start and Result), so the worker sees the real
// classification rather than a plain error string.
//
//	429 + Retry-After    → retryable_failure  (transient overload)
//	401 / 403 / 404     → non_retryable_failure (config / bug)
//	422 budget_exceeded → non_retryable_failure (ceiling refused)
//	5xx                 → retryable_failure (server error)
//	0 (no response)     → retryable_failure (network error / unreachable)
func HTTPErrorOutcome(statusCode int) AttemptOutcome {
	switch {
	case statusCode == 0:
		return OutcomeRetryableFailure // network error / server unreachable (§7)
	case statusCode >= 500:
		return OutcomeRetryableFailure // server error
	case statusCode == 429:
		return OutcomeRetryableFailure // transient overload (with Retry-After)
	case statusCode >= 400 && statusCode <= 499:
		switch statusCode {
		case 401, 403, 404:
			return OutcomeNonRetryableFailure // auth/config/bad request
		case 422:
			return OutcomeNonRetryableFailure // budget_exceeded
		default:
			// 400, 409, etc. — treat as non-retryable configuration/bug
			return OutcomeNonRetryableFailure
		}
	default:
		return OutcomeRetryableFailure // shouldn't happen, but be safe
	}
}