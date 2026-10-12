package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/seppaleinen/idle-deck/remote"
)

func main() {
	bareDir := os.Getenv("IDLE_DECK_REMOTE_BARE_DIR")
	if bareDir == "" {
		bareDir = os.TempDir() + "/idle-deck-bare"
	}
	baseDir := os.Getenv("IDLE_DECK_REMOTE_BASE_DIR")
	if baseDir == "" {
		baseDir = os.TempDir() + "/idle-deck-work"
	}
	gitToken := os.Getenv("IDLE_DECK_REMOTE_GIT_TOKEN")
	if gitToken == "" {
		log.Fatal("IDLE_DECK_REMOTE_GIT_TOKEN is required")
	}
	httpToken := os.Getenv("IDLE_DECK_REMOTE_HTTP_TOKEN")
	if httpToken == "" {
		// For local dev, allow empty? but we require a token to match idle-deck.
		// We'll allow empty but warn.
		log.Print("WARN: IDLE_DECK_REMOTE_HTTP_TOKEN is empty; server will not enforce auth")
	}
	roleToModelJSON := os.Getenv("IDLE_DECK_REMOTE_ROLE_TO_MODEL")
	var roleToModel map[string]string
	if roleToModelJSON == "" {
		roleToModel = map[string]string{
			"plan": "opencode/big-pickle",
			"do":   "opencode/big-pickle",
			"sweep": "opencode/big-pickle",
		}
	} else {
		if err := json.Unmarshal([]byte(roleToModelJSON), &roleToModel); err != nil {
			log.Fatalf("Invalid JSON in IDLE_DECK_REMOTE_ROLE_TO_MODEL: %v", err)
		}
	}
	listenAddr := os.Getenv("IDLE_DECK_REMOTE_LISTEN_ADDR")
	if listenAddr == "" {
		listenAddr = ":8080"
	}

	// Create the ACP runner (default agent).
	runner := remote.NewACP(remote.ACPConfig{
		Bin:   "opencode",
		Args:  []string{"acp"},
		Env:   nil,
	})
	// Model-reachability probe (D31, ADR 0016). The /health endpoint must
	// report whether the model can actually generate, not merely whether
	// the agent process is alive — idle-deck's IdlePolicy gates every run
	// on this answer, so a stub that always said "reachable" would start
	// runs against a dead model. The probe drives a real ACP round-trip
	// through the same agent CLI the runner uses; see cmd/remote/health.go.
	probeModel := "opencode/big-pickle"
	for _, m := range roleToModel {
		if m != "" {
			probeModel = m
			break
		}
	}
	healthProbe := newHealthProbe("opencode", []string{"acp"}, probeModel, nil)
	// Create the remote server.
	srv := remote.NewServer(runner, healthProbe.Check, httpToken, bareDir, baseDir, gitToken)
	// Override the RoleToModel in the config (DefaultConfig sets a default).
	srv.Config.RoleToModel = roleToModel
	// Optionally override HTTPTimeout and WorktreeTTL from env if needed.
	// For simplicity, we keep the defaults from DefaultConfig.

	// Set up HTTP server.
	mux := srv.Routes()
	log.Printf("Starting remote server on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, mux); err != nil {
		log.Fatalf("ListenAndServe: %v", err)
	}
}
