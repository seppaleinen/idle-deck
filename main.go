package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/seppaleinen/idle-deck/config"
	"github.com/seppaleinen/idle-deck/dashboard"
	"github.com/seppaleinen/idle-deck/harness"
	"github.com/seppaleinen/idle-deck/idle"
	"github.com/seppaleinen/idle-deck/queue"
	"github.com/seppaleinen/idle-deck/store"
	"github.com/seppaleinen/idle-deck/tracker"
	"github.com/seppaleinen/idle-deck/worker"

	_ "modernc.org/sqlite"

	"log/slog"
)

const version = "v0.1.0"

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
	case "serve":
		os.Exit(runServe(os.Args[2:]))
	case "secrets":
		os.Exit(runSecrets(os.Args[2:]))
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: idle-deck <command> [flags]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  check    validate configuration and connectivity")
	fmt.Fprintln(os.Stderr, "  run      run the daemon in the foreground")
	fmt.Fprintln(os.Stderr, "  status   report what the daemon is doing")
	fmt.Fprintln(os.Stderr, "  serve    run the read-only dashboard")
	fmt.Fprintln(os.Stderr, "  secrets  manage secrets in macOS Keychain")
	fmt.Fprintln(os.Stderr, "  --version  print the version")
}

func runCheck(args []string) int {
	fs := flag.NewFlagSet("idle-deck check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonFlag := fs.Bool("json", false, "Output machine-readable JSON")
	flags := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	cfg, err := config.LoadRelaxed(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	type checkResult struct {
		Name   string `json:"name"`
		Status string `json:"status"`
		Detail string `json:"detail,omitempty"`
	}

	results := []checkResult{}
	addCheck := func(name, status, detail string) {
		results = append(results, checkResult{Name: name, Status: status, Detail: detail})
		if !*jsonFlag && status != "PASS" {
			fmt.Fprintf(os.Stderr, "%s: %s\n", name, detail)
		}
	}

	// Check 1: required config
	if missing := cfg.MissingRequired(); len(missing) > 0 {
		if *jsonFlag {
			addCheck("required_config", "FAIL", fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			result := map[string]interface{}{
				"checks": results,
				"overall": "FAIL",
			}
			_ = enc.Encode(result)
			return 1
		}
		fmt.Fprintf(os.Stderr, "missing required configuration variables: %s\n", strings.Join(missing, ", "))
		return 1
	}
	addCheck("required_config", "PASS", "")

	repos, err := config.ParseRepos(cfg.Repos)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse repos: %v\n", err)
		return 1
	}
	trk := tracker.NewGitHub(cfg.GitHubAPI, cfg.GitHubToken, repos, nil)
	if err := trk.ValidateRead(ctx); err != nil {
		addCheck("tracker_read", "FAIL", err.Error())
		if !*jsonFlag {
			fmt.Fprintf(os.Stderr, "tracker read: %v\n", err)
		}
	} else {
		addCheck("tracker_read", "PASS", "authenticated read OK")
		if !*jsonFlag {
			fmt.Println("tracker: authenticated read OK")
		}
	}

	// Check harness health
	harnessOK := false
	harnessDetail := ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.HarnessURL+"/health", nil)
	if err != nil {
		harnessDetail = err.Error()
	} else {
		req.Header.Set("Authorization", "Bearer "+cfg.HarnessToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			harnessDetail = err.Error()
		} else {
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				b, _ := io.ReadAll(resp.Body)
				harnessDetail = fmt.Sprintf("status %d: %s", resp.StatusCode, string(b))
			} else {
				harnessOK = true
				harnessDetail = "/health reachable"
			}
		}
	}
	if harnessOK {
		addCheck("harness_health", "PASS", harnessDetail)
		if !*jsonFlag {
			fmt.Println("harness: /health reachable")
		}
	} else {
		addCheck("harness_health", "FAIL", harnessDetail)
		if !*jsonFlag {
			fmt.Fprintf(os.Stderr, "harness health: %v\n", harnessDetail)
		}
	}

	// Check SQLite
	dbPath := config.ExpandHome(cfg.DB)
	sqliteOK := false
	sqliteDetail := ""
	if err := config.CheckDB(dbPath); err != nil {
		sqliteDetail = err.Error()
	} else {
		db, err := store.Open(ctx, dbPath)
		if err != nil {
			sqliteDetail = fmt.Sprintf("open: %v", err)
		} else {
			v, err := store.CurrentVersion(ctx, db)
			db.Close()
			if err != nil {
				sqliteDetail = fmt.Sprintf("version: %v", err)
			} else if v < store.SchemaVersion {
				sqliteDetail = fmt.Sprintf("schema stale (have %d, want %d)", v, store.SchemaVersion)
			} else {
				sqliteOK = true
				sqliteDetail = fmt.Sprintf("schema current (v%d)", v)
			}
		}
	}
	if sqliteOK {
		addCheck("sqlite_schema", "PASS", sqliteDetail)
		if !*jsonFlag {
			fmt.Printf("sqlite: %s\n", sqliteDetail)
		}
	} else {
		addCheck("sqlite_schema", "FAIL", sqliteDetail)
		if !*jsonFlag {
			fmt.Fprintf(os.Stderr, "sqlite: %v\n", sqliteDetail)
		}
	}

	// Check labels
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
	labelsOK := true
	missingLabels := []string{}
	if !*jsonFlag {
		fmt.Println("\nLabels:")
	}
	for _, repo := range repos {
		if !*jsonFlag {
			fmt.Printf("  %s:\n", repo)
		}
		for _, l := range labels {
			exists, err := trk.LabelExists(ctx, repo, l.name)
			if err != nil {
				labelsOK = false
				if *jsonFlag {
					missingLabels = append(missingLabels, fmt.Sprintf("%s/%s (error)", repo, l.name))
				} else {
					fmt.Fprintf(os.Stderr, "    %s: error: %v\n", l.name, err)
				}
				continue
			}
			if exists {
				if !*jsonFlag {
					fmt.Printf("    %s: present\n", l.name)
				}
			} else {
				labelsOK = false
				cmd := fmt.Sprintf("gh label create %s/%s --name \"%s\" --color \"%s\"", repo, l.name, l.name, l.color)
				if *jsonFlag {
					missingLabels = append(missingLabels, fmt.Sprintf("%s/%s", repo, l.name))
				} else {
					fmt.Printf("    %s: MISSING  (%s)\n", l.name, cmd)
				}
			}
		}
	}
	if labelsOK {
		addCheck("labels", "PASS", "all labels present")
	} else {
		addCheck("labels", "FAIL", fmt.Sprintf("missing labels: %v", missingLabels))
	}

	// Check config bounds (already validated by LoadRelaxed, but surface for completeness)
	addCheck("config_bounds", "PASS", "values within bounds")

	// Output results
	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		overall := "PASS"
		for _, r := range results {
			if r.Status == "FAIL" {
				overall = "FAIL"
				break
			}
		}
		result := map[string]interface{}{
			"checks": results,
			"overall": overall,
		}
		_ = enc.Encode(result)
		if overall == "FAIL" {
			return 1
		}
		return 0
	}

	// Human-readable output
	if !labelsOK {
		fmt.Println("\nConfiguration:")
		fmt.Print(config.Format(config.Redact(cfg)))
		return 1
	}

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
	cfg, err := config.LoadRelaxed(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

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

	repos, err := config.ParseRepos(cfg.Repos)
	if err != nil {
		log.Error("parse repos", "err", err)
		return 1
	}

	sweepRepos, err := config.ParseRepos(cfg.SweepRepos)
	if err != nil {
		log.Error("parse sweep repos", "err", err)
		return 1
	}

	dbPath := config.ExpandHome(cfg.DB)
	q := store.NewSQLiteQueue(dbPath, store.WithMaxAttempts(3))

	wm := store.NewSQLiteWatermarks(q.DB())

	hb := store.NewSQLiteHeartbeat(q.DB())

	trk := tracker.NewGitHub(cfg.GitHubAPI, cfg.GitHubToken, repos, wm)

	h := harness.NewRemoteHarness(cfg.HarnessURL, cfg.HarnessToken)

	idlePolicy := idle.NewHarnessIdlePolicy(cfg.HarnessURL, cfg.HarnessToken, cfg.YieldWhenBusy)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	defer func() { _ = hb.ReleaseRunLock(context.Background()) }()

	if acquired, err := hb.TakeRunLock(ctx); err != nil {
		log.Error("take run lock", "err", err)
		return 1
	} else if !acquired {
		log.Warn("run lock already held by another process")
		return 1
	}

	poller := tracker.NewPoller(trk, q, wm, repos, cfg.PollInterval,
		tracker.WithSweep(tracker.SweepConfig{
			Period:         cfg.SweepPeriod,
			Repos:          sweepRepos,
			Prompt:         cfg.SweepPrompt,
			TimeoutSeconds: int(cfg.TierTimeoutSweep / time.Second),
			Budget:         cfg.BudgetSweep,
		}))
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
				_ = hb.Beat(ctx, time.Now())
				for _, repo := range repos {
					_ = hb.SavePoll(ctx, repo, time.Now())
				}
				if err := poller.Sweep(ctx); err != nil {
					log.Error("sweep enqueue failed", "err", err)
				}
			}
		}
	}()

	w := worker.NewWorker(q, h, trk, idlePolicy, cfg.MaxConcurrent, cfg.RetryBackoffInitial, cfg.RetryBackoffMax, log, cfg.GlobalBudgetTokens, q)
	if err := w.Run(ctx); err != nil {
		log.Error("worker exited", "err", err)
		return 1
	}
	return 0
}

func runStatus(args []string) int {
	fs := flag.NewFlagSet("idle-deck status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonFlag := fs.Bool("json", false, "Output machine-readable JSON")
	flags := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()
	cfg, err := config.LoadRelaxed(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	dbPath := config.ExpandHome(cfg.DB)

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		if *jsonFlag {
			result := map[string]interface{}{
				"run_lock":        "none",
				"heartbeat":       nil,
				"queue": map[string]interface{}{
					"queued":    map[string]int{},
					"running":   map[string]int{},
					"succeeded": map[string]int{},
					"escalated": map[string]int{},
				},
				"polls":             []interface{}{},
				"active_task":       nil,
				"harness":           nil,
				"last_escalation":   nil,
				"global_spend":      nil,
				"message":           "no state yet (database not found)",
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(result)
			return 0
		}
		fmt.Println("idle-deck status")
		fmt.Println("================")
		fmt.Println("No state yet (database not found)")
		return 0
	} else if err != nil {
		fmt.Fprintf(os.Stderr, "status: stat DB: %v\n", err)
		return 1
	}

	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode=WAL")
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: open DB: %v\n", err)
		return 1
	}
	defer db.Close()
	hb := store.NewSQLiteHeartbeat(db)

	// Collect data
	beat, err := hb.LastBeat(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: heartbeat: %v\n", err)
		return 1
	}
	held, err := hb.RunLockHeld(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: run lock: %v\n", err)
		return 1
	}

	// Queue depth
	queueCounts := map[string]map[string]int{
		"queued":    {},
		"running":   {},
		"succeeded": {},
		"escalated": {},
	}
	rows, err := db.QueryContext(ctx, `SELECT state, tier, COUNT(*) FROM tasks GROUP BY state, tier ORDER BY state, tier`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: query queue: %v\n", err)
		return 1
	}
	for rows.Next() {
		var state, tier string
		var count int
		rows.Scan(&state, &tier, &count)
		if _, ok := queueCounts[state]; ok {
			queueCounts[state][tier] = count
		}
	}
	rows.Close()

	// Polls and watermarks
	type pollInfo struct {
		Repo      string `json:"repo"`
		Watermark string `json:"watermark"`
		LastPoll  string `json:"last_poll,omitempty"`
		Seeded    bool   `json:"seeded"`
	}
	var polls []pollInfo
	rows2, err := db.QueryContext(ctx, `SELECT repo, watermark FROM tracker_watermarks ORDER BY repo`)
	if err != nil {
		fmt.Fprintf(os.Stderr, "status: query watermarks: %v\n", err)
		return 1
	}
	for rows2.Next() {
		var repo, watermark string
		rows2.Scan(&repo, &watermark)
		lastPoll, seeded, err := hb.LastPoll(ctx, repo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "status: query poll: %v\n", err)
			return 1
		}
		info := pollInfo{
			Repo:      repo,
			Watermark: watermark,
			Seeded:    seeded,
		}
		if seeded {
			info.LastPoll = lastPoll.Format(time.RFC3339)
		}
		polls = append(polls, info)
	}
	rows2.Close()

	// Active task
	type activeTask struct {
		TaskID     string `json:"task_id"`
		Tier       string `json:"tier"`
		Role       string `json:"role"`
		AttemptID  string `json:"attempt_id"`
		RunningFor string `json:"running_for"`
	}
	var active *activeTask
	row := db.QueryRowContext(ctx, `
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
		active = &activeTask{
			TaskID:     taskID,
			Tier:       tier,
			Role:       string(queue.RoleForTier(queue.TaskTier(tier))),
			AttemptID:  attemptID,
			RunningFor: runningTime.Round(time.Second).String(),
		}
	} else if err != sql.ErrNoRows {
		fmt.Fprintf(os.Stderr, "status: query active task: %v\n", err)
		return 1
	}

	// Harness
	type harnessInfo struct {
		Reachable bool   `json:"reachable"`
		Busy      *bool  `json:"busy,omitempty"`
		Model     string `json:"model,omitempty"`
		Capacity  string `json:"capacity,omitempty"`
		Configured bool  `json:"configured"`
	}
	var harness *harnessInfo
	if cfg.HarnessURL != "" {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, cfg.HarnessURL+"/health", nil)
		if cfg.HarnessToken != "" {
			req.Header.Set("Authorization", "Bearer "+cfg.HarnessToken)
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				harness = &harnessInfo{Reachable: true, Configured: true}
				var health struct {
					Reachable *bool `json:"reachable"`
					Busy      *bool `json:"busy"`
					Model     string `json:"model"`
				}
				if err := json.Unmarshal(body, &health); err == nil {
					if health.Busy != nil {
						harness.Busy = health.Busy
						if *health.Busy {
							harness.Capacity = "busy"
						} else {
							harness.Capacity = "free"
						}
					} else {
						harness.Capacity = "free"
					}
					harness.Model = health.Model
				}
			} else {
				harness = &harnessInfo{Reachable: false, Configured: true}
			}
		} else {
			harness = &harnessInfo{Reachable: false, Configured: true}
		}
	} else {
		harness = &harnessInfo{Reachable: false, Configured: false}
	}

	// Most recent escalation
	type escalationInfo struct {
		TaskID string `json:"task_id"`
		Tier   string `json:"tier"`
		Time   string `json:"time"`
	}
	var lastEscalation *escalationInfo
	row2 := db.QueryRowContext(ctx, `
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
		lastEscalation = &escalationInfo{
			TaskID: escTaskID,
			Tier:   escTier,
			Time:   time.Unix(0, escFinishedAt).Format(time.RFC3339),
		}
	} else if err != sql.ErrNoRows {
		fmt.Fprintf(os.Stderr, "status: query escalation: %v\n", err)
		return 1
	}

	// Build result
	if *jsonFlag {
		runLockStatus := "free"
		if held {
			if beat.IsZero() || time.Since(beat) > 3*cfg.PollInterval {
				runLockStatus = "orphaned"
			} else {
				runLockStatus = "held"
			}
		}

		result := map[string]interface{}{
			"run_lock":          runLockStatus,
			"heartbeat":         nil,
			"queue":             queueCounts,
			"polls":             polls,
			"active_task":       active,
			"harness":           harness,
			"last_escalation":   lastEscalation,
			"config_redacted":   config.Redact(cfg),
			"global_spend":      nil,
		}
		if !beat.IsZero() {
			result["heartbeat"] = beat.Format(time.RFC3339)
		}

		// Query cumulative spend from spend_ledger
		var totalSpend int64
		err = db.QueryRowContext(ctx, `SELECT COALESCE(SUM(total_tokens), 0) FROM spend_ledger`).Scan(&totalSpend)
		if err == nil {
			ceiling := int64(cfg.GlobalBudgetTokens)
			enabled := cfg.GlobalBudgetTokens > 0
			remaining := int64(0)
			exhausted := false
			if enabled {
				if ceiling > 0 {
					remaining = ceiling - totalSpend
					if remaining < 0 {
						remaining = 0
					}
				}
				exhausted = totalSpend >= ceiling
			}
			result["global_spend"] = map[string]interface{}{
				"total_tokens": totalSpend,
				"ceiling":      ceiling,
				"enabled":      enabled,
				"remaining":    remaining,
				"exhausted":    exhausted,
			}
		}

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(result)
		return 0
	}

	// Human-readable output
	fmt.Println("idle-deck status")
	fmt.Println("================")

	if held {
		if beat.IsZero() || time.Since(beat) > 3*cfg.PollInterval {
			fmt.Println("Run lock: held but heartbeat stale (orphaned lock)")
		} else {
			fmt.Println("Run lock: held")
		}
	} else {
		fmt.Println("Run lock: free")
	}

	fmt.Println("\nQueue depth:")
	for _, state := range []string{"queued", "running", "succeeded", "escalated"} {
		for tier, count := range queueCounts[state] {
			fmt.Printf("  %s %s: %d\n", state, tier, count)
		}
	}

	fmt.Println("\nPolls and watermarks:")
	for _, p := range polls {
		if p.Seeded {
			fmt.Printf("  %s: watermark=%s  last poll=%s\n", p.Repo, p.Watermark, p.LastPoll)
		} else {
			fmt.Printf("  %s: watermark=%s  last poll: never\n", p.Repo, p.Watermark)
		}
	}

	fmt.Println("\nActive task:")
	if active != nil {
		fmt.Printf("  Task: %s\n", active.TaskID)
		fmt.Printf("  Tier: %s (role: %s)\n", active.Tier, active.Role)
		fmt.Printf("  Attempt: %s\n", active.AttemptID)
		fmt.Printf("  Running: %s\n", active.RunningFor)
	} else {
		fmt.Println("  None")
	}

	fmt.Println("\nHarness:")
	if harness.Configured {
		if harness.Reachable {
			fmt.Println("  Reachability: reachable")
			if harness.Capacity != "" {
				fmt.Printf("  Capacity:   %s", harness.Capacity)
				if harness.Busy != nil && *harness.Busy {
					fmt.Println(" (remote occupied by a run it is executing)")
				} else if cfg.YieldWhenBusy {
					fmt.Println(" (yield-when-busy is on, but the remote is not busy)")
				} else {
					fmt.Println(" (IDLE_DECK_YIELD_WHEN_BUSY is off — busy answers are ignored)")
				}
			}
			if harness.Model != "" {
				fmt.Printf("  Model:      %s\n", harness.Model)
			}
		} else {
			fmt.Println("  Reachability: unreachable")
		}
	} else {
		fmt.Println("  Reachability: not configured (IDLE_DECK_HARNESS_URL not set)")
	}

	fmt.Println("\nMost recent escalation:")
	if lastEscalation != nil {
		fmt.Printf("  Task: %s\n", lastEscalation.TaskID)
		fmt.Printf("  Tier: %s\n", lastEscalation.Tier)
		fmt.Printf("  Time: %s\n", lastEscalation.Time)
	} else {
		fmt.Println("  None")
	}

	return 0
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("idle-deck serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := config.RegisterFlags(fs)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.LoadRelaxed(flags)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	ctx := context.Background()
	dbPath := config.ExpandHome(cfg.DB)
	db, err := store.Open(ctx, dbPath) // read-write; runs forward migrations (v10)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer db.Close()

	// Tracker for the questions section (nil when not configured, so serve
	// still works with DB alone).
	var trk dashboard.TrackerIssueLister
	if cfg.GitHubToken != "" && cfg.Repos != "" {
		repos, err := config.ParseRepos(cfg.Repos)
		if err != nil {
			fmt.Fprintln(os.Stderr, "parse repos: "+err.Error())
			return 1
		}
		wm := store.NewSQLiteWatermarks(db)
		trk = tracker.NewGitHub(cfg.GitHubAPI, cfg.GitHubToken, repos, wm)
	}

	srv, err := dashboard.NewServer(cfg, db, trk)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer srv.Close()
	if err := srv.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, "serve: ", err)
		return 1
	}
	return 0
}

func runSecrets(args []string) int {
	if len(args) < 1 {
		secretsUsage()
		return 2
	}

	// Validate key if present
	if len(args) >= 2 {
		if !config.IsValidKey(args[1]) {
			fmt.Fprintf(os.Stderr, "invalid key: %q (valid: %s, %s)\n",
				args[1], config.KeyGitHubToken, config.KeyHarnessToken)
			return 2
		}
	}

	switch args[0] {
	case "set":
		return runSecretsSet(args[1:])
	case "get":
		return runSecretsGet(args[1:])
	case "delete":
		return runSecretsDelete(args[1:])
	case "list":
		return runSecretsList()
	default:
		fmt.Fprintf(os.Stderr, "unknown secrets command: %q\n", args[0])
		secretsUsage()
		return 2
	}
}

func secretsUsage() {
	fmt.Fprintln(os.Stderr, "usage: idle-deck secrets <command> [args]")
	fmt.Fprintln(os.Stderr, "commands:")
	fmt.Fprintln(os.Stderr, "  set <key> [value]    store a secret (reads from stdin if value omitted)")
	fmt.Fprintln(os.Stderr, "  get <key>            retrieve a secret (prints raw value to stdout)")
	fmt.Fprintln(os.Stderr, "  delete <key>         remove a secret")
	fmt.Fprintln(os.Stderr, "  list                 list stored keys (no values)")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintf(os.Stderr, "valid keys: %s, %s\n", config.KeyGitHubToken, config.KeyHarnessToken)
}

func runSecretsSet(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: idle-deck secrets set <key> [value]")
		return 2
	}
	key := args[0]
	value := ""
	if len(args) >= 2 {
		value = args[1]
	} else {
		// Read from stdin
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
			return 1
		}
		value = strings.TrimSpace(string(data))
	}
	if value == "" {
		fmt.Fprintln(os.Stderr, "secret value is empty")
		return 1
	}

	if config.Keychain == nil {
		// Non-macOS: print visible notice, succeed
		fmt.Fprintf(os.Stderr, "notice: Keychain not available on %s; secret not stored. Use environment variables instead.\n", runtime.GOOS)
		return 0
	}

	if err := config.Keychain.Set(key, value); err != nil {
		fmt.Fprintf(os.Stderr, "store secret: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "stored %s\n", key)
	return 0
}

func runSecretsGet(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: idle-deck secrets get <key>")
		return 2
	}
	key := args[0]

	if config.Keychain == nil {
		fmt.Fprintln(os.Stderr, "secret not found")
		return 1
	}

	value, err := config.Keychain.Get(key)
	if err != nil {
		if errors.Is(err, config.ErrSecretNotFound) {
			fmt.Fprintln(os.Stderr, "secret not found")
			return 1
		}
		fmt.Fprintf(os.Stderr, "get secret: %v\n", err)
		return 1
	}
	fmt.Print(value)
	return 0
}

func runSecretsDelete(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: idle-deck secrets delete <key>")
		return 2
	}
	key := args[0]

	if config.Keychain == nil {
		// Non-macOS: quiet no-op success
		return 0
	}

	if err := config.Keychain.Delete(key); err != nil {
		fmt.Fprintf(os.Stderr, "delete secret: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "deleted %s\n", key)
	return 0
}

func runSecretsList() int {
	if config.Keychain == nil {
		// Non-macOS: quiet no-op, no output
		return 0
	}

	keys, err := config.Keychain.List()
	if err != nil {
		fmt.Fprintf(os.Stderr, "list secrets: %v\n", err)
		return 1
	}
	for _, k := range keys {
		fmt.Println(k)
	}
	return 0
}
