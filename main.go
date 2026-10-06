package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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
	cfg, err := config.Load(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Step 4: SQLite path writable; schema-current is deferred (no storage schema exists yet).
	if err := config.CheckDB(cfg.DB); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// Step 6: resolved configuration with secrets redacted.
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

	// Create tracker.
	trk := tracker.NewGitHub(cfg.GitHubAPI, cfg.GitHubToken, repos, wm)

	// Create harness.
	h := harness.NewRemoteHarness(cfg.HarnessURL, cfg.HarnessToken)

	// Create idle policy.
	idlePolicy := idle.NewHarnessIdlePolicy(cfg.HarnessURL, cfg.HarnessToken)

	// Start poller goroutine.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	poller := tracker.NewPoller(trk, q, wm, repos, cfg.PollInterval)
	go func() {
		ticker := time.NewTicker(cfg.PollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := poller.Tick(ctx); err != nil {
					log.Error("poll tick failed", "err", err)
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
	}

	// Open DB read-only.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode=WAL")
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: open DB: %v\n", err)
		return 1
	}
	defer db.Close()

	fmt.Println("idle-deck status")
	fmt.Println("================")

	// 1. Heartbeat age (DB file mtime as proxy) + run lock.
	info, err := os.Stat(dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: stat DB: %v\n", err)
		return 1
	}
	age := time.Since(info.ModTime())
	fmt.Printf("Heartbeat age: %s\n", age.Round(time.Second))

	var runningCount int
	db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM tasks WHERE state = 'running'`).Scan(&runningCount)
	if runningCount > 0 {
		fmt.Println("Run lock: held")
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

	// 3. Last poll per repo + watermark.
	fmt.Println("\nWatermarks:")
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
		fmt.Printf("  %s: watermark=%s\n", repo, watermark)
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

	// 5. Harness reachability.
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
