package logging

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRotation pins the single-backup rotation: an oversized sjtu.log is
// renamed to sjtu.log.1 (replacing any older backup) before appending
// resumes.
func TestRotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	logPath := filepath.Join(dir, "sjtu", "sjtu.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, make([]byte, maxLogSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath+".1", []byte("older backup"), 0o600); err != nil {
		t.Fatal(err)
	}

	close, err := Setup()
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	defer close()

	backup, err := os.Stat(logPath + ".1")
	if err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	if backup.Size() != maxLogSize+1 {
		t.Errorf("backup size = %d, want the rotated %d bytes", backup.Size(), maxLogSize+1)
	}
	active, err := os.Stat(logPath)
	if err != nil {
		t.Fatalf("active log missing: %v", err)
	}
	if active.Size() != 0 {
		t.Errorf("active log size = %d, want 0 after rotation", active.Size())
	}
}

// TestEnvLevel pins the SJTU_LOG_LEVEL parsing, including the typo path that
// warns and falls back to warn.
func TestEnvLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelWarn,
		"debug":   slog.LevelDebug,
		"INFO":    slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"error":   slog.LevelError,
		"verbose": slog.LevelWarn, // unrecognized → warn, reported
	}
	for value, want := range cases {
		t.Setenv("SJTU_LOG_LEVEL", value)
		got, bad := envLevel()
		if got != want {
			t.Errorf("SJTU_LOG_LEVEL=%q: level = %v, want %v", value, got, want)
		}
		if value == "verbose" && bad != "verbose" {
			t.Errorf("SJTU_LOG_LEVEL=verbose: bad value %q not reported", bad)
		}
	}
}

// TestSetupWritesToFile pins that records land in the log file and respect
// the level gate.
func TestSetupWritesToFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	t.Setenv("SJTU_LOG_LEVEL", "warn")

	close, err := Setup()
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	slog.Info("must be gated out")
	slog.Warn("must be recorded")
	if err := close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	blob, err := os.ReadFile(filepath.Join(dir, "sjtu", "sjtu.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), "must be recorded") {
		t.Errorf("log missing warn record: %q", blob)
	}
	if strings.Contains(string(blob), "gated out") {
		t.Errorf("info record leaked past the warn gate: %q", blob)
	}
}
