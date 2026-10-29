//go:build darwin

package config

import (
	"context"
	"os/exec"
	"strings"
	"testing"
)

// fakeRunner captures the command name and args passed to runCommand.
type fakeRunner struct {
	calls []struct {
		name string
		args []string
	}
}

func (f *fakeRunner) runCmd(ctx context.Context, name string, arg ...string) *exec.Cmd {
	f.calls = append(f.calls, struct {
		name string
		args []string
	}{name: name, args: arg})
	// Return a do-nothing command that always succeeds.
	return exec.Command("true")
}

func TestDarwinKeychain_GetArgs(t *testing.T) {
	// Verify that getDarwinGetArgs produces the correct arguments.
	args := getDarwinGetArgs(KeyGitHubToken)
	expected := []string{
		"find-generic-password",
		"-s", keychainService,
		"-a", KeyGitHubToken,
		"-w",
	}
	if len(args) != len(expected) {
		t.Fatalf("args length %d, want %d", len(args), len(expected))
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("args[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestDarwinKeychain_SetArgs(t *testing.T) {
	args := setDarwinArgs(KeyHarnessToken, "secret-value")
	expected := []string{
		"add-generic-password",
		"-s", keychainService,
		"-a", KeyHarnessToken,
		"-w", "secret-value",
	}
	if len(args) != len(expected) {
		t.Fatalf("args length %d, want %d", len(args), len(expected))
	}
	for i, a := range args {
		if a != expected[i] {
			t.Errorf("args[%d] = %q, want %q", i, a, expected[i])
		}
	}
}

func TestDarwinKeychain_ListCallsAllKeys(t *testing.T) {
	// Replace runCommand with fake to verify calls.
	orig := runCommand
	f := &fakeRunner{}
	runCommand = f.runCmd
	defer func() { runCommand = orig }()

	k := &darwinKeychain{}
	_, err := k.List()
	if err != nil {
		t.Fatalf("List returned error: %v", err)
	}

	// Expect two calls, one for each key, in order.
	if len(f.calls) != 2 {
		t.Fatalf("expected 2 calls to security, got %d", len(f.calls))
	}

	for i, call := range f.calls {
		if call.name != "security" {
			t.Errorf("call %d: name = %q, want security", i, call.name)
		}
		if len(call.args) < 4 {
			t.Errorf("call %d: args too short", i)
		}
		if call.args[0] != "find-generic-password" {
			t.Errorf("call %d: args[0] = %q, want find-generic-password", i, call.args[0])
		}
		if !strings.Contains(strings.Join(call.args, " "), keychainService) {
			t.Errorf("call %d: service not in args: %v", i, call.args)
		}
	}
}
