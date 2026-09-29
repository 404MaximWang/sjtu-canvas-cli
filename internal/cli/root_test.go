package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/cred"
	"github.com/404MaximWang/sjtu-canvas-cli/internal/session"
)

// TestClassify pins the output contract's error-code and exit-code mapping,
// including the auth paths that cannot be exercised without touching the
// user's real keychain.
func TestClassify(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
		wantExit int
	}{
		{"missing credential", cred.ErrNotFound, "auth_required", exitAuth},
		{"canvas 401", &session.HTTPError{Status: "401 Unauthorized"}, "auth_failed", exitAuth},
		{"canvas 403", &session.HTTPError{Status: "403 Forbidden"}, "error", exitError},
		{"missing path", &fs.PathError{Op: "open", Path: "courses/1", Err: fs.ErrNotExist}, "not_found", exitError},
		{"usage passthrough", fail("usage", "x", exitUsage), "usage", exitUsage},
		{"unknown command", errors.New(`unknown command "foo" for "sjtu"`), "usage", exitUsage},
		{"generic", errors.New("boom"), "error", exitError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err)
			if got.code != tc.wantCode || got.exit != tc.wantExit {
				t.Errorf("classify(%v) = {%s, exit %d}, want {%s, exit %d}",
					tc.err, got.code, got.exit, tc.wantCode, tc.wantExit)
			}
		})
	}
}

// TestReportErrorFormat pins the structured stderr shape:
// {"error": "...", "hint": "..."} as one compact JSON line.
func TestReportErrorFormat(t *testing.T) {
	var stderr bytes.Buffer
	exit := reportError(&stderr, fail("usage", "需要路径", exitUsage))
	if exit != exitUsage {
		t.Errorf("exit = %d, want %d", exit, exitUsage)
	}
	var parsed map[string]string
	if err := json.Unmarshal(stderr.Bytes(), &parsed); err != nil {
		t.Fatalf("stderr is not JSON: %q", stderr.String())
	}
	if parsed["error"] != "usage" || parsed["hint"] != "需要路径" {
		t.Errorf("stderr = %v, want error=usage hint=需要路径", parsed)
	}
}
