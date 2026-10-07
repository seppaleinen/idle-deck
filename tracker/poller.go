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
