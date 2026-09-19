package cred

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// fileStore implements Store as plaintext files, one per credential key,
// inside ~/.local/share/sjtu (XDG_DATA_HOME honored). It exists solely for
// headless machines without a keyring daemon; every write is owner-only.
type fileStore struct {
	dir string
}

// newFileStore creates the fallback directory with mode 0700.
func newFileStore() (Store, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "sjtu")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create credential dir: %w", err)
	}
	// MkdirAll does not tighten permissions of a pre-existing directory, so
	// enforce them explicitly; a world-readable credential dir is a defect.
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("secure credential dir: %w", err)
	}
	return fileStore{dir: dir}, nil
}

// path maps a credential key to its file; keys are CLI-controlled constants,
// never user input, so no traversal check is needed.
func (s fileStore) path(key string) string {
	return filepath.Join(s.dir, key)
}

// Get reads the credential file for key.
func (s fileStore) Get(key string) (string, error) {
	data, err := os.ReadFile(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Set writes the credential for key with mode 0600, replacing any previous
// value.
func (s fileStore) Set(key, value string) error {
	return os.WriteFile(s.path(key), []byte(value), 0o600)
}

// Delete removes the credential file for key.
func (s fileStore) Delete(key string) error {
	err := os.Remove(s.path(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Backend names this backend for status output.
func (fileStore) Backend() string { return "file (unencrypted, keyring unavailable)" }
