package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/seppaleinen/idle-deck/config"
	"github.com/seppaleinen/idle-deck/harness"
	"github.com/seppaleinen/idle-deck/idle"
	"github.com/seppaleinen/idle-deck/queue"
	"github.com/seppaleinen/idle-deck/store"
	"github.com/seppaleinen/idle-deck/tracker"
	"github.com/seppaleinen/idle-deck/worker"

	_ "modernc.org/sqlite"

	"log/slog"
)

const version = "0.0.0-dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "--version", "-version", "version":
		fmt.Printf("idle-deck %s\n", version)
		return
	case "check":
		os.Exit(runCheck(os.Args[2:]))
	case "run":
		os.Exit(runRun(os.Args[2:]))
	case "status":
		os.Exit(runStatus(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: idle-deck <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  check   validate configuration and connectivity")
	fmt.Fprintln(os.Stderr, "  run     run the daemon in the foreground")
	fmt.Fprintln(os.Stderr, "  status  report what the daemon is doing")
	fmt.Fprintln(os.Stderr, "  --version  print the version")
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("idle-deck check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	cfg, err := config.Load(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Step 1: required variables present and non-empty (printing the name,
	// never the value — D36).
	if missing := cfg.MissingRequired(); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "missing required configuration variables: %s\n", strings.Join(missing, ", "))
		return 1
	}

	// Step 2: one authenticated read against the tracker — confirms the PAT
	// is valid and has Issues read access (operator-surface §5).
	repos, err := config.ParseRepos(cfg.Repos)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse repos: %v\n", err)
		return 1
	}
	trk := tracker.NewGitHub(cfg.GitHubAPI, cfg.GitHubToken, repos, nil)
	if err := trk.ValidateRead(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "tracker read: %v\n", err)
		return 1
	}
	fmt.Println("tracker: authenticated read OK")

	// Step 3: one GET /health against the harness — confirms URL and bearer
	// token (operator-surface §5). The harness /health must report
	// reachability, not process liveness (D31).
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, cfg.HarnessURL+"/v1/health", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness health: %v\n", err)
		return 1
	}
	req.Header.Set("Authorization", "Bearer "+cfg.HarnessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "harness health: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		fmt.Fprintf(os.Stderr, "harness health: status %d: %s\n", resp.StatusCode, string(b))
		return 1
	}
	fmt.Println("harness: /health reachable")

	// Step 4: SQLite path writable and schema current (D33: auto-migrations
	// run forward-only at startup).
	dbPath := config.ExpandHome(cfg.DB)
	if err := config.CheckDB(dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "sqlite: %v\n", err)
		return 1
	}
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sqlite: open: %v\n", err)
		return 1
	}
	v, err := store.CurrentVersion(ctx, db)
	db.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "schema version: %v\n", err)
		return 1
	}
	if v < store.SchemaVersion {
		fmt.Fprintf(os.Stderr, "sqlite: schema stale (have %d, want %d)\n", v, store.SchemaVersion)
		return 1
	}
	fmt.Printf("sqlite: schema current (v%d)\n", v)

	// Step 5: which of the four D29 labels exist in each configured repository;
	// for any missing, the exact `gh label create` command (operator-surface
	// §5, D39). Colours are documented as a suggested palette, never applied.
	type labelDef struct {
		name  string
		color string
	}
	labels := []labelDef{
		{tracker.LabelHotfix, "ff0000"},
		{tracker.LabelReady, "0086b3"},
		{tracker.LabelRedo, "fbca04"},
		{tracker.LabelNeedsHuman, "b60205"},
	}
	fmt.Println("\nLabels:")
	for _, repo := range repos {
		fmt.Printf("  %s:\n", repo)
		for _, l := range labels {
			exists, err := trk.LabelExists(context.Background(), repo, l.name)
			if err != nil {
				fmt.Fprintf(os.Stderr, "    %s: error: %v\n", l.name, err)
				return 1
			}
			if exists {
				fmt.Printf("    %s: present\n", l.name)
			} else {
				fmt.Printf("    %s: MISSING  (gh label create %s/%s --name \"%s\" --color \"%s\")\n",
					l.name, repo, l.name, l.name, l.color)
			}
		}
	}

	// Step 6: resolved configuration with secrets redacted, non-secret values
	// shown at their defaults (operator-surface §5, D36).
	fmt.Println("\nConfiguration:")
	fmt.Print(config.Format(config.Redact(cfg)))
	return 0
}

func runRun(args []string) int {
	fs := flag.NewFlagSet("idle-deck run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	// Create logger (JSON or text per config).
	var handler slog.Handler
	logLevel := slog.LevelInfo
	switch cfg.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	}
	if cfg.LogFormat == "text" {
		handler = slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	} else {
		handler = slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel})
	}
	log := slog.New(handler)

	// Parse repos.
	repos, err := config.ParseRepos(cfg.Repos)
	if err != nil {
		log.Error("parse repos", "err", err)
		return 1
	}

	// Create SQLite queue with maxAttempts=3 (D14: 2 retries + 1 initial).
	dbPath := config.ExpandHome(cfg.DB)
	q := store.NewSQLiteQueue(dbPath, store.WithMaxAttempts(3))

	// Create watermarks store.
	wm := store.NewSQLiteWatermarks(q.DB())

	// Create heartbeat store (operator-surface §6): liveness + run lock.
	hb := store.NewSQLiteHeartbeat(q.DB())

	// Create tracker.
	trk := tracker.NewGitHub(cfg.GitHubAPI, cfg.GitHubToken, repos, wm)

	// Create harness.
	h := harness.NewRemoteHarness(cfg.HarnessURL, cfg.HarnessToken)

	// Create idle policy.
	idlePolicy := idle.NewHarnessIdlePolicy(cfg.HarnessURL, cfg.HarnessToken)

	// Start poller goroutine.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	// Release the run lock on any exit path (clean or not). Use a fresh
	// context: ctx is already cancelled by the time this defer runs.
	defer func() { _ = hb.ReleaseRunLock(context.Background()) }()

	// Claim the run lock: the daemon is the active run holder.
	// TakeRunLock returns (acquired, err): acquired=true means we now hold it.
	if acquired, err := hb.TakeRunLock(ctx); err != nil {
		log.Error("take run lock", "err", err)
		return 1
	} else if !acquired {
		log.Warn("run lock already held by another process")
		return 1
	}

	poller := tracker.NewPoller(trk, q, wm, repos, cfg.PollInterval)
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				_ = hb.ReleaseRunLock(ctx)
				return
			case <-ticker.C:
				if err := poller.Tick(ctx); err != nil {
					log.Error("poll tick failed", "err", err)
					continue
				}
				// Beat on every successful poll tick (operator-surface §6).
				_ = hb.Beat(ctx, time.Now())
				for _, repo := range repos {
					_ = hb.SavePoll(ctx, repo, time.Now())
				}
			}
		}
	}()

	// Create and run worker.
	w := worker.NewWorker(q, h, trk, idlePolicy, cfg.MaxConcurrent, cfg.RetryBackoffInitial, cfg.RetryBackoffMax, log)
	if err := w.Run(ctx); err != nil {
		log.Error("worker exited", "err", err)
		return 1
	}
	return 0
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("idle-deck status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dbFlag := fs.String("db", "", "SQLite database path (env IDLE_DECK_DB)")
	harnessURLFlag := fs.String("harness-url", "", "Remote execution service base URL (env IDLE_DECK_HARNESS_URL)")
	harnessTokenFlag := fs.String("harness-token", "", "Bearer token for harness (env IDLE_DECK_HARNESS_TOKEN)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Relaxed config load: defaults + --db flag override, skip Validate().
	// Also read optional env vars for status (HarnessURL, HarnessToken).
	cfg := config.Defaults()
	if v := os.Getenv("IDLE_DECK_DB"); v != "" {
		cfg.DB = v
	}
	if *dbFlag != "" {
		cfg.DB = *dbFlag
	}
	// Apply env vars for optional fields (HarnessURL, HarnessToken).
	if v := os.Getenv("IDLE_DECK_HARNESS_URL"); v != "" {
		cfg.HarnessURL = v
	}
	if v := os.Getenv("IDLE_DECK_HARNESS_TOKEN"); v != "" {
		cfg.HarnessToken = v
	}
	// Apply flag overrides for optional fields.
	if *harnessURLFlag != "" {
		cfg.HarnessURL = *harnessURLFlag
	}
	if *harnessTokenFlag != "" {
		cfg.HarnessToken = *harnessTokenFlag
	}
	dbPath := config.ExpandHome(cfg.DB)

	// Check if DB file exists.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		fmt.Println("idle-deck status")
		fmt.Println("================")
		fmt.Println("No state yet (database not found)")
		return 0
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "status: stat DB: %v\n", err)
		return 1
	}

	// Open DB read-only.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode=WAL")
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: open DB: %v\n", err)
		return 1
	}
	defer db.Close()
	hb := store.NewSQLiteHeartbeat(db)

	fmt.Println("idle-deck status")
	fmt.Println("================")

	// 1. Heartbeat age + run lock (operator-surface §6).
	// A heartbeat older than three poll intervals is stale: the daemon is up
	// but not ingesting, and looks exactly like a healthy one without it.
	beat, err := hb.LastBeat(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: heartbeat: %v\n", err)
		return 1
	}
	pollInterval := 60 * time.Second
	if beat.IsZero() {
		fmt.Println("Heartbeat: never (daemon has not run)")
	} else {
		age := time.Since(beat)
		fmt.Printf("Heartbeat age: %s\n", age.Round(time.Second))
		if age > 3*pollInterval {
			fmt.Println("Heartbeat: STALE (daemon may be dead or not polling)")
		}
	}
	held, err := hb.RunLockHeld(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: run lock: %v\n", err)
		return 1
	}
	if held {
		if beat.IsZero() || time.Since(beat) > 3*pollInterval {
			fmt.Println("Run lock: held but heartbeat stale (orphaned lock)")
		} else {
			fmt.Println("Run lock: held")
		}
	} else {
		fmt.Println("Run lock: free")
	}

	// 2. Queue depth by tier and state.
	fmt.Println("\nQueue depth:")
	rows, err := db.QueryContext(context.Background(), `SELECT state, tier, COUNT(*) FROM tasks GROUP BY state, tier ORDER BY state, tier`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: query queue: %v\n", err)
		return 1
	}
	defer rows.Close()
	for rows.Next() {
		var state, tier string
		var count int
		rows.Scan(&state, &tier, &count)
		fmt.Printf("  %s %s: %d\n", state, tier, count)
	}

	// 3. Last successful poll per repo + watermark (operator-surface §6).
	// The last-poll-plus-watermark pair is what tells an operator whether
	// ingestion is silently stuck (operator-surface §6).
	fmt.Println("\nPolls and watermarks:")
	rows2, err := db.QueryContext(context.Background(), `SELECT repo, watermark, updated_at FROM tracker_watermarks ORDER BY repo`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: query watermarks: %v\n", err)
		return 1
	}
	defer rows2.Close()
	for rows2.Next() {
		var repo, watermark string
		var updatedAt int64
		rows2.Scan(&repo, &watermark, &updatedAt)
		lastPoll, seeded, err := hb.LastPoll(context.Background(), repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "status: query poll: %v\n", err)
			return 1
		}
		if seeded {
			fmt.Printf("  %s: watermark=%s  last poll=%s\n", repo, watermark, lastPoll.Round(time.Second))
		} else {
			fmt.Printf("  %s: watermark=%s  last poll: never\n", repo, watermark)
		}
		_ = updatedAt
	}

	// 4. Active task.
	fmt.Println("\nActive task:")
	row := db.QueryRowContext(context.Background(), `
		SELECT t.id, t.tier, a.id, a.started_at
		FROM tasks t
		JOIN attempts a ON a.task_id = t.id AND a.outcome = ''
		WHERE t.state = 'running'
		LIMIT 1
	`)
	var taskID, tier, attemptID string
	var startedAt int64
	if err := row.Scan(&taskID, &tier, &attemptID, &startedAt); err == nil {
		runningTime := time.Since(time.Unix(0, startedAt))
		role := queue.RoleForTier(queue.TaskTier(tier))
		fmt.Printf("  Task: %s\n", taskID)
		fmt.Printf("  Tier: %s (role: %s)\n", tier, role)
		fmt.Printf("  Attempt: %s\n", attemptID)
		fmt.Printf("  Running: %s\n", runningTime.Round(time.Second))
	} else if err == sql.ErrNoRows {
		fmt.Println("  None")
	} else {
		fmt.Fprintf(os.Stderr, "status: query active task: %v\n", err)
		return 1
	}

	// 5. Harness reachability, and the outcome of the most recent call
	// (operator-surface §6).
	fmt.Println("\nHarness:")
	if cfg.HarnessURL != "" {
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, cfg.HarnessURL+"/v1/health", nil)
		if cfg.HarnessToken != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.HarnessToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Printf("  Reachability: unreachable (%v)\n", err)
		} else {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				fmt.Println("  Reachability: reachable")
			} else {
				fmt.Printf("  Reachability: status %d\n", resp.StatusCode)
			}
		}
	} else {
		fmt.Println("  Reachability: not configured (IDLE_DECK_HARNESS_URL not set)")
	}

	// 6. Most recent escalation.
	fmt.Println("\nMost recent escalation:")
	row2 := db.QueryRowContext(context.Background(), `
		SELECT t.id, t.tier, MAX(a.finished_at)
		FROM tasks t
		JOIN attempts a ON a.task_id = t.id
		WHERE t.state = 'escalated'
		GROUP BY t.id
		ORDER BY MAX(a.finished_at) DESC
		LIMIT 1
	`)
	var escTaskID, escTier string
	var escFinishedAt int64
	if err := row2.Scan(&escTaskID, &escTier, &escFinishedAt); err == nil {
		fmt.Printf("  Task: %s\n", escTaskID)
		fmt.Printf("  Tier: %s\n", escTier)
		fmt.Printf("  Time: %s\n", time.Unix(0, escFinishedAt).Format(time.RFC3339))
	} else if err == sql.ErrNoRows {
		fmt.Println("  None")
	} else {
		fmt.Fprintf(os.Stderr, "status: query escalation: %v\n", err)
		return 1
	}

	return 0
}
