// Package daemon runs the sjtu daemon: a purely passive local service that
// executes the same command implementations as the CLI over HTTP on a unix
// socket, plus health probes and transition notifications.
//
// The socket is ~/.local/state/sjtu/sjtud.sock (mode 0600; the file
// permission is the authentication). There are two endpoints: POST /run
// executes an argv, GET /healthz reports per-domain health. Nothing else
// exists; every other path is a 404.
//
// The daemon is an optional accelerator, not infrastructure: its only
// reason to exist is keeping the jAccount/Canvas sessions warm so requests
// skip the SSO chain replays. The CLI frontend never probes the socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/logging"
)

// Health-checked domains.
const (
	DomainCanvas    = "canvas"
	DomainJAccount  = "jaccount"
	DomainVideo     = "video"
	DomainMLearning = "mlearning"
)

// Domains lists every health-checked domain in stable order.
var Domains = []string{DomainCanvas, DomainJAccount, DomainVideo, DomainMLearning}

// credentialDomain maps a health domain to the credential domain whose
// reload can revive it: the three jAccount-backed domains share one root
// credential, the JAAuthCookie.
var credentialDomain = map[string]string{
	DomainCanvas:    DomainCanvas,
	DomainJAccount:  DomainJAccount,
	DomainVideo:     DomainJAccount,
	DomainMLearning: DomainJAccount,
}

// ErrProbeUnconfigured marks a probe that cannot run for missing
// configuration; the domain reports unknown, never fail.
var ErrProbeUnconfigured = errors.New("probe.video_course_id not configured")

// authFailure wraps a probe failure that blames dead credentials.
type authFailure struct{ err error }

func (e authFailure) Error() string { return e.err.Error() }
func (e authFailure) Unwrap() error { return e.err }

// MarkAuth classifies a probe failure as a dead-credential failure. The
// health tracker flips a domain to fail only for marked errors; transient
// network failures are logged but never move healthz.
func MarkAuth(err error) error { return authFailure{err} }

// isAuthFailure reports whether MarkAuth classified the error.
func isAuthFailure(err error) bool {
	var af authFailure
	return errors.As(err, &af)
}

// Hooks wire the daemon to the command implementation and credentials; the
// cli package provides them, keeping daemon free of command knowledge.
type Hooks struct {
	// Run executes one argv with the command's stdout/stderr and returns
	// the process-style exit code. credDomain is non-empty when the
	// failure blames that credential domain (DomainCanvas/DomainJAccount).
	Run func(ctx context.Context, argv []string, stdout, stderr io.Writer) (code int, credDomain string)
	// ReloadCred rebuilds one credential domain from the credential store.
	ReloadCred func(domain string) error
	// Probes are the per-domain health checks. A MarkAuth-wrapped error
	// means dead credentials; any other error is transient.
	Probes map[string]func(ctx context.Context) error
	// NotifyCommand is the argv array executed on every health transition,
	// with the event JSON on stdin. Empty falls back to a desktop
	// notification.
	NotifyCommand []string
}

// shutdownGrace bounds how long SIGTERM waits for in-flight requests.
const shutdownGrace = 5 * time.Second

// probeInterval is the health self-check period.
const probeInterval = time.Hour

// Daemon serves the socket and tracks health.
type Daemon struct {
	hooks  Hooks
	health *health
	ctx    context.Context // daemon lifetime; command contexts derive from it
}

// Run starts the daemon and blocks until ctx is canceled (SIGINT/SIGTERM
// from main). The startup health round completes before the socket accepts
// requests. A second instance against a live socket exits with an error; a
// stale socket file is reclaimed.
func Run(ctx context.Context, hooks Hooks) error {
	dir, err := logging.StateDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create state dir: %w", err)
	}
	sockPath := filepath.Join(dir, "sjtud.sock")

	// A socket that accepts a connection is a live daemon; refuse to
	// double-start. Anything else is a stale file from a dead process.
	if conn, err := net.DialTimeout("unix", sockPath, time.Second); err == nil {
		conn.Close()
		return fmt.Errorf("daemon already running: %s accepts connections", sockPath)
	}
	if err := os.Remove(sockPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale socket: %w", err)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", sockPath, err)
	}
	defer os.Remove(sockPath)
	// The socket file's permission is the authentication; enforce 0600
	// regardless of the process umask.
	if err := os.Chmod(sockPath, 0o600); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	d := &Daemon{
		hooks:  hooks,
		health: newHealth(notifier{command: hooks.NotifyCommand}.notify),
		ctx:    ctx,
	}
	// Startup round: probe every domain before accepting requests, so
	// healthz never reports a domain that was never checked.
	d.probeRound(ctx)
	go d.probeLoop(ctx)

	srv := &http.Server{Handler: d.mux()}
	go func() {
		<-ctx.Done()
		// Stop accepting, let in-flight requests finish within the grace
		// period, then force-close the rest. systemd allows 15 s
		// (TimeoutStopSec) before SIGKILL; the application-level wait is
		// deliberately shorter.
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Warn("graceful shutdown incomplete, forcing close", "error", err)
		}
		srv.Close()
	}()

	slog.Info("daemon listening", "socket", sockPath)
	err = srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// mux routes the two endpoints; every other path and method 404s/405s.
func (d *Daemon) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /run", d.handleRun)
	mux.HandleFunc("GET /healthz", d.handleHealthz)
	return mux
}

// probeLoop re-runs the health round hourly until shutdown.
func (d *Daemon) probeLoop(ctx context.Context) {
	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.probeRound(ctx)
		}
	}
}

// probeRound probes every domain once.
func (d *Daemon) probeRound(ctx context.Context) {
	for _, domain := range Domains {
		d.probeDomain(ctx, domain)
	}
}

// probeDomain runs one domain's probe and records the outcome. A
// dead-credential failure first triggers a credential reload and one
// re-probe: a human re-login is picked up without any daemon restart.
func (d *Daemon) probeDomain(ctx context.Context, domain string) {
	probe := d.hooks.Probes[domain]
	err := probe(ctx)
	if errors.Is(err, ErrProbeUnconfigured) {
		d.health.set(ctx, domain, stateUnknown, err.Error())
		return
	}
	if isAuthFailure(err) {
		if rerr := d.hooks.ReloadCred(credentialDomain[domain]); rerr != nil {
			slog.Warn("credential reload failed", "domain", domain, "error", rerr)
		} else {
			switch err = probe(ctx); {
			case err == nil:
				d.health.set(ctx, domain, stateOK, "")
				return
			case isAuthFailure(err):
				// fall through to fail below
			default:
				slog.Warn("health probe failed after credential reload", "domain", domain, "error", err)
				return
			}
		}
		slog.Warn("health probe reports dead credentials", "domain", domain, "error", err)
		d.health.set(ctx, domain, stateFail, remediation(domain))
		return
	}
	if err != nil {
		// Transient failure: log it, but healthz keeps the last known
		// state — only credential failures move it.
		slog.Warn("health probe failed", "domain", domain, "error", err)
		return
	}
	d.health.set(ctx, domain, stateOK, "")
}

// remediation is the recovery instruction a failed domain's detail carries.
func remediation(domain string) string {
	if domain == DomainCanvas {
		return "Canvas credential rejected; run `sjtu auth canvas login` to log in again"
	}
	return "jAccount session rejected; run `sjtu auth jaccount login` to log in again"
}
