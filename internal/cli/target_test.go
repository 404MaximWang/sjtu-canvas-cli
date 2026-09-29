package cli

import "testing"

// TestCommandTarget pins the TUI argument rule: ls and stat default to the
// working directory, cat always requires an explicit path, and two paths are
// never accepted.
func TestCommandTarget(t *testing.T) {
	cases := []struct {
		name    string
		cwd     string
		args    []string
		want    string
		wantErr bool
	}{
		{"bare ls lists cwd", "/courses/69014", []string{"ls"}, "/courses/69014", false},
		{"bare stat describes cwd", "/courses", []string{"stat"}, "/courses", false},
		{"bare cat is usage error", "/", []string{"cat"}, "", true},
		{"relative path joins cwd", "/courses", []string{"ls", "69014/files"}, "/courses/69014/files", false},
		{"absolute path wins", "/courses", []string{"stat", "/courses/1"}, "/courses/1", false},
		{"parent traversal cleans", "/courses/69014", []string{"ls", "../69088"}, "/courses/69088", false},
		{"two paths rejected", "/", []string{"ls", "/a", "/b"}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := commandTarget(tc.cwd, tc.args)
			if (err != nil) != tc.wantErr {
				t.Fatalf("commandTarget(%q, %q) err = %v, wantErr %v", tc.cwd, tc.args, err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("commandTarget(%q, %q) = %q, want %q", tc.cwd, tc.args, got, tc.want)
			}
		})
	}
}
