package config

import (
	"errors"
	"reflect"
	"testing"
)

// fakeKeychain is a test double for the keychain interface.
type fakeKeychain struct {
	store map[string]string
}

func newFakeKeychain() *fakeKeychain {
	return &fakeKeychain{store: make(map[string]string)}
}

func (f *fakeKeychain) Get(key string) (string, error) {
	if !IsValidKey(key) {
		return "", ErrSecretNotFound
	}
	v, ok := f.store[key]
	if !ok {
		return "", ErrSecretNotFound
	}
	return v, nil
}

func (f *fakeKeychain) Set(key string, value string) error {
	if !IsValidKey(key) {
		return errors.New("invalid key")
	}
	f.store[key] = value
	return nil
}

func (f *fakeKeychain) Delete(key string) error {
	if !IsValidKey(key) {
		return errors.New("invalid key")
	}
	delete(f.store, key)
	return nil
}

func (f *fakeKeychain) List() ([]string, error) {
	keys := make([]string, 0, len(f.store))
	for k := range f.store {
		keys = append(keys, k)
	}
	// Simulate the real List sorting
	// For simplicity in test, we just return what we have.
	return keys, nil
}

func TestKeychainIsValidKey(t *testing.T) {
	if !IsValidKey(KeyGitHubToken) {
		t.Errorf("IsValidKey: %s should be valid", KeyGitHubToken)
	}
	if !IsValidKey(KeyHarnessToken) {
		t.Errorf("IsValidKey: %s should be valid", KeyHarnessToken)
	}
	if IsValidKey("github.tokenx") {
		t.Errorf("IsValidKey: invalid key accepted")
	}
	if IsValidKey("other") {
		t.Errorf("IsValidKey: invalid key accepted")
	}
}

func TestFakeKeychain_GetSetDelete(t *testing.T) {
	fk := newFakeKeychain()

	if err := fk.Set(KeyGitHubToken, "ghp_test"); err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	v, err := fk.Get(KeyGitHubToken)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if v != "ghp_test" {
		t.Errorf("Get = %q, want ghp_test", v)
	}

	if err := fk.Delete(KeyGitHubToken); err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = fk.Get(KeyGitHubToken)
	if !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get after delete should return ErrSecretNotFound, got %v", err)
	}
}

func TestResolveSecrets_KeychainFallback(t *testing.T) {
	// Save original Keychain and restore after test.
	orig := Keychain
	fk := newFakeKeychain()
	fk.Set(KeyGitHubToken, "ghp_from_keychain")
	fk.Set(KeyHarnessToken, "ht_from_keychain")
	Keychain = fk
	defer func() { Keychain = orig }()

	// Env vars not set.
	// Config with empty secrets should be filled from Keychain.
	var cfg Config
	root := reflect.ValueOf(&cfg).Elem()

	if err := resolveSecrets(root); err != nil {
		t.Fatalf("resolveSecrets failed: %v", err)
	}

	if cfg.GitHubToken != "ghp_from_keychain" {
		t.Errorf("GitHubToken = %q, want ghp_from_keychain", cfg.GitHubToken)
	}
	if cfg.HarnessToken != "ht_from_keychain" {
		t.Errorf("HarnessToken = %q, want ht_from_keychain", cfg.HarnessToken)
	}
}

func TestResolveSecrets_EnvWinsOverKeychain(t *testing.T) {
	orig := Keychain
	fk := newFakeKeychain()
	fk.Set(KeyGitHubToken, "ghp_from_keychain")
	Keychain = fk
	defer func() { Keychain = orig }()

	var cfg Config
	root := reflect.ValueOf(&cfg).Elem()

	// Simulate env already filled GitHubToken
	_ = setField(root, fieldMeta{name: "GitHubToken", key: EnvGitHubToken, kind: kindString}, "ghp_from_env")

	if err := resolveSecrets(root); err != nil {
		t.Fatalf("resolveSecrets failed: %v", err)
	}

	// Env value should win
	if cfg.GitHubToken != "ghp_from_env" {
		t.Errorf("GitHubToken = %q, want ghp_from_env (env should win over Keychain)", cfg.GitHubToken)
	}
}

func TestResolveSecrets_KeychainMissing(t *testing.T) {
	orig := Keychain
	fk := newFakeKeychain()
	// Keychain empty
	Keychain = fk
	defer func() { Keychain = orig }()

	var cfg Config
	root := reflect.ValueOf(&cfg).Elem()

	if err := resolveSecrets(root); err != nil {
		t.Fatalf("resolveSecrets failed: %v", err)
	}

	// Should remain empty
	if cfg.GitHubToken != "" {
		t.Errorf("GitHubToken should remain empty when Keychain has no value")
	}
	if cfg.HarnessToken != "" {
		t.Errorf("HarnessToken should remain empty when Keychain has no value")
	}
}

func TestResolveSecrets_NoKeychain(t *testing.T) {
	orig := Keychain
	Keychain = nil
	defer func() { Keychain = orig }()

	var cfg Config
	root := reflect.ValueOf(&cfg).Elem()

	// Should not panic, just leave fields empty
	if err := resolveSecrets(root); err != nil {
		t.Fatalf("resolveSecrets with nil Keychain failed: %v", err)
	}
	if cfg.GitHubToken != "" || cfg.HarnessToken != "" {
		t.Errorf("secrets should remain empty when Keychain is nil")
	}
}
