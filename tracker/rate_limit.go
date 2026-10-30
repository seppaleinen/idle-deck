package tracker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/seppaleinen/idle-deck/store"
)

// ErrRateLimited is returned by the poller when GitHub responds with HTTP 429.
// It is NOT a tick failure: the watermark must not advance (event-contract §2),
// but the daemon continues polling other repositories without hammering the API.
var ErrRateLimited = errors.New("tracker: rate limited (429)")

// RateLimitState captures GitHub API rate-limit information from response headers.
type RateLimitState struct {
	Remaining  int           // X-RateLimit-Remaining
	ResetAt    time.Time     // X-RateLimit-Reset (Unix timestamp)
	RetryAfter time.Duration // Retry-After header (0 if not present)
	Last429    time.Time     // When the last 429 was received
}

// parseRateLimitHeaders extracts rate-limit state from a GitHub API response.
// It is safe to call on a nil response (returns zero value) and tolerates
// missing headers.
func parseRateLimitHeaders(resp *http.Response) RateLimitState {
	if resp == nil {
		return RateLimitState{}
	}

	var state RateLimitState

	// X-RateLimit-Remaining: how many requests are left in the current window
	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			state.Remaining = n
		}
	}

	// X-RateLimit-Reset: Unix timestamp when the quota resets
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if ts, err := strconv.ParseInt(v, 10, 64); err == nil {
			state.ResetAt = time.Unix(ts, 0)
		}
	}

	// Retry-After: either a number of seconds or an HTTP-date.
	// GitHub typically uses seconds on 429 responses.
	if v := strings.TrimSpace(resp.Header.Get("Retry-After")); v != "" {
		// First try as seconds
		if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
			state.RetryAfter = time.Duration(secs) * time.Second
		} else {
			// Fallback: parse as HTTP-date per RFC7231
			if t, err := time.Parse(http.TimeFormat, v); err == nil {
				state.RetryAfter = time.Until(t)
				if state.RetryAfter < 0 {
					state.RetryAfter = 0
				}
			}
		}
	}

	return state
}

// persistRateLimit saves the observed rate-limit state for a repo. It uses the
// WatermarkStore which in practice is a SQLiteWatermarks implementation. The
// function is best-effort: any error is returned to allow callers to log it.
func persistRateLimit(ctx context.Context, wm store.WatermarkStore, repo string, state RateLimitState) error {
	if wm == nil {
		return nil
	}
	// Store remaining quota and reset time; RetryAfter is only relevant on 429.
	remaining := state.Remaining
	resetAt := state.ResetAt.UnixNano()
	retryAfter := int64(state.RetryAfter.Nanoseconds())
	if err := wm.SaveRateLimit(ctx, repo, remaining, resetAt, retryAfter); err != nil {
		return fmt.Errorf("persist rate-limit: %w", err)
	}
	return nil
}

// persistRateLimit429 records that an HTTP 429 was observed, including the
// Retry-After value so the daemon can back off for the exact duration GitHub
// requested. This is a best-effort call; errors are logged by the caller.
func persistRateLimit429(ctx context.Context, wm store.WatermarkStore, repo string, state RateLimitState, now time.Time) error {
	if wm == nil {
		return nil
	}
	// Reuse the SQLite-specific SaveRateLimit429 if available. The base
	// WatermarkStore interface only exposes SaveRateLimit, so we also save
	// the regular state; a more complete implementation may call the 429-specific
	// method via a type assertion.
	remaining := state.Remaining
	resetAt := state.ResetAt.UnixNano()
	retryAfter := int64(state.RetryAfter.Nanoseconds())
	if err := wm.SaveRateLimit(ctx, repo, remaining, resetAt, retryAfter); err != nil {
		return fmt.Errorf("persist 429 state: %w", err)
	}
	return nil
}

// scaleIntervalForRemaining scales the base poll interval when the remaining
// quota is low. The formula is proportional: the closer Remaining is to zero,
// the more the interval is stretched, capped at a 5x factor.
func scaleIntervalForRemaining(base time.Duration, remaining int, maxRemaining int) time.Duration {
	if remaining <= 0 || maxRemaining <= 0 {
		// No information or quota exhausted: use the base interval to avoid
		// infinite scaling. The next tick will likely hit 429 and trigger backoff.
		return base
	}
	// Treat "maxRemaining" as the point where we consider the quota fully healthy.
	// A common GitHub limit is 5000 requests/hour (~80/min). We cap the scaling
	// at a 5x factor to avoid waiting hours on a slightly low quota.
	ratio := float64(maxRemaining) / float64(remaining)
	if ratio < 1.0 {
		ratio = 1.0
	}
	if ratio > 5.0 {
		ratio = 5.0
	}
	return time.Duration(float64(base) * ratio)
}
