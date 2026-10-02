package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// stubRun records invocations and replays scripted outcomes.
type stubRun struct {
	calls   [][]string
	scripts []stubOutcome
}

type stubOutcome struct {
	stdout     string
	stderr     string
	code       int
	credDomain string
}

func (s *stubRun) run(_ context.Context, argv []string, stdout, stderr io.Writer) (int, string) {
	s.calls = append(s.calls, argv)
	out := s.scripts[len(s.calls)-1]
	io.WriteString(stdout, out.stdout)
	io.WriteString(stderr, out.stderr)
	return out.code, out.credDomain
}

// testDaemon builds a Daemon with stubbed hooks and captured notifications.
func testDaemon(run *stubRun) (*Daemon, *[]string, *[]string) {
	events := &[]string{}
	reloads := &[]string{}
	d := &Daemon{
		hooks: Hooks{
			Run:        run.run,
			ReloadCred: func(domain string) error { *reloads = append(*reloads, domain); return nil },
			Probes:     map[string]func(context.Context) error{},
		},
		ctx: context.Background(),
	}
	d.health = newHealth(func(_ context.Context, event, domain string, _ DomainState) {
		*events = append(*events, event+":"+domain)
	})
	return d, events, reloads
}

// serveRun posts body to the /run handler and returns the recorder.
func serveRun(d *Daemon, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/run", strings.NewReader(body))
	rec := httptest.NewRecorder()
	d.handleRun(rec, req)
	return rec
}

func TestRunRejectsMalformedRequests(t *testing.T) {
	d, _, _ := testDaemon(&stubRun{})
	for _, body := range []string{
		`{}`,                          // argv missing
		`{"argv":[]}`,                 // argv empty
		`{"argv":[42]}`,               // non-string element
		`{"argv":["ls"`,               // malformed JSON
		`{"argv":["ls"],"stream":1x}`, // malformed JSON
	} {
		rec := serveRun(d, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s: got status %d, want 400", body, rec.Code)
		}
		var resp struct {
			Error  string `json:"error"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("body %s: response not JSON: %v", body, err)
		}
		if resp.Error != "bad_request" || resp.Detail == "" {
			t.Fatalf("body %s: got %+v", body, resp)
		}
	}
}

func TestRunRejectsSocketDisabledCommands(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{{code: 0, stdout: "ok"}}}
	d, _, _ := testDaemon(run)
	for _, argv := range []string{
		`["daemon"]`, `["tui"]`, `["auth","canvas","login"]`, `["auth","jaccount","login"]`,
		`["auth","logout"]`, `["auth","canvas","logout"]`,
	} {
		rec := serveRun(d, `{"argv":`+argv+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("argv %s: got status %d, want 200", argv, rec.Code)
		}
		var resp struct {
			Code   int    `json:"code"`
			Stderr string `json:"stderr"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Code != exitUsage || !strings.Contains(resp.Stderr, "usage") {
			t.Fatalf("argv %s: got %+v", argv, resp)
		}
	}
	if len(run.calls) != 0 {
		t.Fatalf("disabled commands reached the runner: %v", run.calls)
	}

	// auth status is not interactive and stays available.
	run.scripts = []stubOutcome{{code: 0, stdout: "status"}}
	serveRun(d, `{"argv":["auth","status"]}`)
	if len(run.calls) != 1 {
		t.Fatalf("auth status did not reach the runner")
	}
}

func TestBufferedSuccess(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{{code: 0, stdout: "hello", stderr: "note"}}}
	d, _, _ := testDaemon(run)
	rec := serveRun(d, `{"argv":["cat","/x"]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d", rec.Code)
	}
	var resp struct {
		Code   int    `json:"code"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != 0 || resp.Stdout != "hello" || resp.Stderr != "note" {
		t.Fatalf("got %+v", resp)
	}
}

func TestBufferedRejectsBinaryStdout(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{{code: 0, stdout: "\xff\xfe"}}}
	d, _, _ := testDaemon(run)
	rec := serveRun(d, `{"argv":["cat","/x"]}`)
	var resp struct {
		Code  int    `json:"code"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != -1 || resp.Error != "binary_output_use_stream" {
		t.Fatalf("got %+v", resp)
	}
}

func TestBufferedStderrLossyAndTruncated(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{{
		code:   0,
		stdout: "ok",
		stderr: string([]byte{'a', 0xff}) + strings.Repeat("x", maxStderr),
	}}}
	d, _, _ := testDaemon(run)
	rec := serveRun(d, `{"argv":["x"]}`)
	var resp struct {
		Code            int    `json:"code"`
		Stderr          string `json:"stderr"`
		StderrLossy     bool   `json:"stderr_lossy"`
		StderrTruncated bool   `json:"stderr_truncated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != 0 || !resp.StderrLossy || !resp.StderrTruncated {
		t.Fatalf("got %+v", resp)
	}
	if strings.ContainsRune(resp.Stderr, 0xff) || len(resp.Stderr) > maxStderr {
		t.Fatalf("stderr not sanitized: len %d", len(resp.Stderr))
	}
}

func TestBufferedReloadAndRetryOnce(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{
		{code: 3, stderr: "stale", credDomain: DomainCanvas},
		{code: 0, stdout: "fresh"},
	}}
	d, _, reloads := testDaemon(run)
	rec := serveRun(d, `{"argv":["ls","/courses"]}`)
	var resp struct {
		Code   int    `json:"code"`
		Stdout string `json:"stdout"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(run.calls) != 2 || len(*reloads) != 1 || (*reloads)[0] != DomainCanvas {
		t.Fatalf("calls %v reloads %v", run.calls, *reloads)
	}
	// Only the retried attempt's output is visible.
	if resp.Code != 0 || resp.Stdout != "fresh" || resp.Stderr != "" {
		t.Fatalf("got %+v", resp)
	}
	// The revived domain recovered.
	if st := d.health.snapshot()[DomainCanvas]; st.State != stateOK {
		t.Fatalf("canvas state %q, want ok", st.State)
	}
}

func TestBufferedPersistentAuthFailureMarksFail(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{
		{code: 3, credDomain: DomainJAccount},
		{code: 3, credDomain: DomainJAccount},
	}}
	d, events, _ := testDaemon(run)
	serveRun(d, `{"argv":["cat","/courses/1/attendance/status"]}`)
	st := d.health.snapshot()[DomainJAccount]
	if st.State != stateFail || !strings.Contains(st.Detail, "jaccount login") {
		t.Fatalf("jaccount state %+v, want fail with remediation", st)
	}
	if len(*events) != 1 || (*events)[0] != "fail:"+DomainJAccount {
		t.Fatalf("events %v, want exactly one fail", *events)
	}
}

func TestStreamSuccess(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{{code: 0, stdout: "payload-bytes"}}}
	d, _, _ := testDaemon(run)
	rec := serveRun(d, `{"argv":["cat","/x"],"stream":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d", rec.Code)
	}
	if rec.Body.String() != "payload-bytes" {
		t.Fatalf("got body %q", rec.Body.String())
	}
}

func TestStreamEmptySuccess(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{{code: 0}}}
	d, _, _ := testDaemon(run)
	rec := serveRun(d, `{"argv":["x"],"stream":true}`)
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Fatalf("got status %d body %q", rec.Code, rec.Body.String())
	}
}

func TestStreamEarlyFailure(t *testing.T) {
	run := &stubRun{scripts: []stubOutcome{{code: 1, stderr: "boom"}}}
	d, _, _ := testDaemon(run)
	rec := serveRun(d, `{"argv":["x"],"stream":true}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("got status %d", rec.Code)
	}
	var resp struct {
		Code   int    `json:"code"`
		Error  string `json:"error"`
		Stderr string `json:"stderr"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Code != 1 || resp.Stderr != "boom" {
		t.Fatalf("got %+v", resp)
	}
}

func TestCappedBuffer(t *testing.T) {
	b := &cappedBuffer{cap: 5}
	if n, err := b.Write([]byte("hello world")); n != len("hello world") || err != nil {
		t.Fatalf("write reported %d, %v", n, err)
	}
	if !b.overflow || string(b.Bytes()) != "hello" {
		t.Fatalf("got overflow=%v bytes=%q", b.overflow, b.Bytes())
	}
	b.Reset()
	if b.overflow || len(b.Bytes()) != 0 {
		t.Fatal("reset did not clear")
	}
}

func TestHealthTransitionsNotifyOnce(t *testing.T) {
	var events []string
	h := newHealth(func(_ context.Context, event, domain string, _ DomainState) {
		events = append(events, event+":"+domain)
	})
	ctx := context.Background()
	// Startup round failing counts as a transition: unknown -> fail fires.
	h.set(ctx, DomainCanvas, stateFail, "dead")
	h.set(ctx, DomainCanvas, stateFail, "dead") // steady-state fail: silent
	h.set(ctx, DomainCanvas, stateOK, "")
	h.set(ctx, DomainCanvas, stateOK, "") // steady-state ok: silent
	want := []string{"fail:canvas", "recover:canvas"}
	if strings.Join(events, ",") != strings.Join(want, ",") {
		t.Fatalf("events %v, want %v", events, want)
	}
}

func TestProbeDomainUnconfigured(t *testing.T) {
	d, events, reloads := testDaemon(&stubRun{})
	d.hooks.Probes[DomainVideo] = func(context.Context) error { return ErrProbeUnconfigured }
	d.probeDomain(context.Background(), DomainVideo)
	st := d.health.snapshot()[DomainVideo]
	if st.State != stateUnknown || st.Detail != ErrProbeUnconfigured.Error() {
		t.Fatalf("video state %+v, want unknown with reason", st)
	}
	if len(*events) != 0 || len(*reloads) != 0 {
		t.Fatalf("events %v reloads %v, want none", *events, *reloads)
	}
}

func TestProbeDomainAuthReloadsAndReprobes(t *testing.T) {
	d, _, reloads := testDaemon(&stubRun{})
	calls := 0
	d.hooks.Probes[DomainMLearning] = func(context.Context) error {
		calls++
		if calls == 1 {
			return MarkAuth(io.EOF) // any marked error
		}
		return nil
	}
	d.probeDomain(context.Background(), DomainMLearning)
	if calls != 2 || len(*reloads) != 1 || (*reloads)[0] != DomainJAccount {
		t.Fatalf("probe calls %d reloads %v, want 2 and [jaccount]", calls, *reloads)
	}
	if st := d.health.snapshot()[DomainMLearning]; st.State != stateOK {
		t.Fatalf("mlearning state %q, want ok", st)
	}
}

func TestProbeDomainTransientKeepsState(t *testing.T) {
	d, events, reloads := testDaemon(&stubRun{})
	d.hooks.Probes[DomainCanvas] = func(context.Context) error { return io.EOF } // unmarked: transient
	d.probeDomain(context.Background(), DomainCanvas)
	st := d.health.snapshot()[DomainCanvas]
	if st.State != stateUnknown {
		t.Fatalf("canvas state %q, want unknown (untouched)", st.State)
	}
	if len(*events) != 0 || len(*reloads) != 0 {
		t.Fatalf("events %v reloads %v, want none", *events, *reloads)
	}
}
