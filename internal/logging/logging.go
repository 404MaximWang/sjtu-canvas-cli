// Package logging installs the process-wide slog logger: text records
// appended to the state-directory log file with single-backup rotation,
// wrapped in credential redaction.
//
// Logs go exclusively to the state directory (~/.local/state/sjtu); stdout
// and stderr are reserved for command output and structured errors, so log
// records can never contaminate machine-readable output.
package logging

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

const (
	// maxLogSize is the rotation threshold for the active log file.
	maxLogSize = 4 << 20 // 4 MB
	// CLILogName is the CLI's log file inside the state directory.
	CLILogName = "sjtu.log"
	// DaemonLogName is the daemon's log file beside the CLI's.
	DaemonLogName = "sjtud.log"
)

// Setup installs the default slog logger writing to the named file in the
// state directory and returns a function that closes it. The minimum level
// comes from SJTU_LOG_LEVEL (debug|info|warn|error), defaulting to warn.
// When the existing log already exceeds 4 MB it is renamed to name+".bak",
// replacing any previous backup: exactly one backup is kept.
func Setup(name string) (close func() error, err error) {
	dir, err := StateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(dir, name)
	if st, err := os.Stat(path); err == nil && st.Size() >= maxLogSize {
		if err := os.Rename(path, path+".bak"); err != nil {
			return nil, fmt.Errorf("rotate log: %w", err)
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	level, bad := envLevel()
	handler := session.NewRedactingHandler(slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(slog.New(handler))
	if bad != "" {
		slog.Warn("unrecognized SJTU_LOG_LEVEL, using warn", "value", bad)
	}
	return f.Close, nil
}

// StateDir returns the CLI's state directory (~/.local/state/sjtu, honoring
// XDG_STATE_HOME). Logs and TUI session files live here; it is also the
// natural home for any per-run diagnostics.
func StateDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sjtu"), nil
}

// envLevel parses SJTU_LOG_LEVEL. An unrecognized non-empty value falls back
// to warn and is returned so Setup can log a warning about the typo.
func envLevel() (slog.Level, string) {
	switch v := strings.ToLower(strings.TrimSpace(os.Getenv("SJTU_LOG_LEVEL"))); v {
	case "debug":
		return slog.LevelDebug, ""
	case "info":
		return slog.LevelInfo, ""
	case "", "warn":
		return slog.LevelWarn, ""
	case "error":
		return slog.LevelError, ""
	default:
		return slog.LevelWarn, v
	}
}
