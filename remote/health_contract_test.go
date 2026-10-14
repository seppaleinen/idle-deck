package remote

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seppaleinen/idle-deck/idle"
)

// TestHealthProducerConsumerContract pins the /health response shape between
// its producer (remote.Server.handleHealth, driven by cmd/remote/health.go)
// and its sole consumer (idle.HarnessIdlePolicy.Idle, idle/idle.go).
//
// D31 defines exactly one idle probe. This is a contract pin, not a
// behavioural test: it exists so the two sides cannot drift apart
// silently. If the producer stops emitting "reachable", or the consumer
// starts requiring a field the producer omits, this fails immediately.
func TestHealthProducerConsumerContract(t *testing.T) {
	producer := NewServer(newStubRunner(), func() (bool, string) { return true, "opencode/big-pickle" }, "", t.TempDir(), t.TempDir(), "")
	srv := httptest.NewServer(producer.Routes())
	defer srv.Close()

	consumer := idle.NewHarnessIdlePolicy(srv.URL, "")

	// Producer reports reachable → consumer must certify idle.
	idle, err := consumer.Idle(t.Context())
	if err != nil {
		t.Fatalf("reachable: consumer errored: %v", err)
	}
	if !idle {
		t.Fatal("reachable: consumer reported not-idle")
	}

	// Producer reports unreachable (model down, process alive — D31) →
	// consumer must NOT certify idle.
	producer.Health = func() (bool, string) { return false, "opencode/big-pickle" }
	idle, err = consumer.Idle(t.Context())
	if err != nil {
		t.Fatalf("unreachable: consumer errored: %v", err)
	}
	if idle {
		t.Fatal("unreachable: consumer certified idle against a dead model")
	}
}

// TestHealthProducerShape pins the literal wire shape the producer emits, so
// a rename of the "reachable" key cannot pass unnoticed. The consumer reads
// only "reachable"; the producer emits "reachable" and "model".
func TestHealthProducerShape(t *testing.T) {
	producer := NewServer(newStubRunner(), func() (bool, string) { return true, "stub-model" }, "", t.TempDir(), t.TempDir(), "")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	producer.Routes().ServeHTTP(rec, req)

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode producer body: %v", err)
	}
	if _, ok := body["reachable"]; !ok {
		t.Fatalf("producer omits reachable: %v", body)
	}
	if _, ok := body["model"]; !ok {
		t.Fatalf("producer omits model: %v", body)
	}
}