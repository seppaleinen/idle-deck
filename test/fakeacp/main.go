package main

// fakeacp is a minimal ACP agent for testing the model-reachability probe
// (cmd/remote/health.go). It speaks JSON-RPC 2.0 over stdio, one message
// per line, and its behavior is controlled by the FAKE_ACP_MODE env var:
//
//	"ok"     — session/prompt returns a JSON-RPC result (model reachable).
//	"error"  — session/prompt returns a JSON-RPC error (model rate-limited
//	           or out of credentials → not idle, per D31).
//	"hang"   — session/prompt never responds (model unreachable; the
//	           probe must time out and report false).
//	"die"    — the process exits immediately (no agent at all).
//	"slow"   — initialize takes longer than the startup budget.
//
// Notifications (JSON-RPC messages with no id) are echoed back as
// session/update so the probe's reader has real traffic to consume,
// matching how a real agent interleaves progress with responses.
import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func main() {
	mode := os.Getenv("FAKE_ACP_MODE")
	if mode == "die" {
		os.Exit(1)
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal([]byte(line), &req) != nil {
			continue
		}
		if req.ID == nil {
			// Notification — echo as a session/update so the reader has
			// real traffic; a real agent streams these during a prompt.
			fmt.Fprintln(out, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"available_commands_update"}}}`)
			out.Flush()
			continue
		}
		id := *req.ID
		switch req.Method {
		case "initialize":
			fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":true}}}`+"\n", id)
		case "session/new":
			fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"fake-sid"}}`+"\n", id)
		case "session/prompt":
			switch mode {
			case "error":
				fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"error":{"code":-32603,"message":"Internal error: Rate limit exceeded. Please try again later."}}`+"\n", id)
			case "hang":
				// Deliberately never respond. The probe must time out.
			default: // "ok"
				// Write a file to the workspace to make sure there is a change to commit.
				if err := os.WriteFile("change.txt", []byte("change from fakeacp"), 0o644); err != nil {
					// Ignore error; we still want to respond to the prompt.
				}
				// Output a notification that a tool call completed (a git push)
				fmt.Fprintln(out, `{"jsonrpc":"2.0","method":"session/update","params":{"update":{"sessionUpdate":"tool_call","toolCallId":"tool1","title":"git push origin feature/test-123","kind":"bash","status":"completed","rawInput":{},"rawOutput":{}}}}`)
				out.Flush()
				// Then output the actual response
				fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"result":{"stopReason":"end_turn"}}`+"\n", id)
			}
		default:
			fmt.Fprintf(out, `{"jsonrpc":"2.0","id":%d,"result":{}}`+"\n", id)
		}
		out.Flush()
	}
}
