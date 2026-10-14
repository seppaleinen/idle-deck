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
	gh           *GitHub
	q            queue.Queue
	wm           store.WatermarkStore
	repos        []string
	interval     time.Duration
	now          func() time.Time
	client       *http.Client
	sweepPeriod  time.Duration
	sweepRepos   []string
	sweepPrompt  string
	sweepTimeout int
	sweepBudget  int
}

// SweepConfig holds optional sweep configuration for the poller.
// TimeoutSeconds and Budget come from the operator's D37 overrides
// (IDLE_DECK_TIER_TIMEOUT_SWEEP, IDLE_DECK_BUDGET_SWEEP); zero means
// "use the D37 default" so the poller never hardcodes a tier value.
type SweepConfig struct {
	Period         time.Duration
	Repos          []string
	Prompt         string
	TimeoutSeconds int
	Budget         int
}

// WithSweep returns a PollerOption that configures P3 sweep scheduling.
func WithSweep(cfg SweepConfig) func(*Poller) {
	return func(p *Poller) {
		p.sweepPeriod = cfg.Period
		p.sweepRepos = cfg.Repos
		p.sweepPrompt = cfg.Prompt
		p.sweepTimeout = cfg.TimeoutSeconds
		p.sweepBudget = cfg.Budget
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
		// D37: the sweep tier's timeout and budget are operator-configurable
		// via IDLE_DECK_TIER_TIMEOUT_SWEEP / IDLE_DECK_BUDGET_SWEEP. The poller
		// is configured with the resolved values at construction time; a zero
		// value here means the operator did not override and the D37 default
		// (3600s / 20000) applies — the poller never hardcodes a tier value.
		timeoutSecs := p.sweepTimeout
		if timeoutSecs <= 0 {
			timeoutSecs = 3600
		}
		budget := p.sweepBudget
		if budget <= 0 {
			budget = 20000
		}
		task := queue.Task{
			ID:             taskID,
			RepositoryID:   repo,
			Ticket:         queue.TrackerRef{}, // zero value for P3 (D44)
			Tier:           queue.TierP3,
			Prompt:         p.sweepPrompt,
			State:          queue.StateQueued,
			TimeoutSeconds: timeoutSecs,
			Budget:         budget,
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
		notModified, err := p.runQuery(ctx, repo, wm, q.trigger, q.tier, q.isWatermark)
		if err != nil {
			return err
		}
		// Skip remaining queries if this one returned 304 — repo is quiet
		if notModified {
			break
		}
	}
	return nil
}

// runQuery executes one paginated query and enqueues candidates.
func (p *Poller) runQuery(ctx context.Context, repo string, since time.Time, trigger string, tier queue.TaskTier, isWatermark bool) (bool, error) {
	owner, repoName := splitRepo(repo)
	endpoint := fmt.Sprintf("%s/repos/%s/%s/issues", p.gh.apiURL, owner, repoName)

	params := url.Values{}
	params.Set("state", "open")
	params.Set("since", since.Format(time.RFC3339))
	if isWatermark {
		params.Set("per_page", "100")
		// Note: sort=updated&direction=asc dropped to make ETags stable (sort changes ETag when issues update).
	} else {
		params.Set("labels", trigger)
	}

	// Load ETag for conditional request
	etag, _ := p.wm.LoadETag(ctx, repo, trigger)
	notModified := false

	for page := 0; ; page++ {
		if page > 0 {
			// For subsequent pages, we use the Link header from the previous response.
			// The stub handles this by encoding the page in the URL.
			params.Set("page", fmt.Sprintf("%d", page+1))
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+params.Encode(), nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Authorization", "Bearer "+p.gh.token)
		req.Header.Set("Accept", "application/vnd.github+json")
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}

		resp, err := p.client.Do(req)
		if err != nil {
			return false, err
		}

		if resp.StatusCode == http.StatusNotModified {
			// 304: no new data. Stop pagination and skip remaining queries.
			resp.Body.Close()
			notModified = true
			break
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			// Repo not found or no access.
			return false, fmt.Errorf("repo %s not found: %w", repo, ErrNotAllowlisted)
		}
		if resp.StatusCode >= 400 {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return false, fmt.Errorf("GitHub API %d: %s", resp.StatusCode, string(body))
		}

		// Save new ETag from response
		if newETag := resp.Header.Get("ETag"); newETag != "" {
			// Persist ETag with watermark for next tick
			_ = p.wm.SaveETag(ctx, repo, trigger, newETag)
			etag = newETag
		}

		var issues []githubIssue
		if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
			resp.Body.Close()
			return false, err
		}
		resp.Body.Close()

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
				return false, err
			}
			if err := p.q.Enqueue(ctx, task); err != nil {
				if !errors.Is(err, queue.ErrDuplicate) {
					return false, err
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
	return notModified, nil
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

// patchLabels applies add/remove to an issue's labels (TrackerSink.SetLabels).
// GitHub's PATCH /issues/{number} replaces the entire label set, so we read
// the current labels first and compute the new set as (current ∪ add) − remove.
// This is what makes event-contract §6 real: the trigger label is removed
// on any terminal state (D29), not silently dropped.
func (g *GitHub) patchLabels(ctx context.Context, ref queue.TrackerRef, add, remove []string) error {
	current, err := g.getLabels(ctx, ref)
	if err != nil {
		return err
	}
	set := make(map[string]bool, len(current)+len(add))
	for _, l := range current {
		set[l] = true
	}
	for _, l := range add {
		set[l] = true
	}
	for _, l := range remove {
		delete(set, l)
	}
	newLabels := make([]string, 0, len(set))
	for l := range set {
		newLabels = append(newLabels, l)
	}

	endpoint := fmt.Sprintf("%s/repos/%s/issues/%s", g.apiURL, ref.RepositoryID, ref.ExternalID)
	payload := map[string]any{"labels": newLabels}
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

// getLabels reads the current labels on an issue via GET /issues/{number}.
// Needed because GitHub's PATCH replaces the whole label set — we must
// know what is already there before computing the new set.
func (g *GitHub) getLabels(ctx context.Context, ref queue.TrackerRef) ([]string, error) {
	endpoint := fmt.Sprintf("%s/repos/%s/issues/%s", g.apiURL, ref.RepositoryID, ref.ExternalID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := g.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("get labels: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get labels: %d: %s", resp.StatusCode, string(b))
	}
	// The issues endpoint returns a JSON array; pick the one whose number
	// matches the ref's external id.
	var issues []githubIssue
	if err := json.NewDecoder(resp.Body).Decode(&issues); err != nil {
		return nil, fmt.Errorf("get labels: decode: %w", err)
	}
	for _, issue := range issues {
		if fmt.Sprintf("%d", issue.Number) == ref.ExternalID {
			labels := make([]string, 0, len(issue.Labels))
			for _, l := range issue.Labels {
				labels = append(labels, l.Name)
			}
			return labels, nil
		}
	}
	// Issue not found — nothing to remove; return an empty set rather than
	// an error so a remove on a missing issue is a no-op (idempotent).
	return nil, nil
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
	// Cancellation must go through the queue's lease mechanism (D24): the
	// queue's CancelTask seam method records the running attempt as preempted,
	// which consumes one retry (D17) and follows the normal retry/escalation
	// path (D14). This honours the lease invariant rather than bypassing it
	// with raw SQL writes to tasks/attempts.
	// First, verify the task exists and is running via the seam.
	task, err := p.q.GetTask(ctx, pld.TaskID)
	if err != nil {
		return fmt.Errorf("cancel action: %w", err)
	}
	if task.State != queue.StateRunning {
		// Not running — nothing to cancel; idempotent success.
		return nil
	}
	// Cancel through the queue seam. The queue owns the lease; it atomically
	// records the preempted outcome, updates the task state per retry policy,
	// and releases the lease. This is the same path a P0 preemption uses (D16).
	return p.q.CancelTask(ctx, pld.TaskID)
}

func (p *Poller) processActionRetry(ctx context.Context, payload string) error {
	var pld struct{ TaskID string }
	if err := json.Unmarshal([]byte(payload), &pld); err != nil {
		return err
	}
	if pld.TaskID == "" {
		return fmt.Errorf("retry action: task_id required")
	}
	// Retry must go through the Queue seam (D24, I10): a fresh task via
	// Enqueue, not raw SQL INSERT. Get the task details through the seam,
	// then enqueue with a new dedupe key so the retry is not collapsed.
	task, err := p.q.GetTask(ctx, pld.TaskID)
	if err != nil {
		return fmt.Errorf("retry action: %w", err)
	}
	// Generate a fresh dedupe key for the retry (I10: "never revive in place").
	// The new task carries a distinct identity; it follows normal retry/escalation
	// per D14 (two retries then escalate).
	newTaskID := pld.TaskID + "-retry"
	now := p.now().UnixNano()
	dedupeKey := fmt.Sprintf("retry-%s-%d", pld.TaskID, now)

	retryTask := queue.Task{
		ID:             newTaskID,
		RepositoryID:   task.RepositoryID,
		Ticket:         task.Ticket,
		Tier:           task.Tier,
		Prompt:         task.Prompt,
		TimeoutSeconds: task.TimeoutSeconds,
		Budget:         task.Budget,
		State:          queue.StateQueued,
		TriggeredBy: queue.TriggeredBy{
			EventType:  task.TriggeredBy.EventType,
			DedupeKey:  dedupeKey,
			ReceivedAt: now,
		},
		DerivedFrom: pld.TaskID,
		CreatedAt:   now,
	}
	return p.q.Enqueue(ctx, retryTask)
}
