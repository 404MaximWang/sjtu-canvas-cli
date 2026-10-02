package cli

import (
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/logging"
)

// launchdPlistTemplate is the launchd service definition for macOS. The
// resolved binary path is substituted into ProgramArguments at install time.
//
//go:embed sjtu-daemon.plist
var launchdPlistTemplate string

// systemdServiceTemplate is the systemd user service definition for Linux.
// The resolved binary path is substituted into ExecStart at install time.
//
//go:embed sjtu-daemon.service
var systemdServiceTemplate string

// binaryPathPlaceholder marks where the resolved binary path lands in the
// service definitions. A brace-delimited token (not a % verb) keeps template
// edits safe: systemd unit specifiers like %h would be mangled by Sprintf.
const binaryPathPlaceholder = "{{BINARY_PATH}}"

// renderServiceTemplate substitutes the binary path into a service template.
func renderServiceTemplate(tmpl, binPath string) string {
	return strings.ReplaceAll(tmpl, binaryPathPlaceholder, binPath)
}

// newDaemonInstallCmd builds `sjtu daemon install` (alias: `enable`).
func newDaemonInstallCmd(rt *Runtime, stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "install",
		Aliases: []string{"enable"},
		Short:   "Install and start the background daemon as a system service",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return installDaemonService(rt, stdout, stderr)
		},
	}
}

// newDaemonUninstallCmd builds `sjtu daemon uninstall` (alias: `disable`).
func newDaemonUninstallCmd(stdout, stderr io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:     "uninstall",
		Aliases: []string{"disable"},
		Short:   "Stop and uninstall the background daemon system service",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return uninstallDaemonService(stdout, stderr)
		},
	}
}

// checkDaemonConfig checks required daemon configuration items before service installation.
func checkDaemonConfig(rt *Runtime, stderr io.Writer) {
	cfg, err := rt.Config()
	if err != nil {
		fmt.Fprintf(stderr, "config unreadable, skipping pre-flight checks: %v\n", err)
		return
	}
	if cfg.Probe.VideoCourseID == 0 {
		fmt.Fprintln(stderr, "config probe.video_course_id not set! You may not be able to probe video stream status automatically.")
	}
	if len(cfg.Notify.Command) == 0 {
		fmt.Fprintln(stderr, "config notify.command not set! You may not be able to receive notifications on health transitions.")
	}
}

// installDaemonService performs pre-flight checks and registers the daemon service for the current OS.
func installDaemonService(rt *Runtime, stdout, stderr io.Writer) error {
	checkDaemonConfig(rt, stderr)

	binPath, err := currentBinaryPath()
	if err != nil {
		return err
	}

	switch runtime.GOOS {
	case "darwin":
		return installLaunchdService(binPath, stdout)
	case "linux":
		return installSystemdService(binPath, stdout)
	default:
		return fmt.Errorf("unsupported operating system for service registration: %s", runtime.GOOS)
	}
}

// uninstallDaemonService removes the registered daemon service for the current OS.
func uninstallDaemonService(stdout, stderr io.Writer) error {
	switch runtime.GOOS {
	case "darwin":
		return uninstallLaunchdService(stdout)
	case "linux":
		return uninstallSystemdService(stdout)
	default:
		return fmt.Errorf("unsupported operating system for service removal: %s", runtime.GOOS)
	}
}

// currentBinaryPath resolves the absolute, symlink-evaluated path of the currently running binary.
func currentBinaryPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("lookup executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return exe, nil
	}
	return resolved, nil
}

// installLaunchdService renders and registers the launchd agent on macOS.
func installLaunchdService(binPath string, stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("lookup user home dir: %w", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create LaunchAgents directory: %w", err)
	}
	plistPath := filepath.Join(dir, "cn.edu.sjtu.sjtud.plist")
	content := renderServiceTemplate(launchdPlistTemplate, binPath)
	if err := os.WriteFile(plistPath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write plist file: %w", err)
	}

	domainTarget := fmt.Sprintf("gui/%d", os.Getuid())
	// Try bootout first to clear any old instance; ignore errors if not loaded.
	_ = exec.Command("launchctl", "bootout", domainTarget, plistPath).Run()

	out, err := exec.Command("launchctl", "bootstrap", domainTarget, plistPath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootstrap failed: %s (%w)", string(out), err)
	}

	return writeJSON(stdout, serviceInstallOut("launchd", plistPath))
}

// uninstallLaunchdService stops and unregisters the launchd agent on macOS.
func uninstallLaunchdService(stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("lookup user home dir: %w", err)
	}
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "cn.edu.sjtu.sjtud.plist")
	domainTarget := fmt.Sprintf("gui/%d", os.Getuid())

	_ = exec.Command("launchctl", "bootout", domainTarget, plistPath).Run()

	if err := os.Remove(plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove plist file: %w", err)
	}

	return writeJSON(stdout, map[string]any{"uninstalled": "launchd", "service": "cn.edu.sjtu.sjtud"})
}

// installSystemdService renders and registers the systemd user service on Linux.
func installSystemdService(binPath string, stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("lookup user home dir: %w", err)
	}
	dir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create systemd user directory: %w", err)
	}
	servicePath := filepath.Join(home, ".config", "systemd", "user", "sjtu-daemon.service")
	content := renderServiceTemplate(systemdServiceTemplate, binPath)
	if err := os.WriteFile(servicePath, []byte(content), 0644); err != nil {
		return fmt.Errorf("write service file: %w", err)
	}

	if out, err := exec.Command("systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl daemon-reload failed: %s (%w)", string(out), err)
	}
	// enable registers autostart; restart (not enable --now) starts a fresh
	// unit AND bounces an already-running one, so reinstalling after a binary
	// upgrade actually picks up the new binary.
	if out, err := exec.Command("systemctl", "--user", "enable", "sjtu-daemon").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl enable failed: %s (%w)", string(out), err)
	}
	if out, err := exec.Command("systemctl", "--user", "restart", "sjtu-daemon").CombinedOutput(); err != nil {
		return fmt.Errorf("systemctl restart failed: %s (%w)", string(out), err)
	}

	return writeJSON(stdout, serviceInstallOut("systemd", servicePath))
}

// uninstallSystemdService stops and unregisters the systemd user service on Linux.
func uninstallSystemdService(stdout io.Writer) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("lookup user home dir: %w", err)
	}
	servicePath := filepath.Join(home, ".config", "systemd", "user", "sjtu-daemon.service")

	_ = exec.Command("systemctl", "--user", "disable", "--now", "sjtu-daemon").Run()

	if err := os.Remove(servicePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove service file: %w", err)
	}

	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()

	return writeJSON(stdout, map[string]any{"uninstalled": "systemd", "service": "sjtu-daemon"})
}

// serviceInstallOut is the install command's JSON result: which service
// manager took over, where the unit file landed, and where the daemon logs.
func serviceInstallOut(manager, unitPath string) map[string]any {
	out := map[string]any{"installed": manager, "path": unitPath}
	if dir, err := logging.StateDir(); err == nil {
		out["log"] = filepath.Join(dir, logging.DaemonLogName)
	}
	return out
}
