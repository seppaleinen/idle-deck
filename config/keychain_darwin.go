//go:build darwin

package config

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strings"
)

const keychainService = "idle-deck.secrets"

// runCommand is used by darwinKeychain and can be replaced in tests with
// a fake that verifies arguments without invoking `security`.
var runCommand func(ctx context.Context, name string, arg ...string) *exec.Cmd = exec.CommandContext

// darwinKeychain implements keychain by shelling out to the `security` CLI.
// This is CGO-free and satisfies D32's cgo-free constraint.
type darwinKeychain struct{}

func init() {
	Keychain = &darwinKeychain{}
}

// Get retrieves a secret from the macOS Keychain.
//
// Uses `security find-generic-password -s <service> -a <account> -w` to
// retrieve the password. The secret is passed via command arguments, not
// shell interpolation, so there is no shell injection risk. The value briefly
// appears in /proc/<pid>/cmdline — unavoidable with this approach, but
// acceptable per the design contract.
func (k *darwinKeychain) Get(key string) (string, error) {
	if !IsValidKey(key) {
		return "", ErrSecretNotFound
	}

	args := getDarwinGetArgs(key)
	cmd := runCommand(context.Background(), "security", args...)
	out, err := cmd.Output()
	if err != nil {
		// security returns exit 44 when item is not found.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return "", ErrSecretNotFound
		}
		return "", sanitizeError(err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Set stores a secret in the macOS Keychain.
//
// If a key already exists it is replaced. Uses `-w` to pass the password
// via a command argument. Note: the `security` CLI does not support reading
// the password from stdin for add-generic-password (it prompts twice for
// confirmation), so the value briefly appears in the process command line.
// This is a known limitation of the CGO-free design (D32). The value is
// never logged by idle-deck.
func (k *darwinKeychain) Set(key string, value string) error {
	if !IsValidKey(key) {
		return ErrSecretNotFound
	}

	// Delete existing entry first, then add. The -U flag is not reliable
	// for upsert on macOS (it only updates, doesn't create), so we delete
	// then add to handle both create and update cases.
	// Ignore ErrSecretNotFound from Delete — key not existing is fine.
	if err := k.Delete(key); err != nil && !errors.Is(err, ErrSecretNotFound) {
		return err
	}

	args := setDarwinArgs(key, value)
	cmd := runCommand(context.Background(), "security", args...)
	// Suppress verbose security output (it prints keychain item details to stdout).
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		return sanitizeError(err)
	}
	return nil
}

// Delete removes a secret from the macOS Keychain.
// Returns ErrSecretNotFound if the key did not exist (exit 44).
// Other errors from `security` are wrapped and returned.
func (k *darwinKeychain) Delete(key string) error {
	if !IsValidKey(key) {
		return ErrSecretNotFound
	}

	cmd := runCommand(context.Background(), "security",
		"delete-generic-password",
		"-s", keychainService,
		"-a", key,
	)
	// Suppress verbose security output (it prints keychain item details to stdout).
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return ErrSecretNotFound
		}
		return sanitizeError(err)
	}
	return nil
}

// List returns the set of stored keys from the closed namespace.
// Iterates over the known keys and checks existence via
// `security find-generic-password`.
func (k *darwinKeychain) List() ([]string, error) {
	var keys []string
	for _, key := range []string{KeyGitHubToken, KeyHarnessToken} {
		args := getDarwinGetArgs(key)
		cmd := runCommand(context.Background(), "security", args...)
		// Exit 44 means not found, which is not an error.
		if err := cmd.Run(); err == nil {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// getDarwinArgs returns the command-line arguments for a Get operation.
// Exported for testing arg construction without executing commands.
func getDarwinGetArgs(key string) []string {
	return []string{
		"find-generic-password",
		"-s", keychainService,
		"-a", key,
		"-w",
	}
}

// setDarwinArgs returns the command-line arguments for a Set operation.
// Exported for testing arg construction without executing commands.
func setDarwinArgs(key string, value string) []string {
	return []string{
		"add-generic-password",
		"-s", keychainService,
		"-a", key,
		"-w", value,
	}
}

// sanitizeError wraps an error without leaking secret data from security CLI stderr.
func sanitizeError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return fmt.Errorf("security command failed (exit %d)", exitErr.ExitCode())
	}
	return fmt.Errorf("security command failed")
}
