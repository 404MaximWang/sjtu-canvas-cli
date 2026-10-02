package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"
)

// Output bounds of the two /run modes.
const (
	// maxBufferedStdout caps a buffered run's stdout; beyond it the caller
	// is told to use stream mode.
	maxBufferedStdout = 32 << 20 // 32 MiB
	// maxStderr caps captured stderr in both modes; excess is dropped.
	maxStderr = 1 << 20 // 1 MiB
	// maxRequestBody caps the /run request envelope.
	maxRequestBody = 1 << 20
)

// exitUsage mirrors the CLI's usage-error exit code without importing cli.
const exitUsage = 2

// errStdoutTooLarge aborts a buffered command whose stdout overflowed: the
// producer sees the write error and stops, and the response becomes
// output_too_large_use_stream.
var errStdoutTooLarge = errors.New("stdout exceeds the buffered-mode cap")

// runRequest is the POST /run envelope.
type runRequest struct {
	Argv   []string `json:"argv"`
	Stream bool     `json:"stream"`
}

// handleRun executes the requested argv in one of the two response modes
// the stream flag selects.
func (d *Daemon) handleRun(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody)).Decode(&req); err != nil {
		writeBadRequest(w, "body must be one JSON object")
		return
	}
	if len(req.Argv) == 0 {
		writeBadRequest(w, "argv must be a non-empty string array")
		return
	}
	if disabledOnSocket(req.Argv) {
		writeBuffered(w, map[string]any{
			"code":   exitUsage,
			"stdout": "",
			"stderr": fmt.Sprintf(`{"error":"usage","hint":%q}`+"\n",
				"command not available over the daemon socket: "+strings.Join(req.Argv, " ")),
		})
		return
	}

	// The command dies with the daemon or when the client disconnects,
	// whichever comes first; the daemon itself keeps running.
	ctx, stop := context.WithCancel(d.ctx)
	defer stop()
	defer context.AfterFunc(r.Context(), stop)()

	if req.Stream {
		d.runStream(ctx, w, req.Argv)
	} else {
		d.runBuffered(ctx, w, req.Argv)
	}
}

// writeBadRequest rejects a malformed request envelope.
func writeBadRequest(w http.ResponseWriter, detail string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusBadRequest)
	fmt.Fprintf(w, `{"error":"bad_request","detail":%q}`+"\n", detail)
}

// writeBuffered answers a buffered-mode run: HTTP is always 200, the
// command's success lives in the body's code field.
func writeBuffered(w http.ResponseWriter, resp map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// disabledOnSocket reports commands that cannot run without a terminal or
// would fork a second daemon: interactive logins, the TUI, daemon itself.
func disabledOnSocket(argv []string) bool {
	switch argv[0] {
	case "daemon", "tui":
		return true
	case "auth":
		for _, arg := range argv[1:] {
			if arg == "login" {
				return true
			}
		}
	}
	return false
}

// runOnce executes argv with the credential reload policy: a failure
// blaming a credential domain reloads that domain from the store and
// retries exactly once, so a human re-login is picked up without a daemon
// restart. reset clears the captured output for the retry and reports
// whether a retry is still possible (stream mode forbids it once bytes
// flowed). The final auth outcome refreshes the health tracker; failures
// that are not credential-related never touch it.
func (d *Daemon) runOnce(ctx context.Context, argv []string, stdout, stderr io.Writer, reset func() bool) int {
	code, credDomain := d.hooks.Run(ctx, argv, stdout, stderr)
	if credDomain == "" {
		return code
	}
	if !reset() {
		d.noteRequestOutcome(ctx, credDomain, false)
		return code
	}
	slog.Info("command failed on credentials, reloading and retrying", "domain", credDomain, "argv", argv)
	if err := d.hooks.ReloadCred(credDomain); err != nil {
		slog.Warn("credential reload failed", "domain", credDomain, "error", err)
		d.noteRequestOutcome(ctx, credDomain, false)
		return code
	}
	first := credDomain
	code, credDomain = d.hooks.Run(ctx, argv, stdout, stderr)
	if credDomain == "" {
		d.noteRequestOutcome(ctx, first, true) // the reload revived the domain
	} else {
		d.noteRequestOutcome(ctx, credDomain, false)
	}
	return code
}

// runBuffered executes argv with capped buffers and answers with the
// buffered response shape.
func (d *Daemon) runBuffered(ctx context.Context, w http.ResponseWriter, argv []string) {
	stdout := &cappedBuffer{cap: maxBufferedStdout}
	stdout.abort = errStdoutTooLarge
	stderr := &cappedBuffer{cap: maxStderr}
	code := d.runOnce(ctx, argv, stdout, stderr, func() bool {
		stdout.Reset()
		stderr.Reset()
		return true
	})
	out := stdout.Bytes()
	switch {
	case stdout.overflow:
		writeBuffered(w, map[string]any{"code": -1, "error": "output_too_large_use_stream"})
	case !utf8.Valid(out):
		writeBuffered(w, map[string]any{"code": -1, "error": "binary_output_use_stream"})
	default:
		stderrText, lossy := lossyString(stderr.Bytes())
		resp := map[string]any{"code": code, "stdout": string(out), "stderr": stderrText}
		if lossy {
			resp["stderr_lossy"] = true
		}
		if stderr.overflow {
			resp["stderr_truncated"] = true
		}
		writeBuffered(w, resp)
	}
}

// runStream executes argv with stdout streaming straight to the client. The
// response headers go out with the first stdout byte or at command exit,
// whichever comes first: exit 0 before any output answers 200 with an empty
// body, a failure answers 500 with a JSON error body carrying the captured
// stderr. Once bytes flowed, a failure can only surface as an interrupted
// transfer, so the connection is closed mid-stream.
func (d *Daemon) runStream(ctx context.Context, w http.ResponseWriter, argv []string) {
	tw := &triggerWriter{w: w}
	stderr := &cappedBuffer{cap: maxStderr}
	code := d.runOnce(ctx, argv, tw, stderr, func() bool {
		if tw.started {
			return false // bytes already flowed; a retry cannot be reported
		}
		stderr.Reset()
		return true
	})
	if !tw.started {
		w.Header().Set("Content-Type", "application/json")
		if code == 0 {
			w.WriteHeader(http.StatusOK) // exit 0 with no output: empty body
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		stderrText, _ := lossyString(stderr.Bytes())
		json.NewEncoder(w).Encode(map[string]any{"code": code, "error": "command_failed", "stderr": stderrText})
		return
	}
	if code != 0 {
		// Bytes already flowed, so the failure cannot be reported in-band.
		// Closing the connection without terminating the chunked body
		// surfaces as an interrupted transfer (curl exit 18), which is
		// safe to retry.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				conn.Close()
			}
		}
	}
}

// triggerWriter commits the 200 response headers on the first stdout byte.
type triggerWriter struct {
	w       http.ResponseWriter
	started bool
	err     error
}

func (t *triggerWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if t.err != nil {
		return 0, t.err
	}
	if !t.started {
		t.started = true
		t.w.Header().Set("Content-Type", "application/octet-stream")
		t.w.WriteHeader(http.StatusOK)
	}
	n, err := t.w.Write(p)
	if f, ok := t.w.(http.Flusher); ok {
		f.Flush()
	}
	if err != nil {
		t.err = err
	}
	return n, err
}

// cappedBuffer is a bytes.Buffer bounded to cap bytes. Writes past the cap
// are dropped and set overflow; when abort is set (buffered-mode stdout) the
// write instead fails with abort, cancelling the producing command. stderr
// buffers never abort: the diagnostic channel must never strangle a command.
type cappedBuffer struct {
	buf      bytes.Buffer
	cap      int
	overflow bool
	abort    error
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.overflow && b.abort != nil {
		return 0, b.abort
	}
	room := b.cap - b.buf.Len()
	if room < len(p) {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.overflow = true
		if b.abort != nil {
			return room, b.abort
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

// Bytes returns the captured prefix.
func (b *cappedBuffer) Bytes() []byte { return b.buf.Bytes() }

// Reset drops the captured content and the overflow flag for the retry.
func (b *cappedBuffer) Reset() {
	b.buf.Reset()
	b.overflow = false
}

// lossyString renders b as UTF-8, dropping invalid sequences; the bool
// reports whether anything was dropped.
func lossyString(b []byte) (string, bool) {
	if utf8.Valid(b) {
		return string(b), false
	}
	return strings.ToValidUTF8(string(b), ""), true
}
