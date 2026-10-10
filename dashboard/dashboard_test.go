package dashboard

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/seppaleinen/idle-deck/config"
	"github.com/seppaleinen/idle-deck/store"
)

func TestServer_Smoke(t *testing.T) {
	dbPath := t.TempDir() + "/idle-deck.db"
	ctx := context.Background()
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	cfg := config.Defaults()
	cfg.DB = dbPath
	cfg.DashboardAddr = "127.0.0.1:0"
	cfg.HarnessURL = "http://127.0.0.1:9999"

	srv, err := NewServer(cfg)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	defer srv.Close()

	for _, path := range []string{
		"/api/status", "/api/health", "/api/repos",
		"/api/queue", "/api/attempts", "/api/artifacts",
		"/api/heartbeat", "/api/config", "/",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		srv.mux.ServeHTTP(w, req)
		if w.Code >= 400 {
			t.Errorf("%s: status %d body %s", path, w.Code, w.Body.String())
		}
	}
}

var _ = sql.ErrNoRows