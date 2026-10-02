package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// notifyHookTimeout bounds one notification hook run.
const notifyHookTimeout = 5 * time.Second

// notifier dispatches health-transition events over two independent
// channels: the configured command receives the event JSON on stdin, and a
// desktop notification pops whenever a desktop session exists. The desktop
// channel is not a fallback: both fire when both are available.
type notifier struct {
	command []string
}

// notify sends one event. The event shape is fixed:
//
//	{"event":"fail|recover","domain":"...","detail":"...","at":"RFC3339"}
//
// Hooks run with a small timeout under the daemon's cancel tree; a failing
// hook is logged and otherwise ignored.
func (n notifier) notify(ctx context.Context, event, domain string, st DomainState) {
	payload, err := json.Marshal(map[string]string{
		"event":  event,
		"domain": domain,
		"detail": st.Detail,
		"at":     st.At,
	})
	if err != nil {
		slog.Warn("notify event marshal failed", "event", event, "domain", domain, "error", err)
		return
	}
	if len(n.command) > 0 {
		hookCtx, cancel := context.WithTimeout(ctx, notifyHookTimeout)
		defer cancel()
		cmd := exec.CommandContext(hookCtx, n.command[0], n.command[1:]...)
		cmd.Stdin = bytes.NewReader(payload)
		if out, err := cmd.CombinedOutput(); err != nil {
			slog.Warn("notify hook failed", "event", event, "domain", domain,
				"error", err, "output", strings.TrimSpace(string(out)))
		}
	}
	if desktopAvailable() {
		desktopNotify(domain, event, st.Detail)
	}
}

// desktopAvailable reports whether a desktop session exists to pop a
// notification on: always on macOS; on Linux when a freedesktop session
// bus is reachable — DBUS_SESSION_BUS_ADDRESS is set, or the well-known
// user bus socket exists (the shape systemd user services see). Headless
// machines have neither, and that is what notify.command is for.
func desktopAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		return true
	case "linux":
		if os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "" {
			return true
		}
		if dir := os.Getenv("XDG_RUNTIME_DIR"); dir != "" {
			_, err := os.Stat(filepath.Join(dir, "bus"))
			return err == nil
		}
	}
	return false
}

// desktopNotify pops the platform's desktop notification. Failures are
// silently dropped: the notifier binary may be absent even with a desktop.
func desktopNotify(domain, event, detail string) {
	title := "sjtu daemon"
	body := domain + " " + event
	if detail != "" {
		body += ": " + detail
	}
	switch runtime.GOOS {
	case "darwin":
		exec.Command("osascript", "-e",
			`display notification `+appleScriptQuote(body)+` with title `+appleScriptQuote(title)).Run()
	case "linux":
		exec.Command("notify-send", title, body).Run()
	}
}

// appleScriptQuote renders s as an AppleScript string literal.
func appleScriptQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}
