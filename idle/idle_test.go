package idle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Test 1: Harness returns {"reachable": true} → Idle() returns true, nil.
func TestIdlePolicy_ReachableTrue(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("expected /health, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"reachable": true})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "test-token", false)
	idle, err := p.Idle(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !idle {
		t.Fatal("expected idle=true, got false")
	}
}

// Test 2: Harness returns {"reachable": false} (or only liveness) → Idle() returns false, nil.
func TestIdlePolicy_ReachableFalse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"reachable": false})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", false)
	idle, err := p.Idle(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idle {
		t.Fatal("expected idle=false, got true")
	}
}

// Test 3a: Harness returns 500 → Idle() returns false, error.
func TestIdlePolicy_HTTP5xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", false)
	idle, err := p.Idle(context.Background())
	if err == nil {
		t.Fatal("expected error for 500, got nil")
	}
	if idle {
		t.Fatal("expected idle=false, got true")
	}
}

// Test 3b: Connection refused → Idle() returns false, error.
func TestIdlePolicy_ConnectionRefused(t *testing.T) {
	// Use an invalid port on localhost to force connection refused.
	p := NewHarnessIdlePolicy("http://127.0.0.1:1", "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	idle, err := p.Idle(ctx)
	if err == nil {
		t.Fatal("expected error for connection refused, got nil")
	}
	if idle {
		t.Fatal("expected idle=false, got true")
	}
}

// Test 3c: Harness times out beyond context deadline → Idle() returns false, error.
func TestIdlePolicy_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than the context deadline.
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", false)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	idle, err := p.Idle(ctx)
	if err == nil {
		t.Fatal("expected error for timeout, got nil")
	}
	if idle {
		t.Fatal("expected idle=false, got true")
	}
}

// Test 4a: Harness returns 200 with malformed JSON → Idle() returns false, error.
func TestIdlePolicy_MalformedJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not-json"))
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", false)
	idle, err := p.Idle(context.Background())
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if idle {
		t.Fatal("expected idle=false, got true")
	}
}

// Test 4b: Harness returns 200 with missing "reachable" field → Idle() returns false, error.
func TestIdlePolicy_MissingReachableField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", false)
	idle, err := p.Idle(context.Background())
	if err == nil {
		t.Fatal("expected error for missing reachable field, got nil")
	}
	if idle {
		t.Fatal("expected idle=false, got true")
	}
}

// Test 5: Bearer token is sent on request.
func TestIdlePolicy_BearerTokenSent(t *testing.T) {
	var capturedAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"reachable": true})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "secret-token", false)
	_, _ = p.Idle(context.Background())

	if capturedAuth != "Bearer secret-token" {
		t.Fatalf("expected Authorization 'Bearer secret-token', got %q", capturedAuth)
	}
}

// Test 6a (D49, default): a busy model is still idle when the gate is off.
// This is the backward-compatible behaviour — every existing deployment
// observes reachable-only gating regardless of what the producer reports.
func TestIdlePolicy_BusyIgnoredWhenGateOff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reachable": true,
			"busy":      true,
		})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", false)
	idle, err := p.Idle(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !idle {
		t.Fatal("gate off: busy model should still be idle (D49 default)")
	}
}

// Test 6b (D49, gate on): a busy model is not idle when the operator opted in.
func TestIdlePolicy_BusyBlocksWhenGateOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reachable": true,
			"busy":      true,
		})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", true)
	idle, err := p.Idle(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idle {
		t.Fatal("gate on: busy model must not be idle (D49)")
	}
}

// Test 6c (D49): reachable + not busy + gate on → idle. The gate only fires
// on a busy answer; a free reachable model is idle either way.
func TestIdlePolicy_NotBusyIsIdleWithGateOn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reachable": true,
			"busy":      false,
		})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", true)
	idle, err := p.Idle(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !idle {
		t.Fatal("gate on: free reachable model must be idle (D49)")
	}
}

// Test 6d (D49): a producer that omits `busy` entirely (pre-D49 harness) must
// not error and must not be treated as busy. The field is optional for
// backward compatibility; missing means not-busy, never an error.
func TestIdlePolicy_MissingBusyFieldIsNotBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"reachable": true})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", true)
	idle, err := p.Idle(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !idle {
		t.Fatal("missing busy field: must not be treated as busy (D49)")
	}
}

// Test 6e (D49): reachability still wins — an unreachable model is not idle
// regardless of busy, with the gate on. Reachability is checked first.
func TestIdlePolicy_UnreachableWinsOverBusy(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"reachable": false,
			"busy":      true,
		})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "", true)
	idle, err := p.Idle(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if idle {
		t.Fatal("unreachable: must not be idle even when gate is on (D49)")
	}
}
