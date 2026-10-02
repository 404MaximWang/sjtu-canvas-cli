package mlearning

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// TestValidateForscanURL pins the acceptance rules of scanned QR URLs:
// https scheme, exact mlearning host, forscan path prefix, and both
// roll-call parameters non-empty.
func TestValidateForscanURL(t *testing.T) {
	const valid = "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=tok123&signHistoryId=456"
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid", valid, false},
		{"trailing path segment", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/extra?rollCallToken=t&signHistoryId=1", false},
		{"extra query parameter", valid + "&from=qr", false},
		{"uppercase host", "https://MLEARNING.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", false},
		{"http scheme", "http://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", true},
		{"wrong host", "https://oc.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", true},
		{"host suffix trick", "https://mlearning.sjtu.edu.cn.evil.example/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", true},
		{"wrong path", "https://mlearning.sjtu.edu.cn/lms/mobile2/other/?rollCallToken=t&signHistoryId=1", true},
		{"path without trailing slash", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan?rollCallToken=t&signHistoryId=1", true},
		{"missing rollCallToken", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?signHistoryId=1", true},
		{"empty signHistoryId", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=", true},
		{"not a URL", "not a url", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, historyID, err := ValidateForscanURL(tc.url)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidURL) {
					t.Fatalf("want ErrInvalidURL, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token == "" || historyID == "" {
				t.Fatalf("extracted empty parameters: %q %q", token, historyID)
			}
		})
	}
}

// TestValidateForscanURLExtraction pins the exact parameter values a valid
// URL yields, including percent-decoding.
func TestValidateForscanURLExtraction(t *testing.T) {
	token, historyID, err := ValidateForscanURL(
		"https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=a%2Fb&signHistoryId=42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "a/b" || historyID != "42" {
		t.Fatalf("got %q %q", token, historyID)
	}
}

// TestCleanToken pins the cookie-value normalization: URL-unquote, strip
// surrounding quotes, reject empty results.
func TestCleanToken(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"plain JWT", "eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig", "eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig", false},
		{"quoted and escaped", "%22eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig%22", "eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig", false},
		{"bare quotes", `"tok"`, "tok", false},
		{"bad escape", "%zz", "", true},
		{"empty", `""`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanToken(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

// tokenStub fakes the mlearning host for token-refresh tests: the forscan
// trigger plants a fresh token cookie and counts chain runs; every other
// path is an API endpoint answered by respond with the caller's
// Authorization header.
type tokenStub struct {
	chainRuns int
	apiAuths  []string
	srv       *httptest.Server
	// forscan500 makes the trigger page plant the token cookie and then
	// answer 500, mirroring the real forscan page's behavior.
	forscan500 bool
}

// newTokenStub builds a Client pointed at a stub server, with a stale
// "stale-token" cookie pre-planted in the session jar.
func newTokenStub(t *testing.T, respond func(auth string) string) (*Client, *tokenStub) {
	t.Helper()
	stub := &tokenStub{}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, forscanPath) {
			stub.chainRuns++
			http.SetCookie(w, &http.Cookie{Name: "token", Value: "fresh-token", Path: "/"})
			if stub.forscan500 {
				w.WriteHeader(http.StatusInternalServerError)
				io.WriteString(w, "<html><title>We're sorry</title></html>")
				return
			}
			io.WriteString(w, "<html></html>")
			return
		}
		stub.apiAuths = append(stub.apiAuths, r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, respond(r.Header.Get("Authorization")))
	}))
	t.Cleanup(stub.srv.Close)

	sess, err := session.NewCookies()
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(stub.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	sess.SeedCookie("token", "stale-token", u.Hostname())
	client := New(sess)
	client.base = stub.srv.URL
	return client, stub
}

// TestGetRefreshesRejectedToken pins the 10001 recovery: a dead planted
// token is cleared, the SSO chain re-runs once, and the retried request
// carries the fresh token and passes the success response through.
func TestGetRefreshesRejectedToken(t *testing.T) {
	client, stub := newTokenStub(t, func(auth string) string {
		if auth == "fresh-token" {
			return `{"resultCode":"200","body":{"ok":true}}`
		}
		return `{"resultCode":"10001","body":null}`
	})
	raw, err := client.get(context.Background(), "/lms-lti-rollcall-sjtu/rollcall/existentB?courseCode=1")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"resultCode":"200","body":{"ok":true}}`; string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	if stub.chainRuns != 1 {
		t.Fatalf("SSO chain ran %d times, want 1", stub.chainRuns)
	}
	want := []string{"stale-token", "fresh-token"}
	if !slices.Equal(stub.apiAuths, want) {
		t.Fatalf("API authorizations %v, want %v", stub.apiAuths, want)
	}
}

// TestGetPassesThroughPersistentRejection pins the retry bound: when the
// fresh token is rejected too, the second response passes through verbatim
// and the chain does not loop.
func TestGetPassesThroughPersistentRejection(t *testing.T) {
	client, stub := newTokenStub(t, func(string) string {
		return `{"resultCode":"10001","body":null}`
	})
	raw, err := client.get(context.Background(), "/x")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"resultCode":"10001","body":null}`; string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	if stub.chainRuns != 1 || len(stub.apiAuths) != 2 {
		t.Fatalf("chain runs %d, API calls %d; want 1 and 2", stub.chainRuns, len(stub.apiAuths))
	}
}

// TestGetKeepsLiveToken guards the fast path: a working token triggers no
// chain run and the response passes through untouched.
func TestGetKeepsLiveToken(t *testing.T) {
	client, stub := newTokenStub(t, func(string) string {
		return `{"resultCode":"200","body":{}}`
	})
	raw, err := client.get(context.Background(), "/x")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"resultCode":"200","body":{}}`; string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	if stub.chainRuns != 0 {
		t.Fatalf("SSO chain ran %d times, want 0", stub.chainRuns)
	}
	if len(stub.apiAuths) != 1 || stub.apiAuths[0] != "stale-token" {
		t.Fatalf("API authorizations %v, want [stale-token]", stub.apiAuths)
	}
}

// TestChainToleratesTerminalPageFailure pins the SSO tolerance: the forscan
// page 500s after the callback planted the token cookie, and the chain
// still succeeds because the jar proves the cookie landed.
func TestChainToleratesTerminalPageFailure(t *testing.T) {
	client, stub := newTokenStub(t, func(string) string {
		return `{"resultCode":"200","body":{}}`
	})
	stub.forscan500 = true
	client.clearToken() // force the chain despite the pre-planted stale token
	raw, err := client.get(context.Background(), "/x")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"resultCode":"200","body":{}}`; string(raw) != want {
		t.Fatalf("got %s, want %s", raw, want)
	}
	if stub.chainRuns != 1 {
		t.Fatalf("SSO chain ran %d times, want 1", stub.chainRuns)
	}
	if len(stub.apiAuths) != 1 || stub.apiAuths[0] != "fresh-token" {
		t.Fatalf("API authorizations %v, want [fresh-token]", stub.apiAuths)
	}
}
