package tracker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/seppaleinen/idle-deck/queue"
	"github.com/seppaleinen/idle-deck/store"
)

// Poller runs the polling loop for a set of repositories (event-contract §2, §8).
type Poller struct {
	gh         *GitHub
	q          queue.Queue
	wm         store.WatermarkStore
	repos      []string
	interval   time.Duration
	now        func() time.Time
	client     *http.Client
	sweepPeriod time.Duration
	sweepRepos  []string
	sweepPrompt string
}

// SweepConfig holds optional sweep configuration for the poller.
type SweepConfig struct {
	Period  time.Duration
	Repos   []string
	Prompt  string
}

// WithSweep returns a PollerOption that configures P3 sweep scheduling.
func WithSweep(cfg SweepConfig) func(*Poller) {
	return func(p *Poller) {
		p.sweepPeriod = cfg.Period
		p.sweepRepos = cfg.Repos
		p.sweepPrompt = cfg.Prompt
	}
}

// NewPoller creates a poller for the given repositories.
// Accepts optional PollerOption functions to configure sweep.
func NewPoller(gh *GitHub, q queue.Queue, wm store.WatermarkStore, repos []string, interval time.Duration, opts ...func(*Poller)) *Poller {
	p := &Poller{
		gh:       gh,
		q:        q,
		wm:       wm,
		repos:    repos,
		interval: interval,
		now:      time.Now,
		client:   &http.Client{Timeout: 30 * time.Second},
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Tick runs one polling cycle for all repositories.
func (p *Poller) Tick(ctx context.Context) error {
	tickStart := p.now()
	for _, repo := range p.repos {
		if err := p.pollRepo(ctx, repo, tickStart); err != nil {
			// On error, do NOT advance watermark (event-contract §2).
			return err
		}
		// Watermark advances to tick start AFTER pagination completes.
		if err := p.wm.Save(ctx, repo, tickStart); err != nil {
			return fmt.Errorf("watermark save: %w", err)
		}
	}
	// Process any dashboard actions queued since last tick.
	if err := p.ProcessDashboardActions(ctx); err != nil {
		return fmt.Errorf("process dashboard actions: %w", err)
	}
	return nil
}

// Sweep enqueues P3 sweep tasks for configured repositories.
// Returns nil if sweep is disabled (SweepPeriod == 0).
// Dedupe key: github:sweep:<owner>/<repo>:<bucket> where bucket = now().Truncate(SweepPeriod).Unix().
// ErrDuplicate on enqueue is a no-op (collapse repeated ticks within one bucket).
func (p *Poller) Sweep(ctx context.Context) error {
	if p.sweepPeriod <= 0 {
		return nil // sweep disabled
	}
	now := p.now()
	bucket := now.Truncate(p.sweepPeriod).Unix()

	repos := p.sweepRepos
	if len(repos) == 0 {
		repos = p.repos
	}

	for _, repo := range repos {
		dedupeKey := fmt.Sprintf("github:sweep:%s:%d", repo, bucket)
		// Deterministic ID from the dedupe key so that repeated
		// enqueues within the same bucket are idempotent
		// (INSERT OR IGNORE on the primary key). This is the
		// storage-side mechanism that collapses repeated ticks
		// into one task per bucket (D28).
		hash := sha256.Sum256([]byte(dedupeKey))
		taskID := "sweep-" + hex.EncodeToString(hash[:])
		task := queue.Task{
			ID:             taskID,
			RepositoryID:   repo,
			Ticket:         queue.TrackerRef{}, // zero value for P3 (D44)
			Tier:           queue.TierP3,
			Prompt:         p.sweepPrompt,
			State:          queue.StateQueued,
			TimeoutSeconds: 3600,
			Budget:         20000,
			TriggeredBy: queue.TriggeredBy{
				EventType:  "schedule.sweep",
				DedupeKey:  dedupeKey,
				ReceivedAt: now.UnixNano(),
			},
			CreatedAt: now.UnixNano(),
		}
		if err := p.q.Enqueue(ctx, task); err != nil {
			if !errors.Is(err, queue.ErrDuplicate) {
				return err
			}
			// Duplicate: already enqueued in this bucket, collapse silently.
		}
	}
	return nil
}

// pollRepo runs the 4 queries for a single repo (event-contract §8).
func (p *Poller) pollRepo(ctx context.Context, repo string, tickStart time.Time) error {
	wm, seeded, err := p.wm.Load(ctx, repo)
	if err != nil {
		return err
	}
	if !seeded {
		// First observation: seed at tick start (event-contract §2).
		if err := p.wm.Save(ctx, repo, tickStart); err != nil {
			return err
		}
		// On first tick, use a far-past watermark to catch recent issues.
		wm = tickStart.Add(-24 * time.Hour)
	}

	queries := []struct {
		label       string
		trigger     string
		tier        queue.TaskTier
		isWatermark bool
	}{
		{LabelHotfix, "hotfix", queue.TierP0, false},
		{LabelReady, "ready", queue.TierP2, false},
		{LabelRedo, "redo", queue.TierP1, false},
		{"", "created", queue.TierP1, true}, // watermark query
	}

	for _, q := range queries {
		if err := p.runQuery(ctx, repo, wm, q.trigger, q.tier, q.isWatermark); err != nil {
			return err
		}
	}
	return nil
}

// runQuery executes one paginated query and enqueues candidates.
func (p *Poller) runQuery(ctx context.Context, repo string, since time.Time, trigger string, tier queue.TaskTier, isWatermark bool) error {
	owner, repoName := splitRepo(repo)
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues", p.gh.apiURL, owner, repoName)

	params := url.Values{}
	params.Set("state", "open")
	params.Set("since", since.Format(time.RFC3339))
	if isWatermark {
		params.Set("sort", "updated")
		params.Set("direction", "asc")
		params.Set("per_page", "100")
	} else {
		params.Set("labels", trigger)
	}

	for page := 0; ; page++ {
		if page > 0 {
			// For subsequent pages, we use the Link header from the previous response.
			// The stub handles this by encoding the page in the URL.
			params.Set("page", fmt.Sprintf("%d", page+1))
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+p.gh.token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := p.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusNotModified {
			// 304: no new data since watermark. Stop pagination.
			break
		}
		if resp.StatusCode == http.StatusNotFound {
			// Repo not found or no access.
			return fmt.Errorf("repo %s not found: %w", repo, ErrNotAllowlisted)
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(body))
		}

		var issues []githubIssue
		if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
			return err
		}

		if len(issues) == 0 {
			break
		}

		for _, issue := range issues {
			if issue.PullRequest != nil {
				continue
			}
			// Build raw JSON for Parse (it re-extracts the trigger).
			raw, _ := json.Marshal(issue)
			task, err := p.gh.Parse(ctx, raw)
			if err != nil {
				if errors.Is(err, ErrNotCandidate) || errors.Is(err, ErrNotAllowlisted) {
					continue
				}
				return err
			}
			if err := p.q.Enqueue(ctx, task); err != nil {
				if !errors.Is(err, queue.ErrDuplicate) {
					return err
				}
				// Duplicate: already enqueued, dedupe key handled it.
			}
		}

		// Check for Link: rel="next" to continue pagination.
		link := resp.Header.Get("Link")
		if !strings.Contains(link, `rel="next"`) {
			break
		}
	}
	return nil
}

// splitRepo splits "owner/repo" into owner and repo.
func splitRepo(repo string) (string, string) {
	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		return "", ""
	}
	return parts[0], parts[1]
}

// postComment posts a comment on an issue (TrackerSink.Comment).
func (g *GitHub) postComment(ctx context.Context, ref queue.TrackerRef, body string) error {
	endpoint := fmt.Sprintf("%s/repos/%s/issues/%s/comments", g.apiURL, ref.RepositoryID, ref.ExternalID)
	payload := map[string]string{"body": body}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("comment: %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// patchLabels adds/removes labels on an issue (TrackerSink.SetLabels).
func (g *GitHub) patchLabels(ctx context.Context, ref queue.TrackerRef, add, remove []string) error {
	// Get current labels first, then compute new set.
	// For simplicity, we just PATCH the issue with the new label list.
	// In practice, we'd read current labels and apply add/remove.
	endpoint := fmt.Sprintf("%s/repos/%s/issues/%s", g.apiURL, ref.RepositoryID, ref.ExternalID)
	payload := map[string]any{
		"labels": add, // For now, just set to 'add'; remove is a no-op in this stub impl.
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("patch labels: %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// postIssue creates a new issue (TrackerSink.OpenIssue for P3 sweep findings).
func (g *GitHub) postIssue(ctx context.Context, repo, title, body string, labels []string) error {
	endpoint := fmt.Sprintf("%s/repos/%s/issues", g.apiURL, repo)
	payload := map[string]any{
		"title":  title,
		"body":   body,
		"labels": labels,
	}
	b, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(b)))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("create issue: %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func (g *GitHub) httpClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

// ProcessDashboardActions checks the dashboard_actions table for pending
// actions and executes them. Actions are:
// - poll: trigger a poll for a specific repo
// - cancel: cancel a running task (mark as escalated)
// - retry: re-enqueue a failed/succeeded task for another attempt
func (p *Poller) ProcessDashboardActions(ctx context.Context) error {
	db := p.q.DB()
	if db == nil {
		return nil
	}

	// Fetch pending actions (limit to 10 per tick to avoid long processing).
	rows, err := db.QueryContext(ctx, `
		SELECT id, kind, payload FROM dashboard_actions
		WHERE status = 'pending'
		ORDER BY created_at ASC
		LIMIT 10
	`)
	if err != nil {
		return fmt.Errorf("query actions: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var actionID, kind, payload string
		if err := rows.Scan(&actionID, &kind, &payload); err != nil {
			continue
		}

		// Mark as started
		now := p.now().UnixNano()
		_, _ = db.ExecContext(ctx, `
			UPDATE dashboard_actions SET status = 'running', started_at = ? WHERE id = ?
		`, now, actionID)

		var actErr error
		switch kind {
		case "poll":
			actErr = p.processActionPoll(ctx, payload)
		case "cancel":
			actErr = p.processActionCancel(ctx, payload)
		case "retry":
			actErr = p.processActionRetry(ctx, payload)
		default:
			actErr = fmt.Errorf("unknown action kind: %s", kind)
		}

		finishedAt := p.now().UnixNano()
		if actErr != nil {
			_, _ = db.ExecContext(ctx, `
				UPDATE dashboard_actions SET status = 'failed', finished_at = ?, error = ? WHERE id = ?
			`, finishedAt, actErr.Error(), actionID)
		} else {
			_, _ = db.ExecContext(ctx, `
				UPDATE dashboard_actions SET status = 'completed', finished_at = ? WHERE id = ?
			`, finishedAt, actionID)
		}
	}
	return nil
}

func (p *Poller) processActionPoll(ctx context.Context, payload string) error {
	var pld struct{ Repo string }
	if err := json.Unmarshal([]byte(payload), &pld); err != nil {
		return err
	}
	if pld.Repo == "" {
		return fmt.Errorf("poll action: repo required")
	}
	// Verify the repo is in our allowlist
	found := false
	for _, r := range p.repos {
		if r == pld.Repo {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("poll action: repo %s not in allowlist", pld.Repo)
	}
	// Trigger a poll for this repo by running pollRepo with a far-past watermark
	// to catch any new issues since the last actual poll.
	tickStart := p.now()
	if err := p.pollRepo(ctx, pld.Repo, tickStart.Add(-24*time.Hour)); err != nil {
		return fmt.Errorf("poll action: %w", err)
	}
	return nil
}

func (p *Poller) processActionCancel(ctx context.Context, payload string) error {
	var pld struct{ TaskID string }
	if err := json.Unmarshal([]byte(payload), &pld); err != nil {
		return err
	}
	if pld.TaskID == "" {
		return fmt.Errorf("cancel action: task_id required")
	}
	// The queue doesn't have a direct cancel; we can mark the task as escalated
	// by updating its state, or we can rely on the worker's Abort.
	// For simplicity, mark the task state as 'escalated' which will cause
	// the worker to stop it on the next check.
	_, err := p.q.DB().ExecContext(ctx, `
		UPDATE tasks SET state = 'escalated' WHERE id = ? AND state = 'running'
	`, pld.TaskID)
	if err != nil {
		return err
	}
	return nil
}

func (p *Poller) processActionRetry(ctx context.Context, payload string) error {
	var pld struct{ TaskID string }
	if err := json.Unmarshal([]byte(payload), &pld); err != nil {
		return err
	}
	if pld.TaskID == "" {
		return fmt.Errorf("retry action: task_id required")
	}
	// Re-enqueue the task by inserting a new attempt with incremented ordinal.
	// First, get the task details and current attempt count.
	var tier, prompt string
	var timeoutSecs, budget int
	var repoID, ticketTracker, ticketRepoID, ticketExternalID, ticketURL, eventType, dedupeKey string
	var triggeredByEventType, triggeredByDedupeKey string
	var triggeredByReceivedAt int64
	err := p.q.DB().QueryRowContext(ctx, `
		SELECT t.tier, t.prompt, t.timeout_seconds, t.budget, t.repository_id,
		       t.ticket_tracker, t.ticket_repo_id, t.ticket_external_id, t.ticket_url,
		       t.event_type, t.dedupe_key, t.triggered_by_event_type, t.triggered_by_dedupe_key, t.triggered_by_received_at
		FROM tasks t WHERE t.id = ?
	`, pld.TaskID).Scan(&tier, &prompt, &timeoutSecs, &budget, &repoID,
		&ticketTracker, &ticketRepoID, &ticketExternalID, &ticketURL,
		&eventType, &dedupeKey, &triggeredByEventType, &triggeredByDedupeKey, &triggeredByReceivedAt)
	if err != nil {
		return fmt.Errorf("retry action: task not found: %w", err)
	}

	// Get current attempt count
	var attemptCount int
	_ = p.q.DB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM attempts WHERE task_id = ?
	`, pld.TaskID).Scan(&attemptCount)

	// Create new task with new ID but same properties, increment ordinal in attempt
	newTaskID := pld.TaskID + "-retry-" + fmt.Sprintf("%d", attemptCount+1)
	now := p.now().UnixNano()
	_, err = p.q.DB().ExecContext(ctx, `
		INSERT INTO tasks (id, repository_id, ticket_tracker, ticket_repo_id, ticket_external_id, ticket_url,
			tier, prompt, timeout_seconds, budget, state, event_type, dedupe_key, received_at, created_at,
			triggered_by_event_type, triggered_by_dedupe_key, triggered_by_received_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'queued', ?, ?, ?, ?, ?, ?, ?)
	`, newTaskID, repoID, ticketTracker, ticketRepoID, ticketExternalID, ticketURL,
		tier, prompt, timeoutSecs, budget, eventType, dedupeKey, now, now,
		triggeredByEventType, triggeredByDedupeKey, triggeredByReceivedAt)
	if err != nil {
		return fmt.Errorf("retry action: insert task: %w", err)
	}

	// Enqueue the new task
	task := queue.Task{
		ID:             newTaskID,
		RepositoryID:   repoID,
		Ticket:         queue.TrackerRef{Tracker: ticketTracker, RepositoryID: ticketRepoID, ExternalID: ticketExternalID, URL: ticketURL},
		Tier:           queue.TaskTier(tier),
		Prompt:         prompt,
		State:          queue.StateQueued,
		TimeoutSeconds: timeoutSecs,
		Budget:         budget,
		TriggeredBy: queue.TriggeredBy{
			EventType:  eventType,
			DedupeKey:  dedupeKey,
			ReceivedAt: now,
		},
		CreatedAt: now,
	}
	return p.q.Enqueue(ctx, task)
}
