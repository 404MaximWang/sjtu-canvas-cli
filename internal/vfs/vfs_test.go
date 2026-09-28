package vfs

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/404MaximWang/sjtu-canvas-cli/internal/canvas"
)

// fakeSource is a static Source for tests. contents maps file ID to the
// bytes OpenFile streams.
type fakeSource struct {
	courses       []canvas.Course
	assignments   map[int64][]canvas.Assignment
	announcements map[int64][]canvas.DiscussionTopic
	discussions   map[int64][]canvas.DiscussionTopic
	folders       map[int64][]canvas.Folder
	files         map[int64][]canvas.File
	contents      map[int64]string
}

// Courses implements Source.
func (s *fakeSource) Courses(context.Context) ([]canvas.Course, error) { return s.courses, nil }

// Assignments implements Source.
func (s *fakeSource) Assignments(_ context.Context, courseID int64) ([]canvas.Assignment, error) {
	return s.assignments[courseID], nil
}

// Announcements implements Source.
func (s *fakeSource) Announcements(_ context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	return s.announcements[courseID], nil
}

// Discussions implements Source.
func (s *fakeSource) Discussions(_ context.Context, courseID int64) ([]canvas.DiscussionTopic, error) {
	return s.discussions[courseID], nil
}

// Folders implements Source.
func (s *fakeSource) Folders(_ context.Context, courseID int64) ([]canvas.Folder, error) {
	return s.folders[courseID], nil
}

// Files implements Source.
func (s *fakeSource) Files(_ context.Context, courseID int64) ([]canvas.File, error) {
	return s.files[courseID], nil
}

// OpenFile implements Source.
func (s *fakeSource) OpenFile(_ context.Context, file canvas.File) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader(s.contents[file.ID])), nil
}

// newTestFS builds an FS over the standard fixture: two courses in two
// terms, one with a full complement of entities. The stubbed clock sits
// inside term 2025-2026-1, making it the current term.
func newTestFS() *FS {
	root := int64(9001)
	src := &fakeSource{
		courses: []canvas.Course{
			{
				ID:   10001,
				Name: "数学分析",
				Term: canvas.Term{ID: 1, Name: "2025-2026 Fall",
					StartAt: "2025-09-01T00:00:00Z", EndAt: "2026-01-20T00:00:00Z"},
			},
			{
				ID:   10002,
				Name: "Physics/Mechanics", // slash exercises name sanitization
				Term: canvas.Term{ID: 2, Name: "2024-2025 Spring",
					StartAt: "2025-02-17T00:00:00Z", EndAt: "2025-06-30T00:00:00Z"},
			},
		},
		assignments: map[int64][]canvas.Assignment{
			10001: {{ID: 5001, Name: "作业一", UpdatedAt: "2025-09-10T08:00:00Z",
				Submission: &canvas.Submission{ID: 8801, WorkflowState: "graded",
					Grade: "95", SubmittedAt: "2025-09-09T10:00:00Z"}}},
		},
		announcements: map[int64][]canvas.DiscussionTopic{
			10001: {{ID: 6001, Title: "欢迎"}},
		},
		discussions: map[int64][]canvas.DiscussionTopic{
			10001: {{ID: 6002, Title: "第一题答疑"}},
		},
		folders: map[int64][]canvas.Folder{
			10001: {
				{ID: 9001, Name: "course files", FullName: "course files"},
				{ID: 9002, Name: "lectures", FullName: "course files/lectures", ParentFolderID: &root},
			},
		},
		files: map[int64][]canvas.File{
			10001: {
				{ID: 7001, FolderID: 9001, DisplayName: "syllabus.pdf", Size: 5,
					UpdatedAt: "2025-09-05T09:00:00Z"},
				{ID: 7002, FolderID: 9002, DisplayName: "week 1.ppt", Size: 4},
			},
		},
		contents: map[int64]string{7001: "hello", 7002: "ppt!"},
	}
	fsys := New(context.Background(), src)
	fsys.now = func() time.Time { return time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC) }
	return fsys
}

// TestFSCompliance runs the standard library's conformance suite, which
// exercises directory reads, chunked ReadDir, Stat/Open agreement, and the
// ReadLinkFS symlink checks.
func TestFSCompliance(t *testing.T) {
	err := fstest.TestFS(newTestFS(),
		"courses",
		"courses/10001",
		"courses/10001/announcements/6001/info",
		"courses/10001/discussions/6002/info",
		"courses/10001/assignments/5001/info",
		"courses/10001/assignments/5001/submissions/latest",
		"courses/10001/attendance",
		"courses/10001/files/syllabus.pdf",
		"courses/10001/files/lectures/week 1.ppt",
		"courses/10002",
		"courses/current",
		"courses/2025-2026-1",
		"courses/2025-2026-1/10001",
		"courses/2025-2026-1/数学分析",
		"courses/2024-2025-2/Physics／Mechanics",
	)
	if err != nil {
		t.Fatal(err)
	}
}

// TestSymlinkSemantics checks that Open/Stat follow links while Lstat and
// ReadLink describe the link itself.
func TestSymlinkSemantics(t *testing.T) {
	fsys := newTestFS()

	target, err := fsys.ReadLink("courses/current")
	if err != nil {
		t.Fatalf("ReadLink: %v", err)
	}
	if target != "/courses/2025-2026-1" {
		t.Errorf("current → %q, want /courses/2025-2026-1", target)
	}

	info, err := fsys.Lstat("courses/current")
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("Lstat mode %v, want symlink bit", info.Mode())
	}

	// Stat follows the whole chain: current → term dir → course ID link.
	info, err = fs.Stat(fsys, "courses/current/10001")
	if err != nil {
		t.Fatalf("Stat through symlink chain: %v", err)
	}
	if !info.IsDir() {
		t.Errorf("Stat(courses/current/10001) = %v, want directory", info.Mode())
	}

	// The course-name link resolves through the term directory to the real
	// course directory.
	data, err := fs.ReadFile(fsys, "courses/2025-2026-1/数学分析/assignments/5001/info")
	if err != nil {
		t.Fatalf("ReadFile through name link: %v", err)
	}
	if !strings.Contains(string(data), "作业一") {
		t.Errorf("assignment JSON = %s, want it to name the assignment", data)
	}
}

// TestRemoteFileContent checks that a Canvas file streams its content.
func TestRemoteFileContent(t *testing.T) {
	fsys := newTestFS()
	data, err := fs.ReadFile(fsys, "courses/10001/files/syllabus.pdf")
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "hello" {
		t.Errorf("content = %q, want %q", data, "hello")
	}
}

// TestNotFound checks the missing-path error carries fs.ErrNotExist so the
// CLI can map it to the structured not_found error.
func TestNotFound(t *testing.T) {
	_, err := newTestFS().Open("courses/99999")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Open(missing) error = %v, want fs.ErrNotExist", err)
	}
}

// TestCurrentTermRule pins the /courses/current selection: a containing term
// wins; otherwise the latest start_at wins; terms without a parseable
// start_at never win.
func TestCurrentTermRule(t *testing.T) {
	terms := map[string]canvas.Term{
		"2024-2025-2": {Name: "2024-2025-2", StartAt: "2025-02-17T00:00:00Z", EndAt: "2025-06-30T00:00:00Z"},
		"2025-2026-1": {Name: "2025-2026-1", StartAt: "2025-09-01T00:00:00Z", EndAt: "2026-01-20T00:00:00Z"},
		"unknown":     {Name: "unknown"},
	}

	inside := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	if got := currentTermName(terms, inside); got != "2025-2026-1" {
		t.Errorf("inside a term: got %q, want 2025-2026-1", got)
	}

	between := time.Date(2025, 7, 15, 0, 0, 0, 0, time.UTC)
	if got := currentTermName(terms, between); got != "2025-2026-1" {
		t.Errorf("between terms: got %q, want latest start 2025-2026-1", got)
	}

	if got := currentTermName(map[string]canvas.Term{"unknown": {Name: "unknown"}}, inside); got != "" {
		t.Errorf("no parseable term: got %q, want empty", got)
	}
}

// TestTermLabel pins the 202X-202Y-Z directory naming: canonical season
// names map to semester numbers; anything else yields no label.
func TestTermLabel(t *testing.T) {
	cases := []struct {
		term canvas.Term
		want string
	}{
		{canvas.Term{Name: "2026-2027 Fall"}, "2026-2027-1"},
		{canvas.Term{Name: "2025-2026 Spring"}, "2025-2026-2"},
		{canvas.Term{Name: "2025-2026 Summer"}, "2025-2026-3"},
		{canvas.Term{Name: "自 定义 学期"}, ""},
		{canvas.Term{}, ""},
	}
	for _, tc := range cases {
		if got := termLabel(tc.term); got != tc.want {
			t.Errorf("termLabel(%+v) = %q, want %q", tc.term, got, tc.want)
		}
	}
}
