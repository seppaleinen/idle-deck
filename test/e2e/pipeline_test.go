package e2e

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/seppaleinen/idle-deck/harness"
	"github.com/seppaleinen/idle-deck/queue"
	"github.com/seppaleinen/idle-deck/remote"
	"github.com/seppaleinen/idle-deck/store"
	"github.com/seppaleinen/idle-deck/worker"
	"log/slog"
)

// stubSink implements worker.TrackerSink with no-ops.
type stubSink struct{}

func (s *stubSink) Comment(ctx context.Context, ref queue.TrackerRef, body string) error { return nil }
func (s *stubSink) SetLabels(ctx context.Context, ref queue.TrackerRef, add, remove []string) error { return nil }
func (s *stubSink) OpenIssue(ctx context.Context, repo string, title string, body string, labels []string) error { return nil }

// stubPolicy implements worker.IdlePolicy and always returns idle.
type stubPolicy struct{}

func (p *stubPolicy) Idle(ctx context.Context) (bool, error) { return true, nil }

// stubPR implements remote.PRCreator and returns a dummy PR URL.
type stubPR struct{}

func (s *stubPR) CreatePR(_ context.Context, owner, repo, head, base, title, body string) (string, error) {
	return "https://github.com/" + owner + "/" + repo + "/pull/1", nil
}

// newUpstreamRepo creates a local git repository with one initial commit.
// Copied from remote/workspace_test.go.
func newUpstreamRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = env
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	run("add", "-A")
	run("commit", "-m", "initial")
	return dir
}

// TestPipeline_E2E exercises the real remote wrapper against the real worker loop using test/fakeacp.
func TestPipeline_E2E(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 1. Build fakeacp
	binPath := filepath.Join(t.TempDir(), "fakeacp")
	buildCmd := exec.Command("go", "build", "-o", binPath, "github.com/seppaleinen/idle-deck/test/fakeacp")
	out, err := buildCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build fakeacp: %v\n%s", err, out)
	}

	// 2. Create upstream repo
	upstreamDir := newUpstreamRepo(t)

	// 3. Setup server components
	bareDir := t.TempDir()
	baseDir := t.TempDir()
	wm := remote.NewWorkspaceManager(bareDir, baseDir, "tok-e2e")
	wm.PR = &stubPR{}

	runner := remote.NewACP(remote.ACPConfig{
		Bin: binPath,
		Env: []string{"FAKE_ACP_MODE=ok"},
	})

	srv := remote.NewServer(runner, func() (bool, string) { return true, "fake-model" }, "", bareDir, baseDir, "tok-e2e")
	srv.Config.WorkspaceManager = wm
	// Note: srv.HTTPToken is already "" from NewServer, no bearer auth needed

	// 4. Start HTTP server
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	srvHTTP := &http.Server{Handler: srv.Routes()}
	go srvHTTP.Serve(listener)
	t.Cleanup(func() { srvHTTP.Close() })

	baseURL := "http://" + addr

	// 5. Setup harness, queue, sink, policy
	h := harness.NewRemoteHarness(baseURL, "")

	dbPath := filepath.Join(t.TempDir(), "queue.db")
	q := store.NewSQLiteQueue(dbPath, store.WithMaxAttempts(3))

	sink := &stubSink{}
	policy := &stubPolicy{}

	// 6. Enqueue P1 task. RepositoryURL is the clone target the remote
	// needs (tokenURL passes local paths through unchanged); it is what
	// the tracker would derive from apiURL + owner/repo. RepositoryID
	// stays the identity key.
	task := queue.Task{
		ID:           "e2e-task-1",
		RepositoryID: "local/test-repo",
		RepositoryURL: upstreamDir,
		Ticket: queue.TrackerRef{
			Tracker:      "github",
			RepositoryID: "local/test-repo",
			ExternalID:   "42",
			URL:          "https://github.com/local/test-repo/issues/42",
		},
		Tier:   queue.TierP1,
		Prompt: "test pipeline",
		State:  queue.StateQueued,
		TriggeredBy: queue.TriggeredBy{
			EventType: "issue.opened",
			DedupeKey: "github:issue:local/test-repo:42:created",
		},
	}
	if err := q.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// 7. Run worker
	w := worker.NewWorker(q, h, sink, policy, 1, 60*time.Second, 5*time.Minute, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	go w.Run(ctx)

	// 8. Wait for terminal state
	deadline := time.Now().Add(80 * time.Second)
	var finalTask queue.Task
	for time.Now().Before(deadline) {
		tk, err := q.GetTask(ctx, "e2e-task-1")
		if err == nil {
			finalTask = tk
			if tk.State == queue.StateSucceeded || tk.State == queue.StateEscalated {
				t.Logf("task state: %s", tk.State)
				break
			}
		}
		t.Logf("waiting for task state, current: %v", tk.State)
		time.Sleep(5 * time.Second)
	}

	// 9. Assertions
	if finalTask.State != queue.StateSucceeded {
		t.Fatalf("task state: got %s, want %s", finalTask.State, queue.StateSucceeded)
	}

	// Check attempt count and outcome via DB
	var attemptCount int
	var attemptOutcome string
	row := q.DB().QueryRowContext(ctx, `SELECT COUNT(*), outcome FROM attempts WHERE task_id = ? AND outcome != ''`, "e2e-task-1")
	if err := row.Scan(&attemptCount, &attemptOutcome); err != nil {
		t.Fatalf("query attempts: %v", err)
	}
	if attemptCount < 1 {
		t.Fatalf("expected >= 1 completed attempt, got %d", attemptCount)
	}
	if attemptOutcome != "succeeded" {
		t.Fatalf("attempt outcome: got %s, want succeeded", attemptOutcome)
	}

	// Check artifacts saved
	var artifactCount int
	if err := q.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE attempt_id IN (SELECT id FROM attempts WHERE task_id = ?)`, "e2e-task-1").Scan(&artifactCount); err != nil {
		t.Fatalf("query artifacts: %v", err)
	}
	if artifactCount < 1 {
		t.Fatalf("expected >= 1 artifact, got %d", artifactCount)
	}

	// Check feature branch exists in upstream
	// List all refs that start with refs/heads/feature/
	featCmd := exec.Command("git", "for-each-ref", "refs/heads/feature/", "--format=%(refname:short)")
	featCmd.Dir = upstreamDir
	featOut, featErr := featCmd.CombinedOutput()
	if featErr != nil {
		t.Fatalf("git for-each-ref for feature branches failed: %v: %s", featErr, featOut)
	}
	// If there is any output, then there is at least one feature branch.
	if strings.TrimSpace(string(featOut)) == "" {
		t.Fatalf("no feature branch found in upstream repo")
	}
}