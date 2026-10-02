package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/config"
)

// TestCheckDaemonConfigMissing verifies that missing configuration items trigger pre-flight warnings.
func TestCheckDaemonConfigMissing(t *testing.T) {
	rt := NewRuntime()
	// Populate empty config directly.
	rt.cfg = &config.Config{
		Probe:  config.ProbeConfig{VideoCourseID: 0},
		Notify: config.NotifyConfig{Command: nil},
	}
	rt.once.Do(func() {}) // Mark as loaded.

	var stderr bytes.Buffer
	checkDaemonConfig(rt, &stderr)

	output := stderr.String()
	if !strings.Contains(output, "config probe.video_course_id not set! You may not be able to probe video stream status automatically.") {
		t.Errorf("expected probe.video_course_id warning, got: %s", output)
	}
	if !strings.Contains(output, "config notify.command not set! You may not be able to receive notifications on health transitions.") {
		t.Errorf("expected notify.command warning, got: %s", output)
	}
}

// TestCheckDaemonConfigPresent verifies that fully configured settings produce no warnings.
func TestCheckDaemonConfigPresent(t *testing.T) {
	rt := NewRuntime()
	rt.cfg = &config.Config{
		Probe:  config.ProbeConfig{VideoCourseID: 12345},
		Notify: config.NotifyConfig{Command: []string{"curl", "https://example.com"}},
	}
	rt.once.Do(func() {})

	var stderr bytes.Buffer
	checkDaemonConfig(rt, &stderr)

	if stderr.Len() != 0 {
		t.Errorf("expected no warnings, got: %s", stderr.String())
	}
}

// TestCurrentBinaryPath verifies that currentBinaryPath returns an absolute file path.
func TestCurrentBinaryPath(t *testing.T) {
	path, err := currentBinaryPath()
	if err != nil {
		t.Fatalf("currentBinaryPath failed: %v", err)
	}
	if path == "" {
		t.Fatal("expected non-empty binary path")
	}
}

// TestDaemonSubcommandsStructure verifies that daemon install and uninstall subcommands and aliases are registered.
func TestDaemonSubcommandsStructure(t *testing.T) {
	var stdout, stderr bytes.Buffer
	cmd := newDaemonCmd(NewRuntime(), &stdout, &stderr)

	installCmd, _, err := cmd.Find([]string{"install"})
	if err != nil || installCmd == nil {
		t.Fatal("expected 'install' subcommand to exist")
	}
	enableCmd, _, err := cmd.Find([]string{"enable"})
	if err != nil || enableCmd != installCmd {
		t.Fatal("expected 'enable' alias to resolve to 'install' command")
	}

	uninstallCmd, _, err := cmd.Find([]string{"uninstall"})
	if err != nil || uninstallCmd == nil {
		t.Fatal("expected 'uninstall' subcommand to exist")
	}
	disableCmd, _, err := cmd.Find([]string{"disable"})
	if err != nil || disableCmd != uninstallCmd {
		t.Fatal("expected 'disable' alias to resolve to 'uninstall' command")
	}
}

// TestServiceTemplateRender pins the service-definition contract: each
// template carries exactly one binary-path placeholder, and rendering puts
// the resolved path where the service manager expects it with no residue.
// A broken render installs a unit that cannot start, and the failure only
// surfaces at the user's machine - so it is caught here instead.
func TestServiceTemplateRender(t *testing.T) {
	const bin = "/fake/dir/sjtu"
	templates := map[string]string{
		"launchd": launchdPlistTemplate,
		"systemd": systemdServiceTemplate,
	}
	for name, tmpl := range templates {
		if got := strings.Count(tmpl, binaryPathPlaceholder); got != 1 {
			t.Errorf("%s: placeholder count = %d, want 1", name, got)
		}
		out := renderServiceTemplate(tmpl, bin)
		if strings.Contains(out, binaryPathPlaceholder) {
			t.Errorf("%s: placeholder left in rendered output", name)
		}
		if !strings.Contains(out, bin) {
			t.Errorf("%s: rendered output lacks binary path", name)
		}
	}

	plist := renderServiceTemplate(launchdPlistTemplate, bin)
	if !strings.Contains(plist, "<string>"+bin+"</string>") {
		t.Error("launchd: binary path not inside a ProgramArguments <string> element")
	}
	unit := renderServiceTemplate(systemdServiceTemplate, bin)
	if !strings.Contains(unit, "ExecStart="+bin+" daemon\n") {
		t.Error("systemd: ExecStart line malformed")
	}
}
