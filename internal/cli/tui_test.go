package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// TestSplitLine pins the TUI command-line contract: words split on
// whitespace, single and double quotes group, backslash escapes, and shell
// operators are rejected rather than executed.
func TestSplitLine(t *testing.T) {
	cases := []struct {
		line string
		want []string
	}{
		{`ls /courses`, []string{"ls", "/courses"}},
		{`stat "/courses/10001/files/week 1.ppt"`, []string{"stat", "/courses/10001/files/week 1.ppt"}},
		{`cat '/it/单引号'`, []string{"cat", "/it/单引号"}},
		{`cat /a/b\ c`, []string{"cat", "/a/b c"}},
		{`ls ""`, []string{"ls", ""}},
		{`   `, nil},
	}
	for _, tc := range cases {
		got, err := splitLine(tc.line)
		if err != nil {
			t.Errorf("splitLine(%q): unexpected error %v", tc.line, err)
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("splitLine(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

// TestSplitLineRejectsShell pins that shell constructs are errors, never
// silently reinterpreted.
func TestSplitLineRejectsShell(t *testing.T) {
	for _, line := range []string{
		`ls /courses | grep x`,
		`cat /a > /tmp/b`,
		`ls $HOME`,
		"ls `id`",
		`ls "/courses/$USER"`,
		`ls /courses; cat /etc/passwd`,
	} {
		if _, err := splitLine(line); err == nil {
			t.Errorf("splitLine(%q) succeeded, want rejection", line)
		}
	}
}

// TestLastLogin checks that the most recent session record name wins and
// unrelated files are ignored.
func TestLastLogin(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"sjtu-session-20260920-090000.jsonl",
		"sjtu-session-20260924-131500.jsonl",
		"sjtu-session-bad.jsonl",
		"history",
		"sjtu.log",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := lastLogin(dir)
	want := time.Date(2026, 9, 24, 13, 15, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("lastLogin = %v, want %v", got, want)
	}
	if got := lastLogin(t.TempDir()); !got.IsZero() {
		t.Errorf("lastLogin(empty dir) = %v, want zero", got)
	}
}
