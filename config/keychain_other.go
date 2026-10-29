//go:build !darwin

package config

// noopKeychain is a no-op implementation used on non-macOS platforms.
// Get returns ErrSecretNotFound, Set/Delete/List succeed silently.
type noopKeychain struct{}

func (k *noopKeychain) Get(string) (string, error) {
	return "", ErrSecretNotFound
}

func (k *noopKeychain) Set(string, string) error {
	return nil
}

func (k *noopKeychain) Delete(string) error {
	return nil
}

func (k *noopKeychain) List() ([]string, error) {
	return nil, nil
}
