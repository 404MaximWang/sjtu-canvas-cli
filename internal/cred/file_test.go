package cred

import (
	"os"
	"path/filepath"
	"testing"
)

// Throwaway smoke test: the file fallback must round-trip credentials and
// enforce 0700/0600 permissions.
func TestFileStorePermissions(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	s, err := newFileStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(KeyCanvas, "tok123"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get(KeyCanvas)
	if err != nil || got != "tok123" {
		t.Fatalf("round-trip: got %q, err %v", got, err)
	}
	dir := filepath.Join(os.Getenv("XDG_DATA_HOME"), "sjtu")
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm: %v, err %v", fi.Mode().Perm(), err)
	}
	fi, err = os.Stat(filepath.Join(dir, KeyCanvas))
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("file perm: %v, err %v", fi.Mode().Perm(), err)
	}
	if err := s.Delete(KeyCanvas); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(KeyCanvas); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
