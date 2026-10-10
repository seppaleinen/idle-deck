package dashboard

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"

	"github.com/seppaleinen/idle-deck/config"
	"github.com/seppaleinen/idle-deck/store"
)

type Server struct {
	db      *sql.DB
	cfg     config.Config
	mux     *http.ServeMux
	stopped chan struct{}
}

func NewServer(cfg config.Config) (*Server, error) {
	if cfg.DB == "" {
		return nil, fmt.Errorf("dashboard: DB path is empty")
	}
	dbPath := config.ExpandHome(cfg.DB)
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("dashboard: database %q does not exist (run idle-deck first)", dbPath)
	} else if err != nil {
		return nil, fmt.Errorf("dashboard: stat DB: %w", err)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode=WAL&_mode=ro")
	if err != nil {
		return nil, fmt.Errorf("dashboard: open DB: %w", err)
	}
	v, err := store.CurrentVersion(context.Background(), db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("dashboard: schema version: %w", err)
	}
	if v < store.SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("dashboard: schema stale (have %d, want %d)", v, store.SchemaVersion)
	}
	s := &Server{
		db:      db,
		cfg:     cfg,
		mux:     http.NewServeMux(),
		stopped: make(chan struct{}),
	}
	s.routes()
	return s, nil
}

func (s *Server) Close() error {
	return s.db.Close()
}

func (s *Server) routes() {
	s.mux.HandleFunc("/api/status", s.handleStatus)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/repos", s.handleRepos)
	s.mux.HandleFunc("/api/queue", s.handleQueue)
	s.mux.HandleFunc("/api/attempts", s.handleAttempts)
	s.mux.HandleFunc("/api/artifacts", s.handleArtifacts)
	s.mux.HandleFunc("/api/tasks", s.handleTasks)
	s.mux.HandleFunc("/api/heartbeat", s.handleHeartbeat)
	s.mux.HandleFunc("/api/config", s.handleConfig)

	s.mux.Handle("/static/", NewStaticHandler("/static/"))
	s.mux.HandleFunc("/", s.handleIndex)
}

func (s *Server) ListenAndServe() error {
	addr := s.cfg.DashboardAddr
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	log.Printf("dashboard: listening on http://%s", addr)
	srv := &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("dashboard: serve: %w", err)
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	close(s.stopped)
	return nil
}

func (s *Server) respondJSON(w http.ResponseWriter, r *http.Request, code int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("dashboard: encode JSON: %v", err)
	}
}

func (s *Server) respondError(w http.ResponseWriter, r *http.Request, code int, msg string) {
	s.respondJSON(w, r, code, map[string]string{"error": msg})
}

func (s *Server) dbQuery(ctx context.Context, query string, args ...interface{}) (*sql.Rows, error) {
	return s.db.QueryContext(ctx, query, args...)
}

func (s *Server) dbQueryRow(ctx context.Context, query string, args ...interface{}) *sql.Row {
	return s.db.QueryRowContext(ctx, query, args...)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeFileFS(w, r, staticFS, "static/index.html")
}
