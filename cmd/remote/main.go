package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"

	"github.com/seppaleinen/idle-deck/remote"
)

// agentConfig resolves the ACP agent launch recipe from operator env vars.
// This is D46: the remote agent CLI is operator config, not a build-time
// choice. idle-deck never names a model, provider, or vendor (D6/D40) —
// RunRequest carries a role, and the harness (this binary) decides which
// agent CLI speaks ACP on its behalf. Any ACP agent drops in by setting
// IDLE_DECK_REMOTE_AGENT_BIN / _AGENT_ARGS; A2A-platform adapters are a
// second Harness adapter (D41 trigger 3), not a config swap.
func agentConfig() remote.ACPConfig {
	bin := envOrDefault("IDLE_DECK_REMOTE_AGENT_BIN", "opencode")
	args := envJSONOrDefault("IDLE_DECK_REMOTE_AGENT_ARGS", []string{"acp"})
	env := envJSONOrDefault("IDLE_DECK_REMOTE_AGENT_ENV", []string(nil))
	return remote.ACPConfig{
		Bin:  bin,
		Args: args,
		Env:  env,
	}
}

func envOrDefault(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envJSONOrDefault(name string, fallback []string) []string {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	var parsed []string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		log.Fatalf("Invalid JSON in %s: %v", name, err)
	}
	return parsed
}

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
			"plan":   "opencode/big-pickle",
			"do":     "opencode/big-pickle",
			"sweep":  "opencode/big-pickle",
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

	// ACP runner. The launch recipe comes from operator config (D46), so
	// the binary never hardcodes an agent name — swap the env vars and the
	// remote runs a different ACP CLI without a rebuild.
	cfg := agentConfig()
	runner := remote.NewACP(cfg)

	// Model-reachability probe (D31, ADR 0016). The /health endpoint must
	// report whether the model can actually generate, not merely whether
	// the agent process is alive — idle-deck's IdlePolicy gates every run
	// on this answer, so a stub that always said "reachable" would start
	// runs against a dead model. The probe drives a real ACP round-trip
	// through the SAME agent CLI the runner uses (cfg.Bin/Args/Env); see
	// cmd/remote/health.go.
	probeModel := "opencode/big-pickle"
	for _, m := range roleToModel {
		if m != "" {
			probeModel = m
			break
		}
	}
	healthProbe := newHealthProbe(cfg.Bin, cfg.Args, probeModel, cfg.Env)

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