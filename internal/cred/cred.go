// Package cred stores and retrieves credentials (Canvas token, jAccount
// session cookie) through a single Store interface.
//
// The operating system keyring (macOS Keychain, gnome-keyring, KWallet via
// Secret Service) is the mandatory primary backend. On headless systems
// without a keyring daemon, Open degrades to plain files under
// ~/.local/share/sjtu with owner-only permissions. Callers never learn which
// backend is active.
package cred

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// Credential keys used across the CLI.
const (
	KeyCanvas   = "canvas"
	KeyJAccount = "jaccount"
)

// ErrNotFound reports that no credential exists for the requested key.
var ErrNotFound = errors.New("credential not found")

// Store abstracts credential persistence. Implementations must keep secret
// bytes out of logs and must apply owner-only permissions to anything they
// write to disk.
type Store interface {
	// Get returns the credential for key, or ErrNotFound.
	Get(key string) (string, error)
	// Set stores the credential for key, replacing any previous value.
	Set(key, value string) error
	// Delete removes the credential for key; deleting a missing key is not
	// an error.
	Delete(key string) error
	// Backend names the active backend, e.g. "keyring" or "file", for
	// display in `sjtu auth status`.
	Backend() string
}

// serviceName namespaces all keyring entries written by this CLI.
const serviceName = "sjtu-cli"

// Open returns the keyring-backed Store when an OS keyring is reachable,
// otherwise it degrades to the file backend. The probe performs a read on a
// key that never exists: a well-formed "not found" answer proves the keyring
// daemon is alive without writing anything into the user's keychain.
func Open() (Store, error) {
	if keyringAvailable() {
		return keyringStore{}, nil
	}
	return newFileStore()
}

// keyringAvailable reports whether the OS keyring answers queries.
func keyringAvailable() bool {
	_, err := keyring.Get(serviceName, "probe-nonexistent-key")
	return err == nil || errors.Is(err, keyring.ErrNotFound)
}

// keyringStore implements Store on top of the OS keyring.
type keyringStore struct{}

// Get reads key from the OS keyring.
func (keyringStore) Get(key string) (string, error) {
	value, err := keyring.Get(serviceName, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return value, err
}

// Set writes key into the OS keyring, replacing any previous value.
func (keyringStore) Set(key, value string) error {
	return keyring.Set(serviceName, key, value)
}

// Delete removes key from the OS keyring.
func (keyringStore) Delete(key string) error {
	err := keyring.Delete(serviceName, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}

// Backend names this backend for status output.
func (keyringStore) Backend() string { return "keyring" }

