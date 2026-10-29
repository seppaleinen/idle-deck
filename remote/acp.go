package remote

// ACP adapter — the shared Runner implementation for all Agent Client
// Protocol agents (opencode, pi, and any ACP CLI).
//
// Protocol notes (verified against opencode 1.18.30, 2026-10-09):
//   - JSON-RPC 2.0 over stdio, one message per line.
//   - Fields are camelCase: sessionId, toolCallId, protocolVersion.
//   - initialize -> { agentCapabilities: { loadSession, mcpCapabilities, ... } }
//   - session/new -> { sessionId: "ses_...", configOptions: [...] }
//   - session/prompt -> { stopReason: "end_turn", usage: {...} } on completion
//   - Notifications: { method: "session/update", params: { sessionId,
//     update: { sessionUpdate: ... } } } where sessionUpdate is one of:
//     available_commands_update, agent_thought_chunk, agent_message_chunk,
//     tool_call, tool_call_update.
//   - tool_call / tool_call_update carry: toolCallId, title, kind
//     (execute|read|...), status (pending|in_progress|completed|failed),
//     rawInput (e.g. { command, cwd } for bash), rawOutput (e.g. { output,
//     exit }).
//   - session/abort is NOT implemented by opencode ("Method not found").
//     Cancellation is a process kill: the wrapper kills the process and
//     reaps it, and 204 on DELETE means the agent is confirmed dead (I8).
//   - session/close is supported (graceful) but not required for kill.
//
// The one-line swap: NewACP with a different Bin/Args targets a different
// ACP agent. The contract is one skeleton; the launch recipe is a table.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// ACPConfig is the launch recipe for one ACP agent.
type ACPConfig struct {
	// Bin is the agent binary (e.g. "opencode").
	Bin string
	// Args are the ACP server args (e.g. ["acp"]). The workspace dir is set
	// via cmd.Dir and the session/new cwd param; opencode v2 no longer
	// accepts --cwd on the command line.
	Args []string
	// Env is extra environment for the agent process.
	Env []string
}

// acpRunner is the Runner implementation for ACP agents.
type acpRunner struct {
	cfg ACPConfig

	mu       sync.Mutex
	sessions map[SessionID]*acpSession
	nextID   int
}

// NewACP creates the ACP runner. One instance covers the whole ACP family;
// swap cfg.Bin/Args to target a different ACP agent.
func NewACP(cfg ACPConfig) Runner {
	return &acpRunner{
		cfg:      cfg,
		sessions: make(map[SessionID]*acpSession),
	}
}

type rpcResult struct {
	raw json.RawMessage
	err error
}

// acpSession is one running agent process plus its ACP conversation.
type acpSession struct {
	cmd           *exec.Cmd
	in            *jsonWriter
	out           *bufio.Reader
	processExited chan struct{}
	id            SessionID // assigned by the server; kept for reaping

	mu        sync.Mutex
	acpSID    string
	nextRPC   int
	pending   map[int]chan rpcResult
	result    RunResult
	artifacts []Artifact
	usage     *Usage         // accumulated token usage from session/prompt and session/update
	done      chan struct{}
	doneOnce  sync.Once
}

// Start launches the ACP agent in the workspace, does the handshake, and
// fires the prompt in the background. It blocks until session/new returns
// a session id (the agent is ready and the prompt has been queued).
func (r *acpRunner) Start(ctx context.Context, ws Workspace, req StartRequest) (SessionID, error) {
	// Substitute the workspace dir into the launch args.
	args := make([]string, len(r.cfg.Args))
	copy(args, r.cfg.Args)
	for i, a := range args {
		if a == "{{DIR}}" {
			args[i] = ws.Dir
		}
	}

	cmd := exec.Command(r.cfg.Bin, args...)
	cmd.Dir = ws.Dir
	if len(r.cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), r.cfg.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", fmt.Errorf("stdout pipe: %w", err)
	}
	// Discard agent stderr in v1; W9 wiring adds log capture.
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("start agent: %w", err)
	}

	s := &acpSession{
		cmd:           cmd,
		in:            newJSONWriter(stdin),
		out:           bufio.NewReader(stdout),
		processExited: make(chan struct{}),
		pending:       make(map[int]chan rpcResult),
		done:          make(chan struct{}),
	}

	r.mu.Lock()
	r.nextID++
	sid := SessionID("sess-" + fmt.Sprint(r.nextID))
	s.id = sid
	r.sessions[sid] = s
	r.mu.Unlock()

	// Reap the process; readerPump exits when the process is gone.
	go func() {
		_ = cmd.Wait()
		close(s.processExited)
		s.failPending(fmt.Errorf("agent process exited"))
		s.finish(RunResult{Outcome: ""}) // empty: server decides (abort/timeout)
	}()

	// Read every line: responses go to the pending map, notifications go
	// to the artifact observer.
	go s.readerPump()

	// 1. initialize handshake.
	if _, err := s.rpc(ctx, "initialize", map[string]any{
		"protocolVersion": 1,
		"clientInfo":      map[string]any{"name": "idle-deck", "version": "0.1.0"},
		"capabilities":    map[string]any{},
	}); err != nil {
		r.abortAndReap(s)
		r.remove(sid)
		return "", fmt.Errorf("initialize: %w", err)
	}

	// 2. session/new — cwd is the workspace.
	res, err := s.rpc(ctx, "session/new", map[string]any{
		"cwd":        ws.Dir,
		"mcpServers": []any{},
	})
	if err != nil {
		r.abortAndReap(s)
		r.remove(sid)
		return "", fmt.Errorf("session/new: %w", err)
	}
	var sNew struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &sNew); err != nil || sNew.SessionID == "" {
		r.abortAndReap(s)
		r.remove(sid)
		return "", fmt.Errorf("session/new: no sessionId")
	}
	s.mu.Lock()
	s.acpSID = sNew.SessionID
	s.mu.Unlock()

	// 3. Prompt in the background; collect() (server) gets the result via
	//    Result, which unblocks when done closes.
	go s.prompt(req)

	return sid, nil
}

// prompt sends session/prompt and finishes the session with the outcome.
// On success: succeeded + artifacts. On error (process died, context gone):
// empty outcome — the server's collect() maps it using its flags (abort,
func (s *acpSession) prompt(req StartRequest) {
	s.mu.Lock()
	sid := s.acpSID
	s.mu.Unlock()

	res, err := s.rpc(context.Background(), "session/prompt", map[string]any{
		"sessionId": sid,
		"prompt": []any{
			map[string]any{"type": "text", "text": req.Prompt},
		},
	})
	if err != nil {
		s.finish(RunResult{Outcome: ""}) // server decides
		return
	}
	// Extract usage from session/prompt result if present
	if res != nil {
		var promptResult struct {
			StopReason string `json:"stopReason"`
			Usage      *struct {
				PromptTokens     int `json:"promptTokens"`
				CompletionTokens int `json:"completionTokens"`
				TotalTokens      int `json:"totalTokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(res, &promptResult) == nil && promptResult.Usage != nil {
			s.mu.Lock()
			s.usage = &Usage{
				PromptTokens:     promptResult.Usage.PromptTokens,
				CompletionTokens: promptResult.Usage.CompletionTokens,
				TotalTokens:      promptResult.Usage.TotalTokens,
			}
			s.mu.Unlock()
		}
	}
	s.finish(RunResult{Outcome: OutcomeSucceeded, Artifacts: s.artifactsSnapshot(), Usage: s.usageSnapshot()})
}

// Abort kills the agent process and reaps it. Confirmed-dead (I8): the
// function returns only after cmd.Wait has closed processExited.
func (r *acpRunner) Abort(ctx context.Context, id SessionID) error {
	r.mu.Lock()
	s, ok := r.sessions[id]
	r.mu.Unlock()
	if !ok {
		return nil // already reaped
	}
	return r.abortAndReap(s)
}

// abortAndReap kills the process and waits for it to be confirmed dead.
func (r *acpRunner) abortAndReap(s *acpSession) error {
	var killErr error
	if s.cmd.Process != nil {
		killErr = s.cmd.Process.Kill()
	}
	<-s.processExited // confirmed dead
	r.mu.Lock()
	delete(r.sessions, s.id)
	r.mu.Unlock()
	return killErr
}

// Result blocks until the session is terminal and returns its outcome and
// typed artifacts.
func (r *acpRunner) Result(ctx context.Context, id SessionID) (RunResult, error) {
	r.mu.Lock()
	s, ok := r.sessions[id]
	r.mu.Unlock()
	if !ok {
		return RunResult{}, fmt.Errorf("unknown session")
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return RunResult{}, ctx.Err()
	case <-s.processExited:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.result, nil
}

// --- internals -----------------------------------------------------------

func (s *acpSession) idLocked() SessionID {
	// The server assigns the id; the session keeps a copy.
	return s.id
}

func (r *acpRunner) remove(id SessionID) {
	r.mu.Lock()
	delete(r.sessions, id)
	r.mu.Unlock()
}

// readerPump consumes every stdout line from the agent.
func (s *acpSession) readerPump() {
	for {
		line, err := s.out.ReadString('\n')
		if err != nil {
			return // process is gone
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var envelope struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      *int            `json:"id"`
			Method  string          `json:"method"`
			Params  json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &envelope); err != nil {
			continue
		}
		if envelope.ID != nil {
			// A response to our request.
			res := rpcResult{raw: json.RawMessage(line)}
			var resp struct {
				Error *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if json.Unmarshal([]byte(line), &resp) == nil && resp.Error != nil {
				res.err = fmt.Errorf("rpc error %d: %s", resp.Error.Code, resp.Error.Message)
				res.raw = nil
			}
			s.mu.Lock()
			ch, ok := s.pending[*envelope.ID]
			s.mu.Unlock()
			if ok {
				ch <- res
			}
			continue
		}
		if envelope.Method == "session/update" {
			s.observe(envelope.Params)
		}
	}
}

// observe records artifact signals from a session/update notification.
func (s *acpSession) observe(params json.RawMessage) {
	var p struct {
		Update struct {
			SessionUpdate string          `json:"sessionUpdate"`
			ToolCallID    string          `json:"toolCallId"`
			Title         string          `json:"title"`
			Kind          string          `json:"kind"`
			Status        string          `json:"status"`
			RawInput      json.RawMessage `json:"rawInput"`
			RawOutput     json.RawMessage `json:"rawOutput"`
			Usage         *struct {
				PromptTokens     int `json:"promptTokens"`
				CompletionTokens int `json:"completionTokens"`
				TotalTokens      int `json:"totalTokens"`
			} `json:"usage,omitempty"`
		} `json:"update"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	u := p.Update

	// Extract usage if present in the session/update notification
	if u.Usage != nil {
		s.mu.Lock()
		// Accumulate usage across updates (the last one typically has the final total)
		s.usage = &Usage{
			PromptTokens:     u.Usage.PromptTokens,
			CompletionTokens: u.Usage.CompletionTokens,
			TotalTokens:      u.Usage.TotalTokens,
		}
		s.mu.Unlock()
	}

	if u.SessionUpdate != "tool_call" && u.SessionUpdate != "tool_call_update" {
		return
	}
	if u.Status != "completed" && u.Status != "failed" {
		return
	}
	text := commandText(u.Title, u.RawInput) + "\n" + outputText(u.RawOutput)
	s.recordToolCall(text)
}

// recordToolCall extracts typed artifacts from completed tool activity.
// The agent does git itself in the workspace (D27: the remote owns the
// workspace); the wrapper observes commands to build the artifact list.
//
// Artifact inference is reference-based, not substring-based: a sweep that
// prints "I would run `git push`" or "see PR #123" must not produce a
// false-positive artifact (audit M10). We require an actual branch or PR
// reference — a URL containing the pattern, or a command that creates one.
func (s *acpSession) recordToolCall(text string) {
	lower := strings.ToLower(text)
	switch {
	case hasPRReference(lower):
		s.addArtifact(Artifact{Kind: "pull_request", URI: extractURL(text)})
	case hasCommentReference(lower):
		s.addArtifact(Artifact{Kind: "comment", Body: extractURL(text)})
	case hasPushReference(lower):
		s.addArtifact(Artifact{Kind: "branch", BranchPurpose: stringPtr("feature")})
	}
}

// hasPRReference returns true when the tool output contains an actual PR
// creation or reference — a URL containing "pull/new", "pull/create", or a
// GitHub pull request URL pattern (e.g. "github.com/owner/repo/pull/123").
// A bare mention of "pr create" in prose (e.g. "I would run pr create") is
// NOT a reference and returns false.
func hasPRReference(lower string) bool {
	if strings.Contains(lower, "pull/new") || strings.Contains(lower, "pull/create") {
		return true
	}
	// GitHub PR URL: github.com/<owner>/<repo>/pull/<number>
	if strings.Contains(lower, "/pull/") && strings.Contains(lower, "github.com") {
		return true
	}
	return false
}

// hasCommentReference returns true when the tool output contains an actual
// issue comment creation — a URL containing "issues/<n>/comments" or the
// "gh api repos" comment endpoint.
func hasCommentReference(lower string) bool {
	return strings.Contains(lower, "issues/") && strings.Contains(lower, "comments") ||
		strings.Contains(lower, "gh api repos/") && strings.Contains(lower, "comments")
}

// hasPushReference returns true when the tool output contains an actual git
// push command with a remote reference — "git push" followed by a remote
// (origin, a hostname, or a URL). A bare "git push" in prose is not a push.
func hasPushReference(lower string) bool {
	if !strings.Contains(lower, "git push") {
		return false
	}
	// Require a remote reference after "git push" to distinguish an actual
	// push from a hypothetical mention.
	rest := lower[strings.Index(lower, "git push")+len("git push"):]
	rest = strings.TrimSpace(rest)
	return strings.Contains(rest, "origin") ||
		strings.Contains(rest, "github.com") ||
		strings.Contains(rest, "git@") ||
		strings.Contains(rest, "https://") ||
		strings.Contains(rest, "http://")
}

func (s *acpSession) addArtifact(a Artifact) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, x := range s.artifacts {
		if x.Kind == a.Kind && x.URI == a.URI {
			return
		}
	}
	s.artifacts = append(s.artifacts, a)
}

func (s *acpSession) artifactsSnapshot() []Artifact {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Artifact, len(s.artifacts))
	copy(out, s.artifacts)
	return out
}

func (s *acpSession) usageSnapshot() *Usage {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.usage == nil {
		return nil
	}
	return &Usage{
		PromptTokens:     s.usage.PromptTokens,
		CompletionTokens: s.usage.CompletionTokens,
		TotalTokens:      s.usage.TotalTokens,
	}
}

// finish records the terminal result exactly once and unblocks Result.
// The first non-empty outcome wins; an empty outcome is a signal that the
// agent died and the server must decide (abort/timeout/retryable failure).
// The reap goroutine and the prompt goroutine may both call finish; the
// doneOnce guard keeps them from racing.
func (s *acpSession) finish(result RunResult) {
	s.mu.Lock()
	if s.result.Outcome == "" && len(s.result.Artifacts) == 0 {
		// If the result doesn't have usage but we have accumulated usage, include it
		if result.Usage == nil {
			result.Usage = s.usage
		}
		s.result = result
	}
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}

func (s *acpSession) failPending(err error) {
	s.mu.Lock()
	for id, ch := range s.pending {
		ch <- rpcResult{err: err}
		delete(s.pending, id)
	}
	s.mu.Unlock()
}

// rpc sends a JSON-RPC request and waits for the response (or ctx done).
// It returns the `result` field of the JSON-RPC response (or an error if the
// response contains an `error` field).
func (s *acpSession) rpc(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	s.nextRPC++
	id := s.nextRPC
	ch := make(chan rpcResult, 1)
	s.pending[id] = ch
	s.mu.Unlock()

	body, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	})
	if err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("marshal: %w", err)
	}
	if err := s.in.writeLine(body); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("write: %w", err)
	}

	select {
	case res := <-ch:
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		if res.err != nil {
			return nil, res.err
		}
		// res.raw is the full JSON-RPC response line. Extract the `result` field.
		var wrapper struct {
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(res.raw, &wrapper); err != nil {
			return nil, fmt.Errorf("unwrap response: %w", err)
		}
		if wrapper.Error != nil {
			return nil, fmt.Errorf("rpc error %d: %s", wrapper.Error.Code, wrapper.Error.Message)
		}
		return wrapper.Result, nil
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, ctx.Err()
	case <-s.processExited:
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return nil, fmt.Errorf("agent exited before response to %s", method)
	}
}

// jsonWriter writes newline-delimited JSON lines to a pipe, serialised.
type jsonWriter struct {
	mu sync.Mutex
	w  *bufio.Writer
}

func newJSONWriter(w io.Writer) *jsonWriter {
	return &jsonWriter{w: bufio.NewWriter(w)}
}

func (j *jsonWriter) writeLine(body []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, err := j.w.Write(body); err != nil {
		return err
	}
	if err := j.w.WriteByte('\n'); err != nil {
		return err
	}
	return j.w.Flush()
}

// closeOnce closes a channel exactly once; used for s.done.
var doneClose = sync.Once{}

func closeOnce(done *chan struct{}) {
	close(*done)
}

// commandText pulls the command string out of a tool_call rawInput or title.
func commandText(title string, rawInput json.RawMessage) string {
	var in struct {
		Command string `json:"command"`
	}
	if len(rawInput) > 0 && json.Unmarshal(rawInput, &in) == nil && in.Command != "" {
		return in.Command
	}
	return title
}

// outputText pulls the output string out of a tool_call rawOutput.
func outputText(rawOutput json.RawMessage) string {
	var out struct {
		Output string `json:"output"`
	}
	if len(rawOutput) > 0 && json.Unmarshal(rawOutput, &out) == nil {
		return out.Output
	}
	return ""
}

// extractURL pulls the first http(s) URL out of text.
func extractURL(text string) string {
	for _, field := range strings.Fields(text) {
		if strings.HasPrefix(field, "http://") || strings.HasPrefix(field, "https://") {
			return field
		}
	}
	return ""
}
