// Package config loads and saves the non-sensitive CLI configuration.
//
// Configuration lives at $XDG_CONFIG_HOME/sjtu/config.json (defaulting to
// ~/.config/sjtu). It never contains credentials; those are handled by the
// cred package, so this file is always safe to print, share, or commit.
package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// DefaultCanvasBaseURL is the Canvas instance of the main SJTU campus.
const DefaultCanvasBaseURL = "https://oc.sjtu.edu.cn"

// Config holds every non-sensitive preference of the CLI.
type Config struct {
	// CanvasBaseURL is the Canvas root URL; the main campus and the Joint
	// Institute use different hosts, so this stays configurable.
	CanvasBaseURL string `json:"canvas_base_url"`
	// Probe configures the daemon's health probes.
	Probe ProbeConfig `json:"probe"`
	// Notify configures the daemon's health-transition notification hook.
	Notify NotifyConfig `json:"notify"`
}

// ProbeConfig configures the daemon's health probes.
type ProbeConfig struct {
	// VideoCourseID is the course the v.sjtu probe runs its launch chain
	// against. Zero means unconfigured: the video domain reports unknown.
	// There is deliberately no automatic course pick: a course without the
	// video tool or out of term would manufacture false failures.
	VideoCourseID int64 `json:"video_course_id"`
}

// NotifyConfig configures the daemon's health-transition notification.
type NotifyConfig struct {
	// Command is an argv array executed on every fail/recover transition,
	// with the event JSON on stdin (e.g. an ntfy curl call). Empty falls
	// back to a desktop notification (osascript / notify-send).
	Command []string `json:"command"`
}

// Dir returns the configuration directory, honoring XDG_CONFIG_HOME and
// falling back to ~/.config.
func Dir() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "sjtu"), nil
}

// Load reads config.json, returning defaults when the file does not exist.
// A malformed file is an error: silently resetting user preferences would
// hide real corruption.
func Load() (*Config, error) {
	cfg := &Config{CanvasBaseURL: DefaultCanvasBaseURL}
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "config.json"))
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	if cfg.CanvasBaseURL == "" {
		cfg.CanvasBaseURL = DefaultCanvasBaseURL
	}
	return cfg, nil
}

// Save writes config.json with mode 0644, creating the directory with mode
// 0755 when needed. The file holds no secrets, so plain user-readable
// permissions are correct.
func Save(cfg *Config) error {
	dir, err := Dir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644)
}
