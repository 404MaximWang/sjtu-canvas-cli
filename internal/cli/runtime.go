package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/config"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/cred"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/daemon"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/jaccount"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/mlearning"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/vfs"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/video"
)

// Credential domains a command failure can blame; the daemon reloads the
// blamed domain from the credential store and retries the command once.
const (
	DomainCanvas   = daemon.DomainCanvas
	DomainJAccount = daemon.DomainJAccount
)

// jaState bundles the jAccount-backed long-lived clients sharing one cookie
// session. The jar carries the mlearning Access_Token and the Canvas web
// session established by the video warmup — the session warmth the daemon
// exists to preserve.
type jaState struct {
	cookie string // raw JAAuthCookie, kept for jaccount.Verify
	sess   *session.Session
	video  *video.Client
	ml     *mlearning.Client
}

// videoSource adapts the warm client to the vfs source interface, with the
// auth-stub policy preserved: no credential means every call answers a
// structured auth error instead of the tree losing the nodes.
func (j *jaState) videoSource() vfs.VideoSource {
	if j == nil {
		return videoAuthSource{}
	}
	return j.video
}

// attendanceSource adapts the warm client like videoSource does.
func (j *jaState) attendanceSource() vfs.AttendanceSource {
	if j == nil {
		return attendanceAuthSource{}
	}
	return j.ml
}

// Runtime carries the process's shared state: the config and the warm
// credential sessions. The CLI builds one per process and loads it lazily
// on first use, so commands that need no credentials (auth, update, help)
// never touch the store. The daemon shares one Runtime across requests and
// reloads a domain when its credential dies or is re-issued.
type Runtime struct {
	once    sync.Once
	loadErr error
	cfg     *config.Config

	mu          sync.RWMutex // guards canvasToken and ja
	canvasToken string       // "" when no Canvas credential is stored
	ja          *jaState     // nil when no jAccount credential is stored
}

// NewRuntime returns an unloaded Runtime.
func NewRuntime() *Runtime { return &Runtime{} }

// load performs the one-time config load and credential read.
func (r *Runtime) load() error {
	r.once.Do(func() {
		cfg, err := config.Load()
		if err != nil {
			r.loadErr = fmt.Errorf("load config: %w", err)
			return
		}
		r.cfg = cfg
		r.loadErr = r.reload()
	})
	return r.loadErr
}

// Config returns the loaded configuration.
func (r *Runtime) Config() (*config.Config, error) {
	if err := r.load(); err != nil {
		return nil, err
	}
	return r.cfg, nil
}

// reload reads the credential store and rebuilds both domains' sessions.
// One store read dominates the cost, so a single-domain reload rebuilds
// both; the dropped jAccount jar re-warms lazily on next use.
func (r *Runtime) reload() error {
	store, err := openStore()
	if err != nil {
		return err
	}
	token, err := getCredential(store, cred.KeyCanvas)
	switch {
	case errors.Is(err, cred.ErrNotFound):
		token = ""
	case err != nil:
		return err
	}
	var ja *jaState
	cookie, err := getCredential(store, cred.KeyJAccount)
	switch {
	case errors.Is(err, cred.ErrNotFound):
	case err != nil:
		return err
	default:
		sess, err := session.NewCookies()
		if err != nil {
			return err
		}
		sess.SeedCookie("JAAuthCookie", cookie, "jaccount.sjtu.edu.cn", "my.sjtu.edu.cn")
		ja = &jaState{cookie: cookie, sess: sess, video: video.New(sess), ml: mlearning.New(sess)}
	}
	r.mu.Lock()
	r.canvasToken, r.ja = token, ja
	r.mu.Unlock()
	return nil
}

// ReloadDomain rebuilds one credential domain from the store after its
// credential died or was re-issued (the human re-logged in).
func (r *Runtime) ReloadDomain(domain string) error {
	if err := r.load(); err != nil {
		return err
	}
	switch domain {
	case DomainCanvas, DomainJAccount:
		return r.reload()
	default:
		return fmt.Errorf("unknown credential domain %q", domain)
	}
}

// sessions snapshots the warm sessions under the read lock.
func (r *Runtime) sessions() (canvasToken string, ja *jaState) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.canvasToken, r.ja
}

// openVFS assembles the Canvas-backed file system for one command run over
// the shared sessions: a fresh vfs tree per request, one warm session set
// per process. See daemonHooks for the reload policy.
//
// With lenient=false a missing credential is a hard error (CLI behavior:
// auth_required). With lenient=true the tree is built over an empty token
// (TUI behavior: the startup warning explains, and every fetch then fails
// with 401 until the user logs in).
func (r *Runtime) openVFS(ctx context.Context, lenient bool) (*vfs.FS, *canvas.Client, bool, error) {
	if err := r.load(); err != nil {
		return nil, nil, false, err
	}
	token, ja := r.sessions()
	if token == "" && !lenient {
		return nil, nil, false, cred.ErrNotFound // classifies as auth_required
	}
	client := canvas.New(session.NewToken(token), r.cfg.CanvasBaseURL)
	src, err := vfs.NewCachedSource(vfs.CanvasSource(client))
	if err != nil {
		return nil, nil, false, err
	}
	return vfs.New(ctx, src, ja.videoSource(), ja.attendanceSource()), client, token != "", nil
}

// submitAttendance implements `sjtu attendance submit` over the shared
// jAccount session: the scanned URL rides the SSO chain on the warm jar,
// then the scan endpoint is called and its response passed through.
func (r *Runtime) submitAttendance(ctx context.Context, rawURL string, stdout io.Writer) error {
	if err := r.load(); err != nil {
		return err
	}
	_, ja := r.sessions()
	if ja == nil {
		return mlearning.ErrAuth
	}
	resp, err := ja.ml.Submit(ctx, rawURL)
	if errors.Is(err, mlearning.ErrInvalidURL) {
		return fail("usage", err.Error(), exitUsage)
	}
	if err != nil {
		return err
	}
	_, err = stdout.Write(append(resp, '\n'))
	return err
}

// daemonHooks assembles the daemon's callbacks over this runtime.
func (r *Runtime) daemonHooks() (daemon.Hooks, error) {
	cfg, err := r.Config()
	if err != nil {
		return daemon.Hooks{}, err
	}
	return daemon.Hooks{
		Run: func(ctx context.Context, argv []string, stdout, stderr io.Writer) (int, string) {
			return ExecuteWith(r, ctx, argv, stdout, stderr)
		},
		ReloadCred:    r.ReloadDomain,
		NotifyCommand: cfg.Notify.Command,
		Probes: map[string]func(context.Context) error{
			daemon.DomainCanvas:    r.probeCanvas,
			daemon.DomainJAccount:  r.probeJAccount,
			daemon.DomainVideo:     r.probeVideo,
			daemon.DomainMLearning: r.probeMLearning,
		},
	}, nil
}

// probeCanvas checks the Canvas token with the lightest authenticated call.
func (r *Runtime) probeCanvas(ctx context.Context) error {
	cfg, err := r.Config()
	if err != nil {
		return err
	}
	token, _ := r.sessions()
	if token == "" {
		return daemon.MarkAuth(cred.ErrNotFound)
	}
	_, err = canvas.VerifyToken(ctx, cfg.CanvasBaseURL, token)
	return classifyProbeErr(err)
}

// probeJAccount re-runs the jAccount warmup chain on a fresh session, the
// same check the TUI startup performs.
func (r *Runtime) probeJAccount(ctx context.Context) error {
	if err := r.load(); err != nil {
		return err
	}
	_, ja := r.sessions()
	if ja == nil {
		return daemon.MarkAuth(cred.ErrNotFound)
	}
	return classifyProbeErr(jaccount.Verify(ctx, ja.cookie))
}

// probeVideo runs the full launch chain plus one authenticated vod_live
// call against the configured probe course: only a real API call proves
// the minted jwt passes the gateway. Unconfigured probes report unknown.
func (r *Runtime) probeVideo(ctx context.Context) error {
	cfg, err := r.Config()
	if err != nil {
		return err
	}
	if cfg.Probe.VideoCourseID == 0 {
		return daemon.ErrProbeUnconfigured
	}
	_, ja := r.sessions()
	if ja == nil {
		return daemon.MarkAuth(cred.ErrNotFound)
	}
	_, err = ja.video.LiveSessions(ctx, cfg.Probe.VideoCourseID)
	return classifyProbeErr(err)
}

// probeMLearning hits users/self through the client, which refreshes a dead
// Access_Token itself; a failure here means the JAAuthCookie is dead.
func (r *Runtime) probeMLearning(ctx context.Context) error {
	if err := r.load(); err != nil {
		return err
	}
	_, ja := r.sessions()
	if ja == nil {
		return daemon.MarkAuth(cred.ErrNotFound)
	}
	_, err := ja.ml.Self(ctx)
	return classifyProbeErr(err)
}

// classifyProbeErr marks a probe failure that blames dead credentials; the
// daemon flips a domain's health to fail only for marked failures, so
// transient network errors never move healthz.
func classifyProbeErr(err error) error {
	if err == nil {
		return nil
	}
	var httpErr *session.HTTPError
	switch {
	case errors.Is(err, cred.ErrNotFound),
		errors.Is(err, video.ErrAuth),
		errors.Is(err, mlearning.ErrAuth):
		return daemon.MarkAuth(err)
	case errors.As(err, &httpErr) &&
		(strings.HasPrefix(httpErr.Status, "401") || strings.HasPrefix(httpErr.Status, "403")):
		return daemon.MarkAuth(err)
	}
	// jaccount.Verify surfaces a dead cookie as a JSON syntax error: the
	// chain lands on the login page, which is HTML, not JSON.
	var synErr *json.SyntaxError
	if errors.As(err, &synErr) {
		return daemon.MarkAuth(err)
	}
	return err
}

// authDomainOf maps a command failure to the credential domain it blames,
// or "" when the failure is not authentication-related. The video and
// mlearning chains both surface a dead JAAuthCookie as their ErrAuth.
func authDomainOf(err error) string {
	switch {
	case errors.Is(err, video.ErrAuth),
		errors.Is(err, mlearning.ErrAuth),
		errors.Is(err, errVideoAuth),
		errors.Is(err, errAttendanceAuth):
		return DomainJAccount
	}
	var httpErr *session.HTTPError
	if errors.As(err, &httpErr) && strings.HasPrefix(httpErr.Status, "401") {
		return DomainCanvas
	}
	// A bare missing credential reaches here only from openVFS in strict
	// mode; the attendance path wraps its own in mlearning.ErrAuth.
	if errors.Is(err, cred.ErrNotFound) {
		return DomainCanvas
	}
	return ""
}
