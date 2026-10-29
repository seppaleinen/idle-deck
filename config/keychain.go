package config

import "errors"

// Keychain provides access to the macOS Keychain for idle-deck secrets.
// The interface is closed: only IsValidKey keys may be stored (D36).
//
// On macOS, Keychain is set at init to a darwinKeychain that shells out to
// the `security` CLI (CGO-free, per D32). On other platforms it is nil.
// Tests may inject a fake implementation by assigning this variable.
var Keychain keychain

// keychain is the interface for Keychain operations.
// It is unexported so only the config package implements or replaces it.
type keychain interface {
	// Get retrieves the secret value for key.
	// Returns ErrSecretNotFound when the key does not exist.
	Get(key string) (string, error)

	// Set stores value for key.
	// On non-macOS (noop impl) this is a no-op that returns nil.
	Set(key string, value string) error

	// Delete removes key from the Keychain.
	// On non-macOS (noop impl) this is a no-op that returns nil.
	Delete(key string) error

	// List returns the set of stored keys from the closed namespace.
	// Keys are sorted alphabetically.
	List() ([]string, error)
}

// ErrSecretNotFound is returned by Keychain.Get when the key does not exist.
var ErrSecretNotFound = errors.New("secret not found in Keychain")

// Key namespace — the closed set of allowed keys (D36).
const (
	KeyGitHubToken  = "github.token"
	KeyHarnessToken = "harness.token"
)

// IsValidKey checks whether key belongs to the closed namespace.
func IsValidKey(key string) bool {
	return key == KeyGitHubToken || key == KeyHarnessToken
}

// secretEnvKey maps a keychain key to its corresponding environment variable name.
func secretEnvKey(key string) string {
	switch key {
	case KeyGitHubToken:
		return EnvGitHubToken
	case KeyHarnessToken:
		return EnvHarnessToken
	default:
		return ""
	}
}
