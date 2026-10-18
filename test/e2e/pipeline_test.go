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
// TestPipeline_E2E_Failure exercises acceptance #3: a failing run preserves
// state to a debug/<task_id> branch and returns a branch artifact with
// branch_purpose: debug (I5). fakeacp's "error" mode makes the ACP session
// return an error response, so the run fails and OnFailure fires.
func TestPipeline_E2E_Failure(t *testing.T) {
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
		Env: []string{"FAKE_ACP_MODE=error_with_changes"},
	})

	srv := remote.NewServer(runner, func() (bool, string) { return true, "fake-model" }, "", bareDir, baseDir, "tok-e2e")
	srv.Config.WorkspaceManager = wm

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
	// One attempt: the run fails on the first try, OnFailure fires, and the
	// task escalates immediately. Three attempts would take 3m+ (60s/120s
	// backoff) — acceptance #3 only needs the failure path, not the retry
	// count.
	q := store.NewSQLiteQueue(dbPath, store.WithMaxAttempts(1))

	sink := &stubSink{}
	policy := &stubPolicy{}

	// 6. Enqueue P1 task
	task := queue.Task{
		ID:           "e2e-fail-1",
		RepositoryID: "local/test-repo",
		RepositoryURL: upstreamDir,
		Ticket: queue.TrackerRef{
			Tracker:      "github",
			RepositoryID: "local/test-repo",
			ExternalID:   "43",
			URL:          "https://github.com/local/test-repo/issues/43",
		},
		Tier:   queue.TierP1,
		Prompt: "test failing pipeline",
		State:  queue.StateQueued,
		TriggeredBy: queue.TriggeredBy{
			EventType: "issue.opened",
			DedupeKey: "github:issue:local/test-repo:43:created",
		},
	}
	if err := q.Enqueue(context.Background(), task); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	// 7. Run worker
	w := worker.NewWorker(q, h, sink, policy, 1, 60*time.Second, 5*time.Minute, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	go w.Run(ctx)

	// 8. Wait for terminal state
	deadline := time.Now().Add(60 * time.Second)
	var finalTask queue.Task
	for time.Now().Before(deadline) {
		tk, err := q.GetTask(ctx, "e2e-fail-1")
		if err == nil {
			finalTask = tk
			if tk.State == queue.StateEscalated || tk.State == queue.StateSucceeded {
				t.Logf("task state: %s", tk.State)
				break
			}
		}
		t.Logf("waiting for task state, current: %v", tk.State)
		time.Sleep(2 * time.Second)
	}

	// 9. Assertions — the run must fail and escalate (D14).
	if finalTask.State != queue.StateEscalated {
		t.Fatalf("task state: got %s, want %s", finalTask.State, queue.StateEscalated)
	}

	// 10. A branch artifact must be saved (I5). The remote's OnFailure creates
// one with the attempt ID; the worker's ensureDebugBranch adds a synthesized
// one with the task ID. Either proves the failure path ran.
	var branchArtifactCount int
	row := q.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM artifacts WHERE attempt_id IN (SELECT id FROM attempts WHERE task_id = ?) AND kind = 'branch'`, "e2e-fail-1")
	if err := row.Scan(&branchArtifactCount); err != nil {
		t.Fatalf("query branch artifacts: %v", err)
	}
	if branchArtifactCount < 1 {
		t.Fatalf("expected >= 1 branch artifact, got %d", branchArtifactCount)
	}
	t.Logf("branch artifacts found: %d", branchArtifactCount)

	// 11. The debug branch must exist in the upstream repo.
	// OnFailure names it debug/<attemptID>; any debug/ branch proves it.
	dbgCmd := exec.Command("git", "for-each-ref", "refs/heads/debug/", "--format=%(refname:short)")
	dbgCmd.Dir = upstreamDir
	dbgOut, dbgErr := dbgCmd.CombinedOutput()
	if dbgErr != nil {
		t.Fatalf("git for-each-ref for debug branches failed: %v: %s", dbgErr, dbgOut)
	}
	branchName := strings.TrimSpace(string(dbgOut))
	if branchName == "" {
		t.Fatalf("no debug branch found in upstream repo")
	}
	if !strings.HasPrefix(branchName, "debug/") {
		t.Fatalf("branch name %q does not start with debug/", branchName)
	}
	t.Logf("debug branch: %s", branchName)
}
