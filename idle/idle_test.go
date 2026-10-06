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
		if r.URL.Path != "/v1/health" {
			t.Errorf("expected /v1/health, got %s", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-token" {
			t.Errorf("expected Bearer test-token, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]bool{"reachable": true})
	}))
	defer srv.Close()

	p := NewHarnessIdlePolicy(srv.URL, "test-token")
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

	p := NewHarnessIdlePolicy(srv.URL, "")
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

	p := NewHarnessIdlePolicy(srv.URL, "")
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
	p := NewHarnessIdlePolicy("http://127.0.0.1:1", "")
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

	p := NewHarnessIdlePolicy(srv.URL, "")
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

	p := NewHarnessIdlePolicy(srv.URL, "")
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

	p := NewHarnessIdlePolicy(srv.URL, "")
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

	p := NewHarnessIdlePolicy(srv.URL, "secret-token")
	_, _ = p.Idle(context.Background())

	if capturedAuth != "Bearer secret-token" {
		t.Fatalf("expected Authorization 'Bearer secret-token', got %q", capturedAuth)
	}
}
